package stt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeListener stands in for a listener chain: the test makes it "hear"
// things by calling emit.
type fakeListener struct {
	spec     listenerSpec
	partials bool
	onResult func(TranscriptResult)

	mu     sync.Mutex
	frames int
	closed bool
}

func (f *fakeListener) SendAudio([]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames++
	return nil
}

func (f *fakeListener) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *fakeListener) EmitsPartials() bool { return f.partials }

func (f *fakeListener) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeListener) emit(r TranscriptResult) { f.onResult(r) }

type listenerLog struct {
	mu        sync.Mutex
	listeners []*fakeListener
}

func (l *listenerLog) factory() listenerFactory {
	return func(_ context.Context, spec listenerSpec, onResult func(TranscriptResult)) (Client, error) {
		// Indian listeners start on Sarvam: finals only.
		f := &fakeListener{spec: spec, partials: !spec.indian, onResult: onResult}
		l.mu.Lock()
		l.listeners = append(l.listeners, f)
		l.mu.Unlock()
		return f, nil
	}
}

func (l *listenerLog) last() *fakeListener {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listeners[len(l.listeners)-1]
}

func (l *listenerLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.listeners)
}

// fakeIdentifier answers after delay, or fails; it counts its calls.
type fakeIdentifier struct {
	id    Identification
	err   error
	delay time.Duration
	block bool // never answers (until the hold times out)

	mu    sync.Mutex
	calls int
}

func (f *fakeIdentifier) fn() identifyFunc {
	return func(ctx context.Context, pcm []byte) (Identification, error) {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		if f.block {
			<-ctx.Done()
			return Identification{}, ctx.Err()
		}
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return Identification{}, ctx.Err()
		}
		return f.id, f.err
	}
}

func (f *fakeIdentifier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type adaptiveHarness struct {
	t       *testing.T
	a       *adaptiveClient
	log     *listenerLog
	results chan TranscriptResult
}

func newHarness(t *testing.T, start listenerSpec, indianPrior bool, ids ...namedIdentifier) *adaptiveHarness {
	t.Helper()
	h := &adaptiveHarness{t: t, log: &listenerLog{}, results: make(chan TranscriptResult, 16)}
	a, err := startAdaptive(context.Background(), h.log.factory(), ids, start, indianPrior, defaultHindiConfidence, defaultMinConfidence,
		func(r TranscriptResult) { h.results <- r })
	if err != nil {
		t.Fatal(err)
	}
	a.holdMax = 300 * time.Millisecond
	a.noFinalQuiet = 50 * time.Millisecond
	h.a = a
	t.Cleanup(a.Close)
	return h
}

// speak sends n frames of caller audio, so a turn has audio to identify.
func (h *adaptiveHarness) speak(n int) {
	for i := 0; i < n; i++ {
		if err := h.a.SendAudio(loudFrame()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *adaptiveHarness) next() TranscriptResult {
	h.t.Helper()
	select {
	case r := <-h.results:
		return r
	case <-time.After(2 * time.Second):
		h.t.Fatal("no transcript reached the pipeline")
		return TranscriptResult{}
	}
}

func (h *adaptiveHarness) nothing(within time.Duration) {
	h.t.Helper()
	select {
	case r := <-h.results:
		h.t.Fatalf("unexpected transcript %q", r.Text)
	case <-time.After(within):
	}
}

func deepgramFinal(text, lang string, conf float64) TranscriptResult {
	return TranscriptResult{Text: text, IsFinal: true, Confidence: conf, Language: lang, Provider: "deepgram"}
}

const (
	garbledTamil = "यह न कि मीन डुम पौन से यह वेंडाम. पेय मेंट लिंगई अनुपंगल"
	tamil        = "எனக்கு மீண்டும் போன் செய்ய வேண்டாம். பேமெண்ட் லிங்கை அனுப்புங்கள்."
	hindi        = "कल आप कितने बजे खुलते हैं, appointment लेना पड़ेगा क्या"
)

func TestAdaptiveStartsFromTheCallersHint(t *testing.T) {
	cases := map[string]listenerSpec{
		"":      {},
		"hi-en": {},
		"fr":    {},
		"ta":    {indian: true, language: "ta"},
		"ml-IN": {indian: true, language: "ml"},
		"zh":    {language: "zh"},
	}
	for hint, want := range cases {
		if got := startListener(Hint{Language: hint}); got != want {
			t.Errorf("hint %q: start on %s, want %s", hint, got, want)
		}
	}
}

func TestAdaptivePassesConfidentTurnsStraightThrough(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "es"}}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal("No me llames más, mándame el enlace de pago.", "es", 1.0))
	if got := h.next(); got.Text != "No me llames más, mándame el enlace de pago." {
		t.Fatalf("got %q", got.Text)
	}
	if groq.callCount() != 0 {
		t.Error("a confident Spanish turn must not be checked")
	}
	// Partials are never held.
	h.log.last().emit(TranscriptResult{Text: "hola", Provider: "deepgram"})
	if got := h.next(); got.IsFinal || got.Text != "hola" {
		t.Fatalf("partial should pass through, got %+v", got)
	}
}

