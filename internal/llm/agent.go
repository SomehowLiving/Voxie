package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// agentClient forwards each user turn to an external "bring your own agent"
// HTTP endpoint and streams the reply back into the voice pipeline. The
// external service owns the conversation — memory, prompting, and tool use —
// keyed by the session_id sent with every request; the server contributes
// only speech-to-text in and text-to-speech out.
//
// Request (POST <url>, application/json):
//
//	{
//	  "session_id":       "9f2c…", // stable for the conversation; rotates on Reset
//	  "resource_id":      "…",     // who is on the call; omitted when unknown
//	  "type":             "chat",  // "chat" for a user turn, "oneshot" for a stateless transform
//	  "text":             "…",     // exactly what the caller said
//	  "language":         "ta",    // the language it was heard in, when known
//	  "system":           "…",     // skill text, plus any instruction for this turn
//	  "interrupted_text": "…",     // what the agent was saying when cut off
//	  "context":          ["…"],   // retrieved chunks, when RAG is on
//	  "summary":          "…"      // rolling digest of earlier turns
//	}
//
// session_id scopes one conversation, resource_id the person behind it, so an
// agent can remember a caller between calls. It comes from a verified JWT claim
// or a trusted server-side caller, and survives a resume unchanged.
//
// Everything after system is context rather than speech, and is kept out of
// text on purpose: an agent that stores what it receives should end up with the
// caller's words in its history, not the server's scaffolding around them.
//
// Accepted responses, by Content-Type:
//
//	text/event-stream   "data:" lines, each either raw text or JSON
//	                    {"delta": "…"}; "[DONE]" ends the stream early
//	text/plain          chunked text, spoken as it arrives
//	application/json    {"text": "…"} (or {"response": "…"}), buffered
type agentClient struct {
	url    string
	apiKey string
	http   *http.Client

	// Fixed for the life of the client. Unlike sessionID, a Reset leaves it
	// alone: the conversation starts over, the caller has not changed.
	resourceID string

	mu          sync.Mutex
	sessionID   string
	extraSystem string // accumulated skill text, forwarded on every turn

	// endCall is set when a chat reply carries "end_call": true (JSON replies
	// only) and consumed by TakeEndCall.
	endCall atomic.Bool
}

// TakeEndCall implements CallEnder: whether the last chat reply asked to end
// the call, clearing the flag so it applies to exactly one response.
func (c *agentClient) TakeEndCall() bool { return c.endCall.Swap(false) }

// NewAgentClient returns a Client that proxies turns to an external agent
// endpoint. timeoutMs bounds a whole turn including streaming the reply;
// zero means 60s. An empty resourceID is omitted from every request.
func NewAgentClient(url, apiKey string, timeoutMs int, resourceID string) Client {
	if timeoutMs <= 0 {
		timeoutMs = 60000
	}
	// Keep a warm connection to the agent — a cold TLS handshake on the
	// first turn is user-audible latency.
	transport := &http.Transport{
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &agentClient{
		url:        url,
		apiKey:     apiKey,
		http:       &http.Client{Transport: transport, Timeout: time.Duration(timeoutMs) * time.Millisecond},
		sessionID:  newAgentSessionID(),
		resourceID: resourceID,
	}
}

func newAgentSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type agentRequest struct {
	SessionID  string `json:"session_id"`
	ResourceID string `json:"resource_id,omitempty"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	// Language is the language the caller spoke this turn in (chat only).
	Language string `json:"language,omitempty"`
	System   string `json:"system,omitempty"`

	// Context the pipeline gathered for this turn, kept out of Text so an agent
	// that persists what it receives stores the caller's words and nothing else.
	InterruptedText string   `json:"interrupted_text,omitempty"`
	Context         []string `json:"context,omitempty"`
	Summary         string   `json:"summary,omitempty"`
}

func (c *agentClient) Chat(ctx context.Context, turn Turn, onChunk func(string), onSentence func(string)) (string, error) {
	c.mu.Lock()
	req := agentRequest{
		SessionID:  c.sessionID,
		ResourceID: c.resourceID,
		Type:       "chat",
		Text:       turn.Text,
		Language:   turn.Language,
		// Note is an instruction for this turn only, so it rides with the skill
		// text rather than accumulating into it.
		System:          c.extraSystem + turn.Note,
		InterruptedText: turn.InterruptedText,
		Context:         turn.Context,
		Summary:         turn.Summary,
	}
	c.mu.Unlock()

	result, err := c.do(ctx, req, onChunk, onSentence)
	if err != nil {
		return result, err
	}
	log.Printf("[llm] agent response: %s", truncate(result, 80))
	return result, nil
}

// Greeting implements Greeter: it asks the agent for the call's opening line
// with a "greeting" request (no text -- nobody has spoken yet), so the
// agent can name the person and the reason for the call. Buffered rather
// than streamed: it's one short line, and the pipeline speaks it through
// the same interruptible path as a static greeting.
func (c *agentClient) Greeting(ctx context.Context) (string, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()

	out, err := c.do(ctx, agentRequest{
		SessionID:  sessionID,
		ResourceID: c.resourceID,
		Type:       "greeting",
	}, nil, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ListenHint implements ListenHinter with a "listen" request: what the
// agent knows of the caller's language before they speak. JSON reply
// {"language": "ta", "region": "IN"}, both optional. An agent that doesn't
// know the type may answer anything; only a 2xx JSON object is read.
func (c *agentClient) ListenHint(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	body, err := json.Marshal(agentRequest{SessionID: sessionID, ResourceID: c.resourceID, Type: "listen"})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("agent endpoint returned %d", resp.StatusCode)
	}
	var hint struct {
		Language string `json:"language"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&hint); err != nil {
		return "", "", fmt.Errorf("listen reply: %w", err)
	}
	return strings.TrimSpace(hint.Language), strings.TrimSpace(hint.Region), nil
}

