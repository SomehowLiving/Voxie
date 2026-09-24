package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

// sarvamTestFrame returns one 20ms linear16 frame: a 300Hz tone at the given
// amplitude, or silence at amplitude 0.
func sarvamTestFrame(amplitude float64) []byte {
	buf := &bytes.Buffer{}
	for i := 0; i < audio.FrameSize; i++ {
		v := int16(amplitude * math.Sin(2*math.Pi*300*float64(i)/float64(audio.SampleRate)))
		binary.Write(buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

type sarvamCapture struct {
	mu        sync.Mutex
	apiKey    string
	model     string
	language  string
	fileBytes []byte
	calls     int
}

func newFakeSarvam(t *testing.T, transcript string) (*sarvamCapture, func()) {
	t.Helper()
	capture := &sarvamCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("fake sarvam: not a multipart request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("fake sarvam: no file part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(f)

		capture.mu.Lock()
		capture.calls++
		capture.apiKey = r.Header.Get("api-subscription-key")
		capture.model = r.FormValue("model")
		capture.language = r.FormValue("language_code")
		capture.fileBytes = data
		capture.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"request_id":"r1","transcript":"` + transcript + `"}`))
	}))

	prev := sarvamAPIURL
	sarvamAPIURL = srv.URL
	return capture, func() {
		sarvamAPIURL = prev
		srv.Close()
	}
}

// One spoken utterance between silences must produce exactly one upload of a
// real WAV file carrying the configured key/model/language, and exactly one
// final transcript back through onResult.
func TestSarvamUploadsOneUtteranceAndReportsItsFinal(t *testing.T) {
	capture, cleanup := newFakeSarvam(t, "mera payment fail ho gaya")
	defer cleanup()

	results := make(chan TranscriptResult, 4)
	client, err := NewSarvamClient(context.Background(), config.SarvamConfig{
		APIKey: "sk-test", Model: "saaras:v3", LanguageCode: "hi-IN",
	}, func(r TranscriptResult) { results <- r })
	if err != nil {
		t.Fatalf("NewSarvamClient: %v", err)
	}
	defer client.Close()

	silence, speech := sarvamTestFrame(0), sarvamTestFrame(8000)
	for i := 0; i < 10; i++ { // 200ms of leading silence
		client.SendAudio(silence)
	}
	for i := 0; i < 50; i++ { // 1s of speech
		client.SendAudio(speech)
	}
	for i := 0; i < 40; i++ { // 800ms of silence -- past the 600ms end-of-utterance window
		client.SendAudio(silence)
	}

	select {
	case r := <-results:
		if !r.IsFinal || r.Text != "mera payment fail ho gaya" {
			t.Fatalf("result = %+v, want the fake server's transcript as a final", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no transcript reported -- the utterance was never uploaded")
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.calls != 1 {
		t.Fatalf("uploads = %d, want exactly 1 for one utterance", capture.calls)
	}
	if capture.apiKey != "sk-test" || capture.model != "saaras:v3" || capture.language != "hi-IN" {
		t.Fatalf("request carried key=%q model=%q language=%q", capture.apiKey, capture.model, capture.language)
	}
	if len(capture.fileBytes) < 44 || string(capture.fileBytes[:4]) != "RIFF" || string(capture.fileBytes[8:12]) != "WAVE" {
		t.Fatalf("uploaded file is not a WAV (first bytes %q)", capture.fileBytes[:min(12, len(capture.fileBytes))])
	}
	if rate := binary.LittleEndian.Uint32(capture.fileBytes[24:28]); rate != uint32(audio.SampleRate) {
		t.Fatalf("WAV sample rate = %d, want %d", rate, audio.SampleRate)
	}
}

// Silence alone must never cost an API call.
func TestSarvamDoesNotUploadSilence(t *testing.T) {
	capture, cleanup := newFakeSarvam(t, "should not appear")
	defer cleanup()

	client, err := NewSarvamClient(context.Background(), config.SarvamConfig{APIKey: "sk-test"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("NewSarvamClient: %v", err)
	}
	for i := 0; i < 200; i++ {
		client.SendAudio(sarvamTestFrame(0))
	}
	client.Close() // waits for any in-flight upload

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.calls != 0 {
		t.Fatalf("uploads = %d for pure silence, want 0", capture.calls)
	}
}

func TestSarvamRequiresAnAPIKeyAndEmitsNoPartials(t *testing.T) {
	if _, err := NewSarvamClient(context.Background(), config.SarvamConfig{}, func(TranscriptResult) {}); err == nil {
		t.Fatal("NewSarvamClient accepted an empty api_key")
	}
	client, err := NewSarvamClient(context.Background(), config.SarvamConfig{APIKey: "k"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("NewSarvamClient: %v", err)
	}
	defer client.Close()
	if pe, ok := client.(PartialsEmitter); !ok || pe.EmitsPartials() {
		t.Fatal("sarvam client must implement PartialsEmitter and report false -- it has no streaming endpoint")
	}
}