func TestAdaptiveSwitchesToTheIndianListenerWithSarvamsTranscript(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "ta", Text: "whisper's tamil"}}
	sarvam := &fakeIdentifier{id: Identification{Language: "ta", Text: tamil}, delay: 50 * time.Millisecond}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	world := h.log.last()
	h.speak(60)
	world.emit(deepgramFinal(garbledTamil, "hi", 0.76))

	got := h.next()
	if got.Text != tamil || got.Language != "ta" {
		t.Fatalf("the turn should be Sarvam's Tamil transcript, got %+v", got)
	}
	if sarvam.callCount() != 1 {
		t.Error("sarvam should run alongside groq on a Hindi-labelled turn")
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
	indian := h.log.last()
	if indian.spec != (listenerSpec{indian: true, language: "ta"}) {
		t.Fatalf("switched to %s", indian.spec)
	}
	waitFor(t, world.isClosed)

	// The old listener is no longer heard; the new one is, unchecked.
	world.emit(deepgramFinal("stale", "hi", 0.5))
	indian.emit(TranscriptResult{Text: tamil, IsFinal: true, Language: "ta", Provider: "sarvam", Confidence: 0.75})
	if got := h.next(); got.Text != tamil {
		t.Fatalf("got %q", got.Text)
	}
	h.nothing(50 * time.Millisecond)
	if h.a.EmitsPartials() {
		t.Error("on Sarvam the pipeline must be told there are no partials")
	}
}

func TestAdaptiveSwitchesToDeepgramFixedForKorean(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "ko", Text: "다시 전화하지 마세요."}}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal("タシィチョノアジマセヨー。キョルチェリンクルル", "ja", 0.62))
	if got := h.next(); got.Text != "다시 전화하지 마세요." {
		t.Fatalf("got %q", got.Text)
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
	if spec := h.log.last().spec; spec != (listenerSpec{language: "ko"}) {
		t.Fatalf("switched to %s", spec)
	}
	if !h.a.EmitsPartials() {
		t.Error("Deepgram fixed to Korean still streams partials")
	}
}

func TestAdaptiveReleasesTheTurnWhenTheListenerHearsTheLanguage(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "hi", Text: "whisper"}}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()})
	for i := 0; i < 3; i++ {
		h.speak(60)
		h.log.last().emit(deepgramFinal(hindi, "hi", 0.99))
		if got := h.next(); got.Text != hindi {
			t.Fatalf("turn %d: got %q", i, got.Text)
		}
		time.Sleep(50 * time.Millisecond) // let the background verdict land
	}
	// An Indian caller's confident Hindi is checked (in the background)
	// until two checks agree, then not.
	if groq.callCount() != 2 {
		t.Errorf("checks = %d, want 2 (then settled)", groq.callCount())
	}
	if h.log.count() != 1 {
		t.Error("the listener must not switch for Hindi")
	}
}

func TestAdaptiveDoesNotHoldConfidentHindiForTheCheck(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "hi"}, delay: 250 * time.Millisecond}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()})
	h.speak(60)
	start := time.Now()
	h.log.last().emit(deepgramFinal(hindi, "hi", 1.0))
	h.next()
	if held := time.Since(start); held > 100*time.Millisecond {
		t.Fatalf("held %s: a confident Hindi turn must go to the agent at once", held)
	}
	waitFor(t, func() bool { return groq.callCount() == 1 })
}

