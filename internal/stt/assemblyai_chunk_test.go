package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Universal-Streaming closes the socket on any audio message outside
// 50-1000ms ("Input Duration Violation"). The pipeline sends 20ms frames, so
// SendAudio must batch them. Found in a live test: unbatched, the server
// dropped the session two seconds into the call and STT was dead from then on.
func TestAssemblyAISendAudioBatchesFramesIntoAcceptedChunkSizes(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	done := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		defer close(done)
		for {
			typ, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.BinaryMessage {
				mu.Lock()
				sizes = append(sizes, len(msg))
				mu.Unlock()
			}
		}
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &assemblyAIClient{conn: conn, ctx: ctx, cancel: cancel, onResult: func(TranscriptResult) {}}

	frame := make([]byte, assemblyAISampleRate*2*20/1000) // one 20ms pipeline frame
	for i := 0; i < 50; i++ {                             // 1s of audio
		if err := c.SendAudio(frame); err != nil {
			t.Fatalf("SendAudio: %v", err)
		}
	}
	conn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fake server never saw the connection close")
	}

	const bytesPerMs = assemblyAISampleRate * 2 / 1000
	mu.Lock()
	defer mu.Unlock()
	if len(sizes) == 0 {
		t.Fatal("no audio reached the server")
	}
	for i, n := range sizes {
		if ms := n / bytesPerMs; ms < 50 || ms > 1000 {
			t.Fatalf("message %d is %dms of audio; AssemblyAI only accepts 50-1000ms", i, ms)
		}
	}
}
