package tts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// fakeVoice is a scripted provider: it fails with err, or waits delay and
// then sends chunks (a chunk with Err set fails mid-stream).
type fakeVoice struct {
	err    error
	delay  time.Duration
	chunks []StreamChunk

	mu    sync.Mutex
	texts []string
	langs []string
}

func (v *fakeVoice) record(text, lang string) {
	v.mu.Lock()
	v.texts = append(v.texts, text)
	v.langs = append(v.langs, lang)
	v.mu.Unlock()
}

func (v *fakeVoice) calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.texts)
}

func (v *fakeVoice) Synthesize(ctx context.Context, text string) ([]byte, error) {
	v.record(text, "")
	if v.err != nil {
		return nil, v.err
	}
	return []byte("pcm"), nil
}

func (v *fakeVoice) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return v.SynthesizeStreamLanguage(ctx, text, "", VoiceControls{})
}

func (v *fakeVoice) SynthesizeStreamLanguage(ctx context.Context, text, lang string, vc VoiceControls) (<-chan StreamChunk, error) {
	v.record(text, lang)
	if v.err != nil {
		return nil, v.err
	}
	out := make(chan StreamChunk, len(v.chunks))
	go func() {
		defer close(out)
		select {
		case <-time.After(v.delay):
		case <-ctx.Done():
			return
		}
		for _, c := range v.chunks {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func audio(s string) []StreamChunk { return []StreamChunk{{PCM: []byte(s)}} }

func newTestFailover(t *testing.T, voices map[string]*fakeVoice, opt FailoverOptions) *failoverClient {
	t.Helper()
	clients := map[string]Client{}
	for n, v := range voices {
		clients[n] = v
	}
	if opt.FirstAudioTimeout == 0 {
		opt.FirstAudioTimeout = 200 * time.Millisecond
	}
	f, err := newFailoverClient(clients, opt)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func speak(t *testing.T, f *failoverClient, text string) (string, error) {
	t.Helper()
	ch, err := f.SynthesizeStream(context.Background(), text)
	if err != nil {
		return "", err
	}
	var got []byte
	for c := range ch {
		got = append(got, c.PCM...)
	}
	return string(got), nil
}

func TestFailoverUsesFirstProvider(t *testing.T) {
	a, b := &fakeVoice{chunks: audio("a")}, &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
	if got, err := speak(t, f, "hi"); err != nil || got != "a" {
		t.Fatalf("got %q, %v; want a", got, err)
	}
	if b.calls() != 0 {
		t.Fatal("second provider called although the first worked")
	}
}

func TestFailoverCooldownByError(t *testing.T) {
	cases := []struct {
		err  string
		want time.Duration
	}{
		{"cartesia: status 429 too many requests", 30 * time.Second},
		{"elevenlabs: 402 payment required", longCooldown},
		{"deepgram: 401 unauthorized", disabledCooldown},
		{"connection reset by peer", 30 * time.Second},
		{"cartesia ws []: Invalid language for model: The language is not supported by this model.", 0},
	}
	for _, c := range cases {
		now := time.Unix(1000, 0)
		a, b := &fakeVoice{err: errors.New(c.err)}, &fakeVoice{chunks: audio("b")}
		f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
		f.now = func() time.Time { return now }
		if got, err := speak(t, f, "hi"); err != nil || got != "b" {
			t.Fatalf("%s: got %q, %v; want b", c.err, got, err)
		}
		p := f.providers["a"]
		if c.want == 0 {
			// Only this sentence went elsewhere; a stays first.
			if !p.available(now) {
				t.Fatalf("%s: provider skipped for every sentence", c.err)
			}
			continue
		}
		if !p.available(now.Add(c.want)) || p.available(now.Add(c.want-time.Second)) {
			t.Fatalf("%s: want provider down for %s", c.err, c.want)
		}
		// While a is down, b goes first.
		speak(t, f, "again")
		if a.calls() != 1 {
			t.Fatalf("%s: a tried again during its cooldown", c.err)
		}
	}
}

func TestFailoverSlowFirstAudioGoesToNext(t *testing.T) {
	a := &fakeVoice{delay: time.Second, chunks: audio("a")}
	b := &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
	start := time.Now()
	if got, err := speak(t, f, "hi"); err != nil || got != "b" {
		t.Fatalf("got %q, %v; want b", got, err)
	}
	if d := time.Since(start); d > 800*time.Millisecond {
		t.Fatalf("took %s; should give up on a after the first-audio timeout", d)
	}
}

// slowStart blocks before returning a stream, like a provider waiting for a
// free slot at its concurrency limit.
type slowStart struct{ fakeVoice }

func (s *slowStart) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeVoice.SynthesizeStream(ctx, text)
}

func TestFailoverSlowStartGoesToNext(t *testing.T) {
	b := &fakeVoice{chunks: audio("b")}
	f, err := newFailoverClient(map[string]Client{"a": &slowStart{fakeVoice{chunks: audio("a")}}, "b": b},
		FailoverOptions{Order: []string{"a", "b"}, FirstAudioTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := speak(t, f, "hi"); err != nil || got != "b" {
		t.Fatalf("got %q, %v; want b", got, err)
	}
}

func TestFailoverMidStreamFailureEndsSentence(t *testing.T) {
	a := &fakeVoice{chunks: []StreamChunk{{PCM: []byte("a1")}, {Err: errors.New("socket closed")}}}
	b := &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
	if got, _ := speak(t, f, "one"); got != "a1" {
		t.Fatalf("got %q; the started sentence should keep a's audio, not restart", got)
	}
	if b.calls() != 0 {
		t.Fatal("sentence was repeated on b after a's audio started")
	}
	if got, _ := speak(t, f, "two"); got != "b" {
		t.Fatalf("next sentence: got %q, want b", got)
	}
}

func TestFailoverAllDown(t *testing.T) {
	a, b := &fakeVoice{err: errors.New("boom")}, &fakeVoice{err: errors.New("bang")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
	if _, err := speak(t, f, "hi"); err == nil {
		t.Fatal("want an error when every provider fails")
	}
	// Both are cooling down, but a sentence is still tried rather than dropped.
	speak(t, f, "again")
	if a.calls() != 2 || b.calls() != 2 {
		t.Fatalf("calls a=%d b=%d; down providers should still be tried last", a.calls(), b.calls())
	}
}

func TestFailoverCallerCutInIsNotAFailure(t *testing.T) {
	a := &fakeVoice{delay: time.Second, chunks: audio("a")}
	b := &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b},
		FailoverOptions{Order: []string{"a", "b"}, FirstAudioTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := f.SynthesizeStream(ctx, "hi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !f.providers["a"].available(time.Now()) {
		t.Fatal("a marked down because the caller cut in")
	}
	if b.calls() != 0 {
		t.Fatal("b tried after the caller cut in")
	}
}

func TestFailoverLanguageOrder(t *testing.T) {
	a, b := &fakeVoice{chunks: audio("a")}, &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{
		Order:      []string{"a", "b"},
		ByLanguage: map[string][]string{"hi": {"b", "a"}},
	})
	if got, _ := speak(t, f, "[lang:hi-IN] नमस्ते"); got != "b" {
		t.Fatalf("Hindi: got %q, want b", got)
	}
	if got, _ := speak(t, f, "[lang:ta] வணக்கம்"); got != "a" {
		t.Fatalf("Tamil: got %q, want the default order", got)
	}
}

func TestFailoverConfigErrors(t *testing.T) {
	if _, err := newFailoverClient(map[string]Client{}, FailoverOptions{}); err == nil {
		t.Fatal("want an error for an empty order")
	}
	_, err := newFailoverClient(map[string]Client{"a": &fakeVoice{}},
		FailoverOptions{Order: []string{"a"}, ByLanguage: map[string][]string{"hi": {"x"}}})
	if err == nil {
		t.Fatal("want an error for an unknown provider")
	}
}

func TestSentenceLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"[lang:hi] नमस्ते":              "hi",
		"[voice:male-2] [lang:ta-IN] x": "ta",
		"hello [lang:fr]":               "",
		"plain":                         "",
	} {
		if got := sentenceLanguage(in); got != want {
			t.Errorf("sentenceLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdaptTags(t *testing.T) {
	v := &fakeVoice{chunks: audio("x")}
	ch, err := adaptTags("cartesia", v).(ControllableStreamer).
		SynthesizeStreamWithControls(context.Background(), "[voice:female-1] [lang:hi] नमस्ते जी", VoiceControls{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if v.texts[0] != "नमस्ते जी" || v.langs[0] != "hi" {
		t.Fatalf("got text %q lang %q; want tags removed and hi passed", v.texts[0], v.langs[0])
	}
	r := &fakeVoice{}
	if adaptTags("vibevoice", r) != Client(r) {
		t.Fatal("the voice router reads tags itself and should get them unchanged")
	}
}

func chainOrder(t *testing.T, c Client) []string {
	t.Helper()
	s, ok := c.(*sanitizingClient)
	if !ok {
		t.Fatalf("got %T", c)
	}
	f, ok := s.inner.(*failoverClient)
	if !ok {
		t.Fatalf("got %T inside", s.inner)
	}
	return f.order
}

func voiceConfig(choice bool) *config.Config {
	cfg := &config.Config{}
	cfg.TTS.Provider = "failover"
	cfg.TTS.Failover = []string{"vibevoice", "deepgram"}
	cfg.TTS.Languages = map[string][]string{"hi": {"vibevoice", "deepgram"}}
	cfg.TTS.ClientChoice = choice
	cfg.VibeVoice.TTSURL = "http://127.0.0.1:1"
	cfg.Deepgram.APIKey = "test"
	return cfg
}

func TestNewClientForPutsTheChosenVoiceFirst(t *testing.T) {
	cfg := voiceConfig(true)
	c, err := NewClientFor(cfg, "deepgram")
	if err != nil {
		t.Fatal(err)
	}
	if got := chainOrder(t, c); !slices.Equal(got, []string{"deepgram", "vibevoice"}) {
		t.Fatalf("order = %v", got)
	}
	if !slices.Equal(cfg.TTS.Failover, []string{"vibevoice", "deepgram"}) {
		t.Fatal("the deployment's own order was changed")
	}
}

func TestNewClientForKeepsTheDeploymentOrder(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg   *config.Config
		voice string
	}{
		"choice off":    {voiceConfig(false), "deepgram"},
		"unknown voice": {voiceConfig(true), "elevenlabs"},
		"no voice":      {voiceConfig(true), ""},
	} {
		c, err := NewClientFor(tc.cfg, tc.voice)
		if err != nil {
			t.Fatal(err)
		}
		if got := chainOrder(t, c); !slices.Equal(got, []string{"vibevoice", "deepgram"}) {
			t.Errorf("%s: order = %v", name, got)
		}
	}
}

func TestVoices(t *testing.T) {
	if got := Voices(voiceConfig(true)); !slices.Equal(got, []string{"vibevoice", "deepgram"}) {
		t.Errorf("failover: %v", got)
	}
	single := &config.Config{}
	single.TTS.Provider = "cartesia"
	if got := Voices(single); !slices.Equal(got, []string{"cartesia"}) {
		t.Errorf("single: %v", got)
	}
}

// Cartesia refuses the WebSocket handshake with 402 when out of credit; the
// chain must see the status to skip it for longer than a blip.
func TestCartesiaRefusedHandshakeCarriesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("Model credits limit reached"))
	}))
	defer srv.Close()
	c := NewCartesiaWSClient("key", "", "ws"+strings.TrimPrefix(srv.URL, "http"))
	_, err := c.SynthesizeStream(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("err = %v, want the 402", err)
	}
	if d, _ := classify(err, defaultCooldown); d != longCooldown {
		t.Errorf("cooldown = %s, want %s (out of credit)", d, longCooldown)
	}
}

// Live: Cartesia out of credit, and an English-only Deepgram voice read the
// agent's Tamil, Hindi and Dutch. A voice that speaks the language goes first.
func TestFailoverPrefersAVoiceThatSpeaksTheLanguage(t *testing.T) {
	cart := &fakeVoice{err: errors.New("cartesia ws dial: 402 Payment Required")}
	dg, el := &fakeVoice{chunks: audio("dg")}, &fakeVoice{chunks: audio("el")}
	f := newTestFailover(t, map[string]*fakeVoice{"cartesia": cart, "deepgram": dg, "elevenlabs": el}, FailoverOptions{
		Order: []string{"cartesia", "deepgram", "elevenlabs"},
		Speaks: map[string]func(string) bool{
			"deepgram":   func(l string) bool { return l == "en" },
			"elevenlabs": func(l string) bool { return l == "en" || l == "ta" },
		},
	})
	if got, _ := speak(t, f, "[lang:ta] வணக்கம்"); got != "el" {
		t.Fatalf("Tamil spoken by %q, want elevenlabs", got)
	}
	if got, _ := speak(t, f, "[lang:en] Hello."); got != "dg" {
		t.Fatalf("English spoken by %q, want deepgram (next in order)", got)
	}
}

// No voice that speaks it is up: the one that doesn't still reads it, rather
// than silence.
func TestFailoverStillSpeaksWhenNoVoiceKnowsTheLanguage(t *testing.T) {
	dg := &fakeVoice{chunks: audio("dg")}
	f := newTestFailover(t, map[string]*fakeVoice{"deepgram": dg}, FailoverOptions{
		Order:  []string{"deepgram"},
		Speaks: map[string]func(string) bool{"deepgram": func(l string) bool { return l == "en" }},
	})
	if got, _ := speak(t, f, "[lang:hi] नमस्ते"); got != "dg" {
		t.Fatalf("got %q", got)
	}
}

func TestSpeaksFor(t *testing.T) {
	cfg := &config.Config{}
	if s := speaksFor(cfg, "deepgram"); !s("en") || s("ta") {
		t.Error("default Aura voice is English only")
	}
	cfg.Deepgram.TTSModel = "aura-2-celeste-es"
	if s := speaksFor(cfg, "deepgram"); !s("es") || s("en") {
		t.Error("a Spanish Aura voice speaks Spanish")
	}
	if s := speaksFor(cfg, "elevenlabs"); !s("ta") || !s("hi") || s("bn") {
		t.Error("ElevenLabs v2.5 speaks Tamil and Hindi, not Bengali")
	}
	if s := speaksFor(cfg, "cartesia"); !s("hi") || !s("od") || !s("ja") || s("uk") {
		t.Error("Sonic 3.6, the default, speaks Hindi, Odia and Japanese natively, and Ukrainian without a native voice")
	}
	cfg.Cartesia.Model = "sonic-3"
	if s := speaksFor(cfg, "cartesia"); !s("hi") || s("od") {
		t.Error("sonic-3 speaks Hindi, not Odia")
	}
	if speaksFor(cfg, "vibevoice") != nil {
		t.Error("the voice router picks its own voice per language")
	}
}

// The pipeline learns which voice spoke a sentence from its first chunk.
func TestFailoverNamesTheVoiceThatSpoke(t *testing.T) {
	a, b := &fakeVoice{err: errors.New("HTTP 402: out of credits")}, &fakeVoice{chunks: audio("b")}
	f := newTestFailover(t, map[string]*fakeVoice{"a": a, "b": b}, FailoverOptions{Order: []string{"a", "b"}})
	ch, err := f.SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first.Provider != "b" {
		t.Fatalf("first chunk names %q; want b", first.Provider)
	}
	for range ch {
	}
}