func TestAdaptiveBackgroundCheckMovesTheListenerForTheNextTurns(t *testing.T) {
	// Live: Punjabi came back from multi as Hindi at 1.00, and Whisper
	// called it Hindi too; only Sarvam knew.
	groq := &fakeIdentifier{id: Identification{Language: "hi", Text: "whisper"}}
	sarvam := &fakeIdentifier{id: Identification{Language: "pa", Text: "ਕਿਰਪਾ ਕਰਕੇ"}, delay: 80 * time.Millisecond}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal("कृपा करके मैंने दुबारा phone न करो", "hi", 1.0))
	if got := h.next(); got.Text != "कृपा करके मैंने दुबारा phone न करो" {
		t.Fatalf("the turn goes on as heard, got %q", got.Text)
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
	if spec := h.log.last().spec; spec != (listenerSpec{indian: true, language: "pa"}) {
		t.Fatalf("switched to %s", spec)
	}
	h.nothing(100 * time.Millisecond) // the released turn isn't sent again
}

func TestAdaptiveKeepsTheTranscriptWhenIdentificationFails(t *testing.T) {
	groq := &fakeIdentifier{err: errors.New("429 rate limited")}
	sarvam := &fakeIdentifier{err: errors.New("timeout")}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal(garbledTamil, "hi", 0.76))
	if got := h.next(); got.Text != garbledTamil {
		t.Fatalf("got %q", got.Text)
	}
	if h.log.count() != 1 {
		t.Error("no switch without an identification")
	}
}

func TestAdaptiveFallsBackToSarvamWhenGroqFails(t *testing.T) {
	groq := &fakeIdentifier{err: errors.New("401")}
	sarvam := &fakeIdentifier{id: Identification{Language: "te", Text: "నాకు మళ్ళీ ఫోన్ చేయకండి"}}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal("न कुमल्ली phone चैय कंडी", "hi", 0.86))
	if got := h.next(); got.Language != "te" {
		t.Fatalf("got %+v", got)
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
}

func TestAdaptiveNeverTrustsSarvamsEnglish(t *testing.T) {
	// Sarvam labels every world language en-IN (French came back as English).
	groq := &fakeIdentifier{err: errors.New("down")}
	sarvam := &fakeIdentifier{id: Identification{Language: "en", Text: "Don't call me anymore"}}
	h := newHarness(t, listenerSpec{language: "zh"}, false, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	h.speak(60)
	h.log.last().emit(deepgramFinal("请不要", "", 0.5))
	if got := h.next(); got.Text != "请不要" {
		t.Fatalf("got %q", got.Text)
	}
	if h.log.count() != 1 {
		t.Error("sarvam's 'en' must not move the call")
	}
}

func TestAdaptiveHoldIsBounded(t *testing.T) {
	groq := &fakeIdentifier{block: true}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	h.speak(60)
	start := time.Now()
	h.log.last().emit(deepgramFinal(garbledTamil, "hi", 0.7))
	if got := h.next(); got.Text != garbledTamil {
		t.Fatalf("got %q", got.Text)
	}
	if held := time.Since(start); held > time.Second {
		t.Fatalf("held %s, longer than the hold limit", held)
	}
}

func TestAdaptiveSkipsShortTurns(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "ta"}}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()})
	h.speak(20) // 0.4s of voice
	h.log.last().emit(deepgramFinal("हाँ जी", "hi", 0.7))
	h.next()
	if groq.callCount() != 0 {
		t.Error("'हाँ जी' is too short to identify")
	}
	// A Chinese sentence is one "word" of text but seconds of voice.
	h.speak(100)
	h.log.last().emit(deepgramFinal("真是呀为，", "ja", 0.68))
	h.next()
	if groq.callCount() != 1 {
		t.Error("two seconds of speech must be checked however short its text")
	}
}

