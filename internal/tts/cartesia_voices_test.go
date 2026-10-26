package tts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// Live, Katie (American English) read Hindi and Tamil like a foreigner: each
// Indian language gets a voice native in it, and its India locale.
func TestCartesiaVoiceAndLocalePerLanguage(t *testing.T) {
	o := CartesiaOptions{}
	cases := []struct{ lang, voice, locale string }{
		{"hi", cartesiaNativeVoices["hi"], "hi-IN"},
		{"ta", cartesiaNativeVoices["ta"], "ta-IN"},
		{"od", cartesiaNativeVoices["od"], "or-IN"}, // Voxie's od is Cartesia's or
		{"es", defaultCartesiaVoiceID, "es"},
		{"en", defaultCartesiaVoiceID, "en"},
		{"", defaultCartesiaVoiceID, ""}, // untagged: Cartesia detects the language
	}
	for _, c := range cases {
		if v := o.voiceFor(c.lang); v != c.voice {
			t.Errorf("voice for %q = %s, want %s", c.lang, v, c.voice)
		}
		if l := o.localeFor(c.lang); l != c.locale {
			t.Errorf("locale for %q = %q, want %q", c.lang, l, c.locale)
		}
	}
	if o.model() != "sonic-3.6" {
		t.Errorf("default model %s", o.model())
	}
	for _, v := range cartesiaNativeVoices {
		if len(v) != 36 {
			t.Errorf("voice id %q isn't a UUID", v)
		}
	}
}

func TestCartesiaConfigOverrides(t *testing.T) {
	o := CartesiaOptions{
		Model: "sonic-3.6-2026-08-27", VoiceID: "own-voice",
		Voices: map[string]string{"hi": "my-hindi"}, Locales: map[string]string{"en": "en-IN"},
	}
	if o.voiceFor("hi") != "my-hindi" || o.voiceFor("ta") != cartesiaNativeVoices["ta"] || o.voiceFor("fr") != "own-voice" {
		t.Error("configured voices come first, then native ones, then voice_id")
	}
	if o.localeFor("en") != "en-IN" || o.localeFor("hi") != "hi-IN" {
		t.Error("a configured locale overrides the default")
	}
	if o.model() != "sonic-3.6-2026-08-27" {
		t.Error("a configured model is used")
	}
}

func TestCartesiaSpeaksOdiaFromSonic36(t *testing.T) {
	for model, odia := range map[string]bool{"": true, "sonic-3.6": true, "sonic-3.6-2026-08-27": true, "sonic-latest": true,
		"sonic-3": false, "sonic-3-2025-10-27": false, "sonic-3.5": false, "sonic-2": false, "sonic-turbo": false} {
		if got := cartesiaSpeaks(model, "od"); got != odia {
			t.Errorf("%q speaks Odia: %v, want %v", model, got, odia)
		}
		if !cartesiaSpeaks(model, "hi") {
			t.Errorf("%q speaks Hindi", model)
		}
	}
}

// What actually goes to Cartesia for a Tamil sentence.
func TestCartesiaWSSendsModelVoiceAndLocale(t *testing.T) {
	got := make(chan wsGenerationRequest, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var req wsGenerationRequest
		if conn.ReadJSON(&req) == nil {
			got <- req
			conn.WriteJSON(map[string]any{"type": "done", "done": true, "context_id": req.ContextID})
		}
	}))
	defer srv.Close()
	c := NewCartesiaWSClientWith("key", "ws"+strings.TrimPrefix(srv.URL, "http"), CartesiaOptions{})
	ls, ok := c.(interface {
		SynthesizeStreamLanguage(context.Context, string, string, VoiceControls) (<-chan StreamChunk, error)
	})
	if !ok {
		t.Fatal("cartesia client takes a language")
	}
	ch, err := ls.SynthesizeStreamLanguage(context.Background(), "வணக்கம்", "ta", VoiceControls{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	req := <-got
	if req.ModelID != "sonic-3.6" || req.Voice.ID != cartesiaNativeVoices["ta"] || req.Locale != "ta-IN" {
		t.Fatalf("sent model %q voice %q locale %q", req.ModelID, req.Voice.ID, req.Locale)
	}
}