func (c *agentClient) OneShot(ctx context.Context, system, user string) (string, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()

	out, err := c.do(ctx, agentRequest{
		SessionID:  sessionID,
		ResourceID: c.resourceID,
		Type:       "oneshot",
		Text:       user,
		System:     system,
	}, nil, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// do posts one request and streams the reply through the chunk/sentence
// callbacks, returning the full concatenated text.
func (c *agentClient) do(ctx context.Context, payload agentRequest, onChunk func(string), onSentence func(string)) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("agent request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, text/plain, application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("agent endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("agent endpoint returned %d: %s", resp.StatusCode, truncate(string(snippet), 200))
	}

	var fullResponse strings.Builder
	var sentenceBuf strings.Builder
	spokeFirst := false // whether the reply's first piece has gone to the voice
	// speak hands a finished sentence to the voice. A long first sentence
	// goes clause first: a slow voice (Sarvam, ~1-3s a sentence) starts
	// speaking sooner, and the rest is voiced while the clause plays.
	speak := func(sentence string) {
		if !spokeFirst {
			if cut := firstClauseCut(sentence); cut > 0 {
				onSentence(strings.TrimSpace(sentence[:cut]))
				// The rest keeps the sentence's language tag, or the voice
				// would guess its language from the script.
				sentence = leadingLangTag(sentence) + strings.TrimLeft(sentence[cut:], " ")
			}
		}
		if trimmed := strings.TrimSpace(sentence); trimmed != "" {
			onSentence(trimmed)
		}
		spokeFirst = true
	}
	emit := func(chunk string) {
		if chunk == "" {
			return
		}
		fullResponse.WriteString(chunk)
		sentenceBuf.WriteString(chunk)
		if onChunk != nil {
			onChunk(chunk)
		}
		if onSentence != nil {
			text := sentenceBuf.String()
			if idx := findSentenceEnd(text); idx >= 0 {
				rest := text[idx+1:]
				speak(text[:idx+1])
				sentenceBuf.Reset()
				sentenceBuf.WriteString(rest)
			}
		}
	}

	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		var endCall bool
		endCall, err = readSSE(resp.Body, emit)
		if endCall && payload.Type == "chat" {
			c.endCall.Store(true)
		}
	case strings.HasPrefix(contentType, "application/json"):
		var endCall bool
		endCall, err = readJSONReply(resp.Body, emit)
		if endCall && payload.Type == "chat" {
			c.endCall.Store(true)
		}
	default:
		err = readTextStream(resp.Body, emit)
	}
	if err != nil {
		return fullResponse.String(), fmt.Errorf("agent reply: %w", err)
	}

	if onSentence != nil {
		if remaining := strings.TrimSpace(sentenceBuf.String()); remaining != "" {
			speak(remaining) // a one-sentence reply is often only flushed here
		}
	}
	return fullResponse.String(), nil
}

// readSSE emits the payload of each "data:" line. A line is treated as JSON
// {"delta": "…"} when it parses as an object; anything else is raw text, so
// trivial agents can just print data lines without JSON-encoding.
func readSSE(r io.Reader, emit func(string)) (bool, error) {
	endCall := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:/id:/retry:/comments/blank separators
		}
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			return endCall, nil
		}
		if strings.HasPrefix(data, "{") {
			var event struct {
				Delta   string `json:"delta"`
				EndCall bool   `json:"end_call"`
			}
			if json.Unmarshal([]byte(data), &event) == nil {
				// {"end_call": true}, on its own or with the last delta,
				// works as in a JSON reply: end once the reply has played.
				endCall = endCall || event.EndCall
				emit(event.Delta)
				continue
			}
		}
		emit(data)
	}
	return endCall, scanner.Err()
}

