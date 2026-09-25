package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureAgent stands in for an external agent, recording what it is sent and
// replying with a fixed line.
type captureAgent struct {
	srv      *httptest.Server
	requests []agentRequest
}

func newCaptureAgent(t *testing.T) *captureAgent {
	t.Helper()

	c := &captureAgent{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req agentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("agent received unparseable body %q: %v", body, err)
		}
		c.requests = append(c.requests, req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"ok."}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureAgent) client(resourceID string) Client {
	return NewAgentClient(c.srv.URL, "", 0, resourceID)
}

func TestChatForwardsTheResourceID(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.Chat(context.Background(), Turn{Text: "hello", Prompt: "hello"}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(agent.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(agent.requests))
	}
	if got := agent.requests[0].ResourceID; got != "user_8891" {
		t.Fatalf("resource_id = %q, want user_8891", got)
	}
}

// The greeting request carries no text (nobody has spoken), the conversation's
// session_id, and the resource_id -- the agent needs the last one to know who
// it's calling before the first turn.
func TestGreetingSendsTypeGreetingWithSessionAndResource(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("+919876543210")

	g, ok := client.(Greeter)
	if !ok {
		t.Fatal("agent client does not implement Greeter")
	}
	text, err := g.Greeting(context.Background())
	if err != nil {
		t.Fatalf("Greeting: %v", err)
	}
	if text != "ok." {
		t.Fatalf("Greeting returned %q, want the agent's reply", text)
	}

	if len(agent.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(agent.requests))
	}
	req := agent.requests[0]
	if req.Type != "greeting" || req.Text != "" || req.ResourceID != "+919876543210" || req.SessionID == "" {
		t.Fatalf("greeting request = %+v, want type=greeting, empty text, resource and session set", req)
	}

	// The same session_id must carry into the first chat turn, so the agent
	// sees one conversation, not two.
	if _, err := client.Chat(context.Background(), Turn{Text: "hello?", Prompt: "hello?"}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if agent.requests[1].SessionID != req.SessionID {
		t.Fatalf("chat session_id %q differs from greeting session_id %q", agent.requests[1].SessionID, req.SessionID)
	}
}

// The rolling summary uses the same endpoint, so it needs the resource too.
func TestOneShotForwardsTheResourceID(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.OneShot(context.Background(), "summarise", "the call"); err != nil {
		t.Fatalf("OneShot: %v", err)
	}

	if got := agent.requests[0].ResourceID; got != "user_8891" {
		t.Fatalf("resource_id = %q, want user_8891", got)
	}
}

// Omit the field entirely when anonymous. An empty string would pool every
// anonymous caller into one shared resource.
func TestResourceIDIsOmittedWhenUnknown(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"ok."}`))
	}))
	defer srv.Close()

	client := NewAgentClient(srv.URL, "", 0, "")
	if _, err := client.Chat(context.Background(), Turn{Text: "hello", Prompt: "hello"}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, present := fields["resource_id"]; present {
		t.Fatalf("resource_id present in an anonymous request: %s", body)
	}
}

// The whole reason Turn is split: an agent that persists what it receives must
// end up with the caller's words in its thread, not the pipeline's scaffolding.
// Prompt carries that scaffolding for stateless models and must never be sent.
func TestContextTravelsBesideTextNotInsideIt(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	turn := Turn{
		Text:            "what's my balance",
		Prompt:          "[Context:\nbalance is 42\n]\n\nUser: what's my balance",
		InterruptedText: "your account was",
		Context:         []string{"balance is 42"},
		Summary:         "caller asked about fees earlier",
		Note:            " (speech was unclear, ask them to repeat)",
	}
	if _, err := client.Chat(context.Background(), turn, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	got := agent.requests[0]
	if got.Text != "what's my balance" {
		t.Errorf("text = %q, want the caller's words verbatim", got.Text)
	}
	if strings.Contains(got.Text, "[Context:") {
		t.Errorf("prompt scaffolding leaked into text: %q", got.Text)
	}
	if got.InterruptedText != "your account was" {
		t.Errorf("interrupted_text = %q", got.InterruptedText)
	}
	if len(got.Context) != 1 || got.Context[0] != "balance is 42" {
		t.Errorf("context = %v", got.Context)
	}
	if got.Summary != "caller asked about fees earlier" {
		t.Errorf("summary = %q", got.Summary)
	}
	// The note is an instruction, so it rides with the skill text.
	if !strings.Contains(got.System, "ask them to repeat") {
		t.Errorf("system = %q, want the per-turn note appended", got.System)
	}
}

// A plain turn carries no extras, so they stay off the wire entirely.
func TestPlainTurnSendsNoContextFields(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"ok."}`))
	}))
	defer srv.Close()

	client := NewAgentClient(srv.URL, "", 0, "")
	if _, err := client.Chat(context.Background(), Turn{Text: "hi", Prompt: "hi"}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for _, absent := range []string{"interrupted_text", "context", "summary", "resource_id"} {
		if _, present := fields[absent]; present {
			t.Errorf("%s present on a plain turn: %s", absent, body)
		}
	}
}

