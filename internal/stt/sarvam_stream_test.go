package stt

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// fakeSarvamStream stands in for Sarvam's streaming endpoint: it records
// the query and every audio message, and replies to the first one with a
// Tamil transcript, as the real service does after END_SPEECH.
type fakeSarvamStream struct {
	server  *httptest.Server
	mu      sync.Mutex
	query   string
	key     string
	chunks  [][]byte
	conn    *websocket.Conn
	replied bool
}

func newFakeSarvamStream(t *testing.T) *fakeSarvamStream {
	t.Helper()
	f := &fakeSarvamStream{}
	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.query, f.key, f.conn = r.URL.RawQuery, r.Header.Get("api-subscription-key"), conn
		f.mu.Unlock()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m sarvamStreamAudio
			if json.Unmarshal(msg, &m) != nil || m.Audio.Data == "" {
				continue
			}
			pcm, _ := base64.StdEncoding.DecodeString(m.Audio.Data)
			f.mu.Lock()
			f.chunks = append(f.chunks, pcm)
			first := !f.replied
			f.replied = true
			f.mu.Unlock()
			if first {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"events","data":{"signal_type":"END_SPEECH"}}`))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"data","data":{"transcript":"எனக்கு மீண்டும் போன் செய்ய வேண்டாம்.","language_code":"ta-IN","language_probability":0.93}}`))
			}
		}
	}))
	t.Cleanup(f.server.Close)
	old := sarvamStreamURL
	sarvamStreamURL = "ws" + strings.TrimPrefix(f.server.URL, "http")
	t.Cleanup(func() { sarvamStreamURL = old })
	return f
}

func TestSarvamStreamSendsBatchedAudioAndEmitsFinals(t *testing.T) {
	f := newFakeSarvamStream(t)
	results := make(chan TranscriptResult, 4)
	c, err := NewSarvamStreamClient(t.Context(), config.SarvamConfig{APIKey: "k", LanguageCode: "unknown"}, func(r TranscriptResult) { results <- r })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	frame := make([]byte, 640) // 20ms at 16kHz
	for i := 0; i < 10; i++ {  // 200ms
		if err := c.SendAudio(frame); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case r := <-results:
		if !r.IsFinal || !strings.Contains(r.Text, "போன்") {
			t.Fatalf("got %+v, want the Tamil transcript as a final", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no transcript")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key != "k" || !strings.Contains(f.query, "language-code=unknown") || !strings.Contains(f.query, "model=saarika") {
		t.Errorf("connected with key=%q query=%q", f.key, f.query)
	}
	for _, chunk := range f.chunks {
		if len(chunk) < sarvamStreamChunkBytes {
			t.Errorf("sent a %d-byte audio message, want at least %d (100ms)", len(chunk), sarvamStreamChunkBytes)
		}
	}
	if emitsPartials := c.(PartialsEmitter).EmitsPartials(); emitsPartials {
		t.Error("Sarvam streams finals only")
	}
}

func TestSarvamStreamReportsADroppedConnection(t *testing.T) {
	f := newFakeSarvamStream(t)
	c, err := NewSarvamStreamClient(t.Context(), config.SarvamConfig{APIKey: "k"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	frame := make([]byte, 3200)
	if err := c.SendAudio(frame); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	_ = f.conn.Close()
	f.mu.Unlock()
	// The drop surfaces on a following SendAudio -- which is what a
	// failover client needs to move to another provider.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.SendAudio(frame); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("SendAudio kept succeeding after the connection dropped")
}