func TestAdaptiveChecksSpeechThatProducedNoTranscript(t *testing.T) {
	// Chinese on Deepgram multi comes back empty.
	groq := &fakeIdentifier{id: Identification{Language: "zh", Text: "请不要再给我打电话了"}}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	h.speak(noFinalSpeechFrames + 5)
	time.Sleep(80 * time.Millisecond)
	_ = h.a.SendAudio(quietFrame())
	if got := h.next(); got.Text != "请不要再给我打电话了" || !got.IsFinal {
		t.Fatalf("got %+v", got)
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
	if spec := h.log.last().spec; spec != (listenerSpec{language: "zh"}) {
		t.Fatalf("switched to %s", spec)
	}
}

func TestAdaptiveStopsCheckingAfterTheCap(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "pl"}} // no listener known for it
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	for i := 0; i < adaptiveMaxChecks+2; i++ {
		h.speak(60)
		h.log.last().emit(deepgramFinal("Prosze do mnie nie dzwonic wiecej", "en", 0.6))
		h.next()
	}
	if groq.callCount() != adaptiveMaxChecks {
		t.Errorf("checks = %d, want the cap %d", groq.callCount(), adaptiveMaxChecks)
	}
}

func TestAdaptiveDoesNotCheckSarvamTurns(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "ta"}}
	h := newHarness(t, listenerSpec{indian: true, language: "ta"}, true, namedIdentifier{"groq", groq.fn()})
	h.speak(60)
	h.log.last().emit(TranscriptResult{Text: tamil, IsFinal: true, Confidence: 0.6, Language: "ta", Provider: "sarvam"})
	h.next()
	if groq.callCount() != 0 {
		t.Error("Sarvam's confidence is a language probability, not grounds for a check")
	}
}

func TestAdaptiveFallsBackToWorldWhenTheHintedListenerFails(t *testing.T) {
	calls := 0
	build := func(_ context.Context, spec listenerSpec, onResult func(TranscriptResult)) (Client, error) {
		calls++
		if spec.indian {
			return nil, errors.New("sarvam down")
		}
		return &fakeListener{spec: spec, partials: true, onResult: onResult}, nil
	}
	a, err := startAdaptive(context.Background(), build, nil, listenerSpec{indian: true, language: "ta"}, true, 0.97, 0.85, func(TranscriptResult) {})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.currentSpec() != (listenerSpec{}) || calls != 2 {
		t.Fatalf("on %s after %d builds", a.currentSpec(), calls)
	}
}