// Reset drops the history, not the person.
func TestResetRotatesTheSessionButKeepsTheResource(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.Chat(context.Background(), Turn{Text: "first", Prompt: "first"}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	client.Reset()
	if _, err := client.Chat(context.Background(), Turn{Text: "second", Prompt: "second"}, nil, nil); err != nil {
		t.Fatalf("Chat after reset: %v", err)
	}

	before, after := agent.requests[0], agent.requests[1]
	if before.SessionID == after.SessionID {
		t.Fatal("session_id survived a Reset")
	}
	if after.ResourceID != "user_8891" {
		t.Fatalf("resource_id after reset = %q, want user_8891", after.ResourceID)
	}
}

func newReplyAgent(t *testing.T, reply string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return NewAgentClient(srv.URL, "", 0, "")
}

// A JSON chat reply with end_call asks for the call to end after it's spoken,
// exactly once.
func TestChatReplyEndCallIsTakenOnce(t *testing.T) {
	client := newReplyAgent(t, `{"text":"Sorry for the trouble. Goodbye.","end_call":true}`)
	text, err := client.Chat(context.Background(), Turn{Text: "it's not mine", Prompt: "it's not mine"}, nil, nil)
	if err != nil || text != "Sorry for the trouble. Goodbye." {
		t.Fatalf("Chat = %q, %v", text, err)
	}
	ender, ok := client.(CallEnder)
	if !ok {
		t.Fatal("agent client does not implement CallEnder")
	}
	if !ender.TakeEndCall() {
		t.Fatal("end_call was not recorded")
	}
	if ender.TakeEndCall() {
		t.Fatal("end_call must apply to one response only")
	}
}

func TestChatReplyWithoutEndCallDoesNotEndTheCall(t *testing.T) {
	client := newReplyAgent(t, `{"text":"Do you have a minute?"}`)
	if _, err := client.Chat(context.Background(), Turn{Text: "hi", Prompt: "hi"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.(CallEnder).TakeEndCall() {
		t.Fatal("a reply without end_call ended the call")
	}
}

// Only a chat reply can end the call -- not the greeting or background work.
func TestEndCallOnGreetingIsIgnored(t *testing.T) {
	client := newReplyAgent(t, `{"text":"Hi, this is Voxie.","end_call":true}`)
	if _, err := client.(Greeter).Greeting(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.(CallEnder).TakeEndCall() {
		t.Fatal("end_call on a greeting reply must be ignored")
	}
}

func TestListenHintSendsTypeListenAndReadsTheReply(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("+919876543210")
	h, ok := client.(ListenHinter)
	if !ok {
		t.Fatal("agent client does not implement ListenHinter")
	}
	if _, _, err := h.ListenHint(context.Background()); err != nil {
		// The capture agent replies with plain text, not a hint object.
		t.Logf("non-JSON reply: %v", err)
	}
	if len(agent.requests) != 1 || agent.requests[0].Type != "listen" || agent.requests[0].ResourceID != "+919876543210" {
		t.Fatalf("listen request = %+v", agent.requests)
	}

	lang, region, err := newReplyAgent(t, `{"language":"ta","region":"IN"}`).(ListenHinter).ListenHint(context.Background())
	if err != nil || lang != "ta" || region != "IN" {
		t.Fatalf("ListenHint = %q, %q, %v", lang, region, err)
	}
	lang, region, err = newReplyAgent(t, `{}`).(ListenHinter).ListenHint(context.Background())
	if err != nil || lang != "" || region != "" {
		t.Fatalf("an empty hint = %q, %q, %v", lang, region, err)
	}
}

func TestChatSendsTheLanguageTheCallerSpoke(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("")
	if _, err := client.Chat(context.Background(), Turn{Text: "வணக்கம்", Prompt: "வணக்கம்", Language: "ta"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Chat(context.Background(), Turn{Text: "hmm", Prompt: "hmm"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if agent.requests[0].Language != "ta" || agent.requests[1].Language != "" {
		t.Fatalf("languages sent = %q, %q; want ta, then none", agent.requests[0].Language, agent.requests[1].Language)
	}
}

func TestLongFirstSentenceGoesToTheVoiceClauseFirst(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  []string
	}{
		{"hindi greeting", `{"text":"नमस्ते सोफ़िया, मैं REX हूँ, आपके प्रो अकाउंट से कॉल कर रहा हूँ। क्या आपके पास एक मिनट है?"}`,
			[]string{"नमस्ते सोफ़िया,", "मैं REX हूँ, आपके प्रो अकाउंट से कॉल कर रहा हूँ।", "क्या आपके पास एक मिनट है?"}},
		// The rest of a cut sentence keeps its language tag.
		{"tagged", `{"text":"[lang:mr] नमस्कार सोफिया, मी REX बोलतोय, तुमच्या प्रो खात्यातून कॉल करतोय."}`,
			[]string{"[lang:mr] नमस्कार सोफिया,", "[lang:mr] मी REX बोलतोय, तुमच्या प्रो खात्यातून कॉल करतोय."}},
		// A clause too short to be worth saying alone ("Hi Sofía,") is kept.
		{"short first clause", `{"text":"Hi Sofía, this is REX calling from your Pro account. Do you have a minute?"}`,
			[]string{"Hi Sofía, this is REX calling from your Pro account.", "Do you have a minute?"}},
		// A comma inside a number is not a clause break.
		{"number", `{"text":"Your payment of ₹4,999 didn't go through this morning at all. Sorry."}`,
			[]string{"Your payment of ₹4,999 didn't go through this morning at all.", "Sorry."}},
		// Only the reply's first sentence is split.
		{"later sentences whole", `{"text":"Understood. I'll hold your account until Monday, and send you the link now."}`,
			[]string{"Understood.", "I'll hold your account until Monday, and send you the link now."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			client := newReplyAgent(t, c.reply)
			if _, err := client.Chat(context.Background(), Turn{Text: "hi", Prompt: "hi"}, nil, func(s string) { got = append(got, s) }); err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("sentences to the voice:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestStreamedReplyIsVoicedAsItArrivesAndCanEndTheCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, event := range []string{
			`{"delta":"[lang:hi] धन्यवाद, सोफ़िया। "}`,
			`{"delta":"[lang:hi] आपका दिन शुभ हो।"}`,
			`{"end_call":true}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	client := NewAgentClient(srv.URL, "", 0, "")

	var sentences []string
	if _, err := client.Chat(context.Background(), Turn{Text: "bye", Prompt: "bye"}, nil, func(s string) { sentences = append(sentences, s) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"[lang:hi] धन्यवाद, सोफ़िया।", "[lang:hi] आपका दिन शुभ हो।"}
	if strings.Join(sentences, "|") != strings.Join(want, "|") {
		t.Fatalf("sentences = %q, want %q", sentences, want)
	}
	if !client.(CallEnder).TakeEndCall() {
		t.Fatal(`{"end_call": true} in a streamed reply must end the call`)
	}
}
