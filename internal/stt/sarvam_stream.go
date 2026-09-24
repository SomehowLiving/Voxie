package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// Sarvam AI streaming speech-to-text: one WebSocket per call, raw audio in,
// one transcript per utterance out. Sarvam segments utterances itself (it
// sends START_SPEECH / END_SPEECH events), and each transcript arrives
// ~0.1-0.2s after the caller stops -- measured live, with the language it
// detected (hi-IN, ta-IN, bn-IN, ...). Its strength is Indian languages:
// all ten tested (hi, bn, ta, te, kn, ml, mr, gu, pa, en-IN) came back
// word-perfect with the right language, where Deepgram's multilingual
// model garbled Tamil/Telugu/Marathi and labelled them Hindi. It is weak
// outside them: Spanish and German come back as-is, French is silently
// translated to English, Japanese is nonsense -- all labelled en-IN.
//
//   Endpoint: wss://api.sarvam.ai/speech-to-text/ws?language-code=..&model=..
//   Auth:     api-subscription-key header
//   Send:     {"audio":{"data":<base64 PCM16>,"sample_rate":"16000","encoding":"audio/wav"}}
//   Receive:  {"type":"data","data":{"transcript","language_code","language_probability"}}
//             {"type":"events","data":{"signal_type":"START_SPEECH"|"END_SPEECH"}}
//
// No partial transcripts: EmitsPartials() is false, so the pipeline's
// barge-in runs on VAD alone after the backchannel window.

// sarvamStreamURL is a var only so tests can point it at a local server.
var sarvamStreamURL = "wss://api.sarvam.ai/speech-to-text/ws"

const (
	sarvamStreamSampleRate = 16000
	// ~100ms per message: small enough to keep latency low, large enough
	// not to flood the socket with 50 tiny JSON frames a second.
	sarvamStreamChunkBytes   = sarvamStreamSampleRate * 2 * 100 / 1000
	sarvamStreamWriteWait    = 5 * time.Second
	sarvamDefaultStreamModel = "saarika:v2.5"
)

type sarvamStreamClient struct {
	conn     *websocket.Conn
	onResult func(TranscriptResult)
	ctx      context.Context
	cancel   context.CancelFunc

	writeMu sync.Mutex // gorilla/websocket allows one concurrent writer
	pending []byte     // audio not yet sent, guarded by writeMu
	closed  atomic.Bool
	// failed is set when the connection drops without Close being called,
	// so the next SendAudio reports it -- which is what lets a failover
	// client move on to another provider mid-call.
	failed atomic.Bool
	readWG sync.WaitGroup
}

func sarvamStreamEndpoint(cfg config.SarvamConfig) string {
	model := cfg.StreamModel
	if model == "" {
		model = sarvamDefaultStreamModel
	}
	language := cfg.LanguageCode
	if language == "" {
		language = "unknown" // auto-detect
	}
	q := url.Values{}
	q.Set("language-code", language)
	q.Set("model", model)
	q.Set("vad_signals", "true")
	return sarvamStreamURL + "?" + q.Encode()
}

func NewSarvamStreamClient(ctx context.Context, cfg config.SarvamConfig, onResult func(TranscriptResult)) (Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("sarvam stt: api_key is required")
	}
	sttCtx, cancel := context.WithCancel(ctx)
	header := http.Header{}
	header.Set("api-subscription-key", cfg.APIKey)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(sttCtx, sarvamStreamEndpoint(cfg), header)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sarvam stt: dial: %w", err)
	}
	c := &sarvamStreamClient{conn: conn, onResult: onResult, ctx: sttCtx, cancel: cancel}
	c.readWG.Add(1)
	go c.readLoop()
	log.Printf("[stt:sarvam] streaming (%s) -- finals only, barge-in runs VAD-only", sarvamStreamEndpoint(cfg)[len(sarvamStreamURL)+1:])
	return c, nil
}

type sarvamStreamAudio struct {
	Audio struct {
		Data       string `json:"data"`
		SampleRate string `json:"sample_rate"`
		Encoding   string `json:"encoding"`
	} `json:"audio"`
}

func (c *sarvamStreamClient) SendAudio(data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("sarvam stt: client closed")
	}
	if c.failed.Load() {
		return fmt.Errorf("sarvam stt: connection lost")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// The pipeline reuses data's buffer after this returns: copy, don't keep.
	c.pending = append(c.pending, data...)
	if len(c.pending) < sarvamStreamChunkBytes {
		return nil
	}
	var msg sarvamStreamAudio
	msg.Audio.Data = base64.StdEncoding.EncodeToString(c.pending)
	msg.Audio.SampleRate = "16000"
	msg.Audio.Encoding = "audio/wav"
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("sarvam stt: encode audio: %w", err)
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(sarvamStreamWriteWait)); err != nil {
		return fmt.Errorf("sarvam stt: set write deadline: %w", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		c.failed.Store(true)
		return fmt.Errorf("sarvam stt: write audio: %w", err)
	}
	c.pending = c.pending[:0]
	return nil
}

type sarvamStreamMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type sarvamStreamTranscript struct {
	Transcript          string   `json:"transcript"`
	LanguageCode        string   `json:"language_code"`
	LanguageProbability *float64 `json:"language_probability"`
}

func (c *sarvamStreamClient) readLoop() {
	defer c.readWG.Done()
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			if !c.closed.Load() {
				c.failed.Store(true)
				log.Printf("[stt:sarvam] connection lost: %v", err)
			}
			return
		}
		var msg sarvamStreamMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			log.Printf("[stt:sarvam] bad message: %v", err)
			continue
		}
		switch msg.Type {
		case "data":
			var t sarvamStreamTranscript
			if err := json.Unmarshal(msg.Data, &t); err != nil {
				log.Printf("[stt:sarvam] bad transcript: %v", err)
				continue
			}
			text := strings.TrimSpace(t.Transcript)
			if text == "" {
				continue
			}
			confidence := 0.0
			if t.LanguageProbability != nil {
				confidence = *t.LanguageProbability
			}
			log.Printf("[stt:sarvam] final (%s): %q", t.LanguageCode, text)
			c.onResult(TranscriptResult{Text: text, IsFinal: true, Confidence: confidence, Language: baseLanguage(t.LanguageCode)})
		case "events":
			// START_SPEECH / END_SPEECH: the pipeline runs its own VAD, so
			// these are informational.
		case "error":
			log.Printf("[stt:sarvam] error: %s", string(msg.Data))
		}
	}
}

// EmitsPartials is false: Sarvam's stream sends one transcript per
// utterance, never interim text.
func (c *sarvamStreamClient) EmitsPartials() bool { return false }

func (c *sarvamStreamClient) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	c.writeMu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(sarvamStreamWriteWait))
	_ = c.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"flush"}`))
	c.writeMu.Unlock()
	_ = c.conn.Close()
	c.cancel()
	c.readWG.Wait()
	log.Println("[stt:sarvam] closed")
}