// readTextStream emits raw bytes as they arrive, holding back any trailing
// partial UTF-8 rune so multi-byte characters split across reads stay intact.
func readTextStream(r io.Reader, emit func(string)) error {
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			valid := len(pending)
			for valid > 0 && !utf8.Valid(pending[:valid]) {
				valid--
			}
			emit(string(pending[:valid]))
			pending = pending[valid:]
		}
		if err == io.EOF {
			if len(pending) > 0 {
				emit(string(pending))
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readJSONReply emits a buffered JSON reply's text and reports whether it
// also asked to end the call ("end_call": true) after this line is spoken.
func readJSONReply(r io.Reader, emit func(string)) (bool, error) {
	body, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return false, err
	}
	var reply struct {
		Text     string `json:"text"`
		Response string `json:"response"`
		EndCall  bool   `json:"end_call"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return false, fmt.Errorf("parse json reply: %w", err)
	}
	text := reply.Text
	if text == "" {
		text = reply.Response
	}
	// Hand a buffered reply over one sentence at a time, so the first
	// sentence is synthesized and playing while the rest are still being
	// voiced. Emitted whole, the splitter cut it only at its LAST sentence
	// end, and a three-sentence reply from a slower voice (Sarvam, ~1s a
	// sentence) sat silent until nearly all of it was ready.
	for _, sentence := range splitSentences(text) {
		emit(sentence)
	}
	return reply.EndCall, nil
}

// leadingLangTag returns a sentence's "[lang:xx] " prefix, if it has one.
func leadingLangTag(sentence string) string {
	if m := langTagPrefix.FindString(sentence); m != "" {
		return strings.TrimSpace(m) + " "
	}
	return ""
}

var langTagPrefix = regexp.MustCompile(`^\s*\[lang:[A-Za-z]{2,3}(?:-[A-Za-z]{2})?\]`)

// firstClauseCut returns where to cut a long sentence after its first
// clause (a comma, Arabic or CJK comma, or a dash), so the voice can start on
// it; 0 when the sentence is short or has no clause break far enough in to
// be worth saying on its own.
func firstClauseCut(sentence string) int {
	const minSentence, minClause, minRest = 40, 12, 12
	total := utf8.RuneCountInString(sentence)
	if total < minSentence {
		return 0
	}
	runes := 0
	for i, r := range sentence {
		runes++
		cut := 0
		switch r {
		case '،', '，', '、':
			cut = i + utf8.RuneLen(r)
		case ',', '—':
			// Only as punctuation: "₹4,999" has a comma too.
			if next := i + utf8.RuneLen(r); next < len(sentence) && sentence[next] == ' ' {
				cut = next
			}
		}
		if cut > 0 && runes >= minClause && total-runes >= minRest {
			return cut
		}
	}
	return 0
}

// splitSentences cuts text after each sentence ender that is followed by a
// space or the end ("First. Second? Third।"), keeping each ender and the
// space after it with its sentence. Joining the parts gives back the text.
func splitSentences(text string) []string {
	var parts []string
	start := 0
	runes := []rune(text)
	pos := 0 // byte offset of runes[i]
	for i, r := range runes {
		width := utf8.RuneLen(r)
		ender := r == '.' || r == '!' || r == '?' || r == '।' || r == '॥' || r == '。' || r == '！' || r == '？'
		if ender {
			end := pos + width
			// A following space belongs to this sentence; a following
			// letter or digit (an email, "4.5") means no break here.
			if i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' {
				if i+1 < len(runes) {
					end++
				}
				parts = append(parts, text[start:end])
				start = end
			} else if r == '।' || r == '॥' || r == '。' || r == '！' || r == '？' {
				parts = append(parts, text[start:end])
				start = end
			}
		}
		pos += width
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	return parts
}

// SetTools is a no-op: the external agent owns its own tools. Server-side
// plugins and skills are not forwarded in agent mode.
func (c *agentClient) SetTools(tools []ToolDefinition) {
	if len(tools) > 0 {
		log.Printf("[llm] agent provider ignores %d server-side tools (the agent owns tool use)", len(tools))
	}
}

func (c *agentClient) SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error)) {
}

func (c *agentClient) AppendSystemPrompt(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extraSystem += text
}

// Reset rotates the session ID so the agent starts a fresh conversation.
func (c *agentClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = newAgentSessionID()
}