func TestFailoverPartialsFollowTheActiveProvider(t *testing.T) {
	providers := map[string]*fakeProvider{"deepgram": {name: "deepgram", failOn: 2}, "sarvam": {name: "sarvam"}}
	c, err := newFailoverClient(context.Background(), []string{"deepgram", "sarvam"}, factory(providers, nil), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pe := c.(PartialsEmitter)
	if !pe.EmitsPartials() {
		t.Fatal("on deepgram: partials")
	}
	_ = c.SendAudio(quietFrame())
	_ = c.SendAudio(quietFrame()) // deepgram fails; sarvam takes over
	if pe.EmitsPartials() {
		t.Fatal("on sarvam: no partials")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAdaptiveSarvamOverrulesWhisperOnIndianLanguages(t *testing.T) {
	cases := []struct {
		name    string
		heard   TranscriptResult
		whisper string
		sarvam  string
		want    listenerSpec
	}{
		// Live: a short Bengali turn, heard by multi as Spanish, Whisper called Vietnamese.
		{"bengali", deepgramFinal("Hallo. ¿Qué bolchen?", "es", 0.66), "vi", "bn", listenerSpec{indian: true, language: "bn"}},
		// Live: Gujarati that Whisper called Bengali.
		{"gujarati", deepgramFinal("Hello, call boliche?", "en", 0.50), "bn", "gu", listenerSpec{indian: true, language: "gu"}},
		// Live: Punjabi that multi heard as Hindi at 0.90 and Whisper called Hindi.
		{"punjabi", deepgramFinal("कृपा करके मैंने दुबारा phone न करो", "hi", 0.90), "hi", "pa", listenerSpec{indian: true, language: "pa"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			groq := &fakeIdentifier{id: Identification{Language: c.whisper, Text: "whisper"}}
			sarvam := &fakeIdentifier{id: Identification{Language: c.sarvam, Text: "sarvam's " + c.sarvam}, delay: 80 * time.Millisecond}
			h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
			h.speak(60)
			h.log.last().emit(c.heard)
			if got := h.next(); got.Text != "sarvam's "+c.sarvam || got.Language != c.sarvam {
				t.Fatalf("got %+v", got)
			}
			waitFor(t, func() bool { return h.log.count() == 2 })
			if spec := h.log.last().spec; spec != c.want {
				t.Fatalf("switched to %s", spec)
			}
		})
	}
}

func TestAdaptiveDoesNotWaitForSarvamWhenHindiIsCertain(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "hi"}}
	sarvam := &fakeIdentifier{id: Identification{Language: "hi"}, delay: 250 * time.Millisecond}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	h.speak(60)
	start := time.Now()
	h.log.last().emit(deepgramFinal(hindi, "hi", 1.0))
	h.next()
	if held := time.Since(start); held > 150*time.Millisecond {
		t.Fatalf("held %s: a certain Hindi turn must not wait for Sarvam", held)
	}
}

func TestAdaptiveReplaysWhatTheCallerSaidDuringTheCheck(t *testing.T) {
	groq := &fakeIdentifier{id: Identification{Language: "kn", Text: "whisper"}, delay: 100 * time.Millisecond}
	sarvam := &fakeIdentifier{id: Identification{Language: "kn", Text: "ದಯವಿಟ್ಟು ನನಗೆ ಮತ್ತೆ ಕರೆ ಮಾಡಬೇಡಿ"}, delay: 100 * time.Millisecond}
	h := newHarness(t, listenerSpec{}, true, namedIdentifier{"groq", groq.fn()}, namedIdentifier{"sarvam", sarvam.fn()})
	world := h.log.last()
	h.speak(60)
	world.emit(deepgramFinal("दाइए भी two नैने गई मत", "hi", 0.91))
	// The caller keeps talking while the turn is checked, and the old
	// listener reports it -- as garbage.
	h.speak(30)
	world.emit(deepgramFinal("नानु सोमवार पावती", "hi", 0.8))
	if got := h.next(); got.Language != "kn" {
		t.Fatalf("got %+v", got)
	}
	waitFor(t, func() bool { return h.log.count() == 2 })
	indian := h.log.last()
	// The new listener exists before the replay reaches it: wait for the
	// frames rather than reading the count once (flaky under -race).
	frames := func() int { indian.mu.Lock(); defer indian.mu.Unlock(); return indian.frames }
	deadline := time.Now().Add(2 * time.Second)
	for frames() < 30 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if replayed := frames(); replayed < 30 {
		t.Fatalf("new listener got %d frames; the 30 spoken during the check must be replayed", replayed)
	}
	h.nothing(100 * time.Millisecond) // the old listener's garbage is dropped
}

func TestAdaptiveLabelsTurnsFromAFixedLanguageListener(t *testing.T) {
	// Deepgram with the language fixed doesn't label its transcripts.
	h := newHarness(t, listenerSpec{language: "ko"}, false)
	h.speak(60)
	h.log.last().emit(TranscriptResult{Text: "다시 전화하지 마세요", IsFinal: true, Confidence: 0.99, Provider: "deepgram"})
	if got := h.next(); got.Language != "ko" {
		t.Fatalf("language = %q, want ko", got.Language)
	}
}

func TestAdaptiveDoesNotCheckSpeechTheListenerIsHearing(t *testing.T) {
	// A quiet caller: the loudness says they stopped, but partial words keep
	// coming. That's a listener that hears them, not one that missed them.
	groq := &fakeIdentifier{id: Identification{Language: "hi"}}
	h := newHarness(t, listenerSpec{}, false, namedIdentifier{"groq", groq.fn()})
	h.speak(noFinalSpeechFrames + 5)
	h.log.last().emit(TranscriptResult{Text: "कल आप कितने बजे", Provider: "deepgram"})
	h.next() // the partial
	time.Sleep(80 * time.Millisecond)
	_ = h.a.SendAudio(quietFrame())
	h.nothing(100 * time.Millisecond)
	if groq.callCount() != 0 {
		t.Fatal("no-transcript check fired although the listener was sending words")
	}
}
