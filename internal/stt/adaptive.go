package stt

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// The adaptive STT client ([stt] provider = "adaptive") picks the listener
// for each call and switches it mid-call when the caller's language needs
// another -- instead of one provider order for a whole deployment.
//
// Why, measured on 25 languages at phone quality:
//   - Deepgram's multilingual model hears English, Hindi (and Hinglish),
//     Spanish, French, German, Italian, Portuguese, Dutch, Russian and
//     Japanese at 0.99-1.00, and streams partials. It is the default.
//   - It does not hear regional Indian languages (Tamil, Telugu, Bengali...
//     come back as Hindi-looking text labelled "hi" at 0.70-0.95), nor
//     Korean, Arabic, Turkish or Chinese (soup at 0.5-0.7, or nothing).
//   - With the language fixed, Deepgram hears all of those except Malayalam
//     at 0.97-1.00; Sarvam hears every Indian language. The gap is knowing
//     the language, not hearing it.
//
// So every final from the listener is looked at. One it may have misheard
// -- Deepgram's "hi" below hindi_confidence, anything below min_confidence,
// or speech that produced no transcript at all -- is held while the turn's
// audio is identified (Groq Whisper, then Sarvam). If the language is one
// the listener hears, the final is released unchanged; if not, the turn is
// replaced by the identifier's transcript (Sarvam's for Indian languages)
// and the call moves to the listener that hears it. A failed or slow
// identification releases the final unchanged: never worse than before.
//
// Partials pass straight through, so barge-in never waits on a check.

const (
	adaptiveBufferMax      = 20 * time.Second       // caller audio kept for identification
	adaptivePreRoll        = 300 * time.Millisecond // audio kept from before a turn started
	adaptiveHoldMax        = 3 * time.Second        // longest a turn is held for a check
	adaptiveMaxChecks      = 6                      // per call
	adaptiveMaxNoFinal     = 3                      // checks on speech with no transcript, per call
	adaptiveSettleAfter    = 2                      // agreeing checks before the language is settled
	noFinalSpeechFrames    = 75                     // ~1.5s of speech...
	shortTurnFrames        = 40                     // under ~0.8s of voice: too little to identify
	voicedPeak             = 1000                   // a frame this loud has the caller's voice in it
	noFinalQuiet           = 2 * time.Second        // ...then this long with no transcript
	defaultHindiConfidence = 0.97
	defaultMinConfidence   = 0.85
)

var (
	// Deepgram multi hears these (verified).
	multiLanguages = setOf("en", "es", "fr", "de", "it", "pt", "nl", "ru", "ja", "hi")
	// Regional Indian languages: the Indian listener.
	indianRegional = setOf("bn", "ta", "te", "kn", "ml", "mr", "gu", "pa", "od")
	// World languages Deepgram hears only with the language fixed (verified).
	fixedWorldLanguages = setOf("zh", "ko", "ar", "tr")
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// listenerSpec names a listener: the world or Indian chain, and a language
// fixed on its Deepgram ("" = multi).
type listenerSpec struct {
	indian   bool
	language string
}

func (l listenerSpec) String() string {
	name, lang := "world", l.language
	if l.indian {
		name = "indian"
	}
	if lang == "" {
		lang = "multi"
	}
	return fmt.Sprintf("%s (%s)", name, lang)
}

// covers reports whether this listener hears lang.
func (l listenerSpec) covers(lang string) bool {
	switch {
	case l.indian:
		return indianRegional[lang] || lang == "hi" || lang == "en"
	case l.language != "":
		return lang == l.language
	default:
		return multiLanguages[lang]
	}
}

// listenerFor is the listener that hears lang best; false when none is
// known to.
func listenerFor(lang string) (listenerSpec, bool) {
	switch {
	case indianRegional[lang]:
		return listenerSpec{indian: true, language: lang}, true
	case multiLanguages[lang]:
		return listenerSpec{}, true
	case fixedWorldLanguages[lang]:
		return listenerSpec{language: lang}, true
	}
	return listenerSpec{}, false
}

// startListener chooses the first listener from what's known of the
// caller. It's only a prior: a wrong one is corrected by the first check.
func startListener(hint Hint) listenerSpec {
	lang := baseLanguage(hint.Language)
	switch {
	case indianRegional[lang]:
		return listenerSpec{indian: true, language: lang}
	case fixedWorldLanguages[lang]:
		return listenerSpec{language: lang}
	}
	return listenerSpec{}
}

type listenerFactory func(ctx context.Context, spec listenerSpec, onResult func(TranscriptResult)) (Client, error)

type namedIdentifier struct {
	name     string
	identify identifyFunc
}

type timedFrame struct {
	at  time.Time
	pcm []byte
}

type adaptiveItem struct {
	final  *TranscriptResult // nil: the caller spoke and nothing was transcribed
	pcm    []byte
	spec   listenerSpec
	voiced int       // frames with the caller's voice in this turn
	from   int       // the listener that produced it
	at     time.Time // when it arrived
	// verdict carries a background check's answer back to the worker; the
	// turn itself was released when the check started.
	verdict *Identification
}

// How a final is handled.
type checkKind int

const (
	checkNone       checkKind = iota // pass it on
	checkHold                        // hold it while its language is identified
	checkBackground                  // pass it on now; identify its language anyway
)

type adaptiveClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	build    listenerFactory
	ids      []namedIdentifier
	onResult func(TranscriptResult)

	hindiConfidence float64
	minConfidence   float64
	indianPrior     bool // the caller is probably in India: check Hindi turns until settled
	holdMax         time.Duration
	noFinalQuiet    time.Duration

	mu               sync.Mutex
	current          Client
	spec             listenerSpec
	nextID           int
	acceptID         int // results from other listeners are dropped
	frames           []timedFrame
	turnStart        time.Time // audio from here belongs to the next turn
	loudSinceFinal   int
	voicedSinceFinal int
	lastLoudAt       time.Time
	noFinalQueued    bool
	// heardWords: the listener has sent partial words since the last final,
	// so it is hearing the caller -- whatever the loudness says.
	heardWords bool

	queue chan adaptiveItem
	done  chan struct{}

	// Owned by the worker goroutine.
	checks, noFinalChecks int
	agreedOn              string
	agreeing              int
}

func newAdaptiveClient(ctx context.Context, cfg *config.Config, hint Hint, onResult func(TranscriptResult)) (Client, error) {
	ac := cfg.STT.Adaptive
	world := orDefault(ac.World, []string{"deepgram"})
	indian := orDefault(ac.Indian, []string{"sarvam", "deepgram"})
	var ids []namedIdentifier
	for _, name := range orDefault(ac.Identify, []string{"groq", "sarvam"}) {
		switch name {
		case "groq":
			if cfg.Groq.APIKey == "" {
				log.Printf("[stt:adaptive] no GROQ_API_KEY: groq can't identify languages")
				continue
			}
			ids = append(ids, namedIdentifier{"groq", groqIdentifier(cfg.Groq.APIKey, ac.IdentifyModel)})
		case "sarvam":
			if cfg.Sarvam.APIKey == "" {
				log.Printf("[stt:adaptive] no SARVAM_API_KEY: sarvam can't identify languages")
				continue
			}
			ids = append(ids, namedIdentifier{"sarvam", sarvamIdentifier(cfg.Sarvam.APIKey)})
		default:
			return nil, fmt.Errorf("[stt.adaptive] identify: unknown identifier %q (supported: groq, sarvam)", name)
		}
	}
	if len(ids) == 0 {
		log.Printf("[stt:adaptive] no language identifier configured: the listener will never switch")
	}
	build := func(ctx context.Context, spec listenerSpec, onResult func(TranscriptResult)) (Client, error) {
		names := world
		if spec.indian {
			names = indian
		}
		return newChain(ctx, cfg, names, spec.language, onResult)
	}
	hindi, min := ac.HindiConfidence, ac.MinConfidence
	if hindi <= 0 {
		hindi = defaultHindiConfidence
	}
	if min <= 0 {
		min = defaultMinConfidence
	}
	indianPrior := strings.EqualFold(hint.Region, "IN") || indianRegional[baseLanguage(hint.Language)]
	log.Printf("[stt:adaptive] caller hint: language=%q region=%q", hint.Language, hint.Region)
	return startAdaptive(ctx, build, ids, startListener(hint), indianPrior, hindi, min, onResult)
}

func orDefault(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}

func startAdaptive(ctx context.Context, build listenerFactory, ids []namedIdentifier, start listenerSpec, indianPrior bool, hindiConfidence, minConfidence float64, onResult func(TranscriptResult)) (*adaptiveClient, error) {
	actx, cancel := context.WithCancel(ctx)
	a := &adaptiveClient{
		ctx: actx, cancel: cancel, build: build, ids: ids, onResult: onResult,
		hindiConfidence: hindiConfidence, minConfidence: minConfidence, indianPrior: indianPrior,
		holdMax: adaptiveHoldMax, noFinalQuiet: noFinalQuiet, turnStart: time.Now(),
		queue: make(chan adaptiveItem, 32), done: make(chan struct{}),
	}
	spec := start
	c, id, err := a.open(spec)
	if err != nil && start != (listenerSpec{}) {
		log.Printf("[stt:adaptive] %s unavailable (%v); starting on the world listener", start, err)
		spec = listenerSpec{}
		c, id, err = a.open(spec)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	a.mu.Lock()
	a.current, a.spec, a.acceptID = c, spec, id
	a.mu.Unlock()
	log.Printf("[stt:adaptive] listening with %s", spec)
	go a.run()
	return a, nil
}

// open builds a listener. Its results are dropped until the caller makes
// it current by setting acceptID to the returned id.
func (a *adaptiveClient) open(spec listenerSpec) (Client, int, error) {
	a.mu.Lock()
	a.nextID++
	id := a.nextID
	a.mu.Unlock()
	c, err := a.build(a.ctx, spec, func(r TranscriptResult) { a.listenerResult(id, r) })
	if err != nil {
		return nil, 0, err
	}
	return c, id, nil
}

func (a *adaptiveClient) SendAudio(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return fmt.Errorf("stt adaptive: closed")
	}
	now := time.Now()
	a.frames = append(a.frames, timedFrame{at: now, pcm: append([]byte(nil), data...)})
	cut := 0
	for cut < len(a.frames) && now.Sub(a.frames[cut].at) > adaptiveBufferMax {
		cut++
	}
	a.frames = a.frames[cut:]

	peak := framePeak(data)
	if peak > voicedPeak {
		a.voicedSinceFinal++
	}
	if peak > speechPeak {
		a.loudSinceFinal++
		a.lastLoudAt = now
	}
	if !a.noFinalQueued && !a.heardWords && a.loudSinceFinal >= noFinalSpeechFrames && now.Sub(a.lastLoudAt) > a.noFinalQuiet {
		// The caller clearly spoke and the listener said nothing: Chinese on
		// Deepgram multi comes back empty.
		a.noFinalQueued = true
		a.enqueueLocked(adaptiveItem{pcm: a.turnAudioLocked(), spec: a.spec, voiced: a.voicedSinceFinal, from: a.acceptID, at: now})
	}
	return a.current.SendAudio(data)
}

func (a *adaptiveClient) listenerResult(id int, r TranscriptResult) {
	a.mu.Lock()
	if id != a.acceptID {
		a.mu.Unlock()
		return
	}
	if !r.IsFinal {
		// Words are coming, so this isn't speech the listener missed. Live,
		// a quiet caller's last "loud" frame came ~1.5s before the end of a
		// Hindi sentence, the no-transcript check fired early, and the real
		// final waited behind it for 1-2.6s.
		if strings.TrimSpace(r.Text) != "" {
			a.heardWords = true
		}
		a.mu.Unlock()
		a.onResult(r)
		return
	}
	if r.Language == "" && a.spec.language != "" {
		// Deepgram with a language fixed doesn't label its transcripts.
		r.Language = a.spec.language
	}
	now := time.Now()
	item := adaptiveItem{final: &r, pcm: a.turnAudioLocked(), spec: a.spec, voiced: a.voicedSinceFinal, from: id, at: now}
	a.turnStart = now
	a.loudSinceFinal, a.voicedSinceFinal = 0, 0
	a.noFinalQueued, a.heardWords = false, false
	// Finals queue even when they won't be checked, so a turn released
	// after a check never lands after the turn that followed it.
	a.enqueueLocked(item)
	a.mu.Unlock()
}

func (a *adaptiveClient) enqueueLocked(item adaptiveItem) {
	select {
	case a.queue <- item:
	default:
		// Only if the worker is badly stuck; pass the turn on unchecked.
		if item.final != nil {
			go a.onResult(*item.final)
		}
	}
}

// turnAudioLocked is the caller's audio since the last turn (plus pre-roll).
func (a *adaptiveClient) turnAudioLocked() []byte {
	from := a.turnStart.Add(-adaptivePreRoll)
	var pcm []byte
	for _, f := range a.frames {
		if !f.at.Before(from) {
			pcm = append(pcm, f.pcm...)
		}
	}
	return pcm
}

func (a *adaptiveClient) run() {
	defer close(a.done)
	for {
		select {
		case item := <-a.queue:
			a.process(item)
		case <-a.ctx.Done():
			return
		}
	}
}

func (a *adaptiveClient) process(item adaptiveItem) {
	a.mu.Lock()
	stale := item.from != a.acceptID
	a.mu.Unlock()
	if stale {
		// Heard by a listener the call has since left; its audio was
		// replayed to the new one, which will report it properly.
		return
	}
	if item.verdict != nil {
		a.applyBackgroundVerdict(item)
		return
	}
	kind := checkHold
	if item.final != nil {
		kind = a.checkKind(*item.final, item.voiced)
	}
	switch kind {
	case checkNone:
		a.onResult(*item.final)
		return
	case checkBackground:
		// A confident Hindi turn on an Indian call: the caller doesn't wait
		// for the check. It runs anyway -- Punjabi comes back from multi as
		// Hindi at 1.00 -- and moves the listener for the turns after.
		a.onResult(*item.final)
		a.checks++
		go func() {
			id, ok := a.identifyTurn(item, true)
			if !ok {
				return
			}
			item.verdict = &id
			a.mu.Lock()
			a.enqueueLocked(item)
			a.mu.Unlock()
		}()
		return
	}
	if item.final == nil {
		if len(a.ids) == 0 || a.noFinalChecks >= adaptiveMaxNoFinal || a.checks >= adaptiveMaxChecks {
			return
		}
		a.noFinalChecks++
	}
	a.checks++

	started := time.Now()
	id, ok := a.identifyTurn(item, false)
	heard := "no transcript"
	if item.final != nil {
		heard = fmt.Sprintf("%s %s@%.2f %q", item.final.Provider, orDash(item.final.Language), item.final.Confidence, truncateText(item.final.Text, 50))
	}
	elapsed := time.Since(started).Round(10 * time.Millisecond)

	if !ok {
		log.Printf("[stt:adaptive] checked turn (%s): not identified in %s; keeping the transcript", heard, elapsed)
		a.release(item)
		return
	}
	if item.spec.covers(id.Language) {
		log.Printf("[stt:adaptive] checked turn (%s): %s says %s in %s; %s hears it", heard, id.Source, id.Language, elapsed, item.spec)
		a.agree(id.Language)
		a.release(item)
		return
	}
	next, known := listenerFor(id.Language)
	if !known {
		log.Printf("[stt:adaptive] checked turn (%s): %s says %s in %s; no listener known for it, keeping %s", heard, id.Source, id.Language, elapsed, item.spec)
		a.release(item)
		return
	}
	log.Printf("[stt:adaptive] checked turn (%s): %s says %s in %s; switching to %s", heard, id.Source, id.Language, elapsed, next)
	if id.Text != "" {
		a.onResult(TranscriptResult{Text: id.Text, IsFinal: true, Language: id.Language, Provider: id.Source})
	} else {
		a.release(item)
	}
	a.agreedOn, a.agreeing = "", 0
	a.switchTo(next, item.at)
}

func (a *adaptiveClient) release(item adaptiveItem) {
	if item.final != nil {
		a.onResult(*item.final)
	}
}

// agree counts checks that confirmed the listener; after a couple the
// language is settled and Hindi turns stop being checked on sight.
func (a *adaptiveClient) agree(lang string) {
	if lang == a.agreedOn {
		a.agreeing++
	} else {
		a.agreedOn, a.agreeing = lang, 1
	}
}

func (a *adaptiveClient) settled() bool { return a.agreeing >= adaptiveSettleAfter }

// applyBackgroundVerdict acts on a background check: the listener hears
// the language (the call settles), or the call moves to one that does, for
// the turns after. Audio since the last turn is replayed to it.
func (a *adaptiveClient) applyBackgroundVerdict(item adaptiveItem) {
	id := *item.verdict
	heard := fmt.Sprintf("%s %s@%.2f %q", item.final.Provider, orDash(item.final.Language), item.final.Confidence, truncateText(item.final.Text, 50))
	if item.spec.covers(id.Language) {
		log.Printf("[stt:adaptive] background check (%s): %s says %s; %s hears it", heard, id.Source, id.Language, item.spec)
		a.agree(id.Language)
		return
	}
	next, known := listenerFor(id.Language)
	if !known {
		log.Printf("[stt:adaptive] background check (%s): %s says %s; no listener known for it", heard, id.Source, id.Language)
		return
	}
	log.Printf("[stt:adaptive] background check (%s): %s says %s; switching to %s for the next turns", heard, id.Source, id.Language, next)
	a.agreedOn, a.agreeing = "", 0
	a.mu.Lock()
	since := a.turnStart
	a.mu.Unlock()
	a.switchTo(next, since)
}

// checkKind decides how to handle a final. voiced is how many frames of the
// caller's voice the turn had.
func (a *adaptiveClient) checkKind(r TranscriptResult, voiced int) checkKind {
	if len(a.ids) == 0 || a.checks >= adaptiveMaxChecks {
		return checkNone
	}
	// The rules read Deepgram's confidence and language labels. Sarvam's
	// "confidence" is its language probability, and its misses (a world
	// language turned into English) can't be seen from its output.
	if r.Provider != "deepgram" {
		return checkNone
	}
	// "haan", "ok": too little audio to identify, and cheap to mishear.
	// Measured in voice, not words: a Chinese sentence is one "word", and
	// "喂，你好，请问是哪位？" came back from multi as the five-character
	// "真是呀为，".
	if voiced < shortTurnFrames {
		return checkNone
	}
	if r.Confidence > 0 && r.Confidence < a.minConfidence {
		return checkHold
	}
	if r.Language == "hi" && !a.settled() {
		switch {
		case r.Confidence < a.hindiConfidence:
			return checkHold
		case a.indianPrior:
			return checkBackground
		}
	}
	return checkNone
}

// identifyTurn asks which language the turn is in.
//
// Whisper (the first identifier) knows every language but, on short turns,
// mislabels Indian ones -- live: Bengali as Vietnamese, Gujarati as
// Bengali, Punjabi as Hindi. Sarvam named every Indian language right but
// calls every world language "en". So Sarvam runs alongside on a call that
// looks Indian (an Indian number, Hindi-labelled or Indic-script text), and
// its answer is taken whenever it names an Indian language; it's also the
// transcript used when the call moves to the Indian listener. Whisper's
// answer stands for everything else.
//
// thorough waits for every identifier asked, not just the first usable
// answer -- for background checks, where nobody is waiting.
func (a *adaptiveClient) identifyTurn(item adaptiveItem, thorough bool) (Identification, bool) {
	if len(item.pcm) == 0 || len(a.ids) == 0 {
		return Identification{}, false
	}
	ctx, cancel := context.WithTimeout(a.ctx, a.holdMax)
	defer cancel()

	type answer struct {
		id  Identification
		err error
		src string
	}
	answers := make(chan answer, len(a.ids))
	launched, done := map[string]bool{}, map[string]bool{}
	var sarvamID *namedIdentifier
	for i := range a.ids {
		if a.ids[i].name == "sarvam" {
			sarvamID = &a.ids[i]
		}
	}
	launch := func(n namedIdentifier) {
		if launched[n.name] {
			return
		}
		launched[n.name] = true
		go func() {
			id, err := n.identify(ctx, item.pcm)
			if id.Source == "" {
				id.Source = n.name
			}
			answers <- answer{id, err, n.name}
		}()
	}
	pending := func(name string) bool { return launched[name] && !done[name] }

	looksIndian := a.indianPrior || item.spec.indian ||
		(item.final != nil && (item.final.Language == "hi" || hasIndicScript(item.final.Text)))
	launch(a.ids[0])
	if looksIndian && sarvamID != nil {
		launch(*sarvamID)
	}
	// Deepgram was unsure of this "hi": Whisper also calls Punjabi Hindi, so
	// Sarvam gets a say before the turn is released as Hindi.
	unsureHindi := thorough || (item.final != nil && item.final.Confidence < 0.95)

	var whisper, sarvam *Identification // the latest usable answer from each side
	for {
		// Decide with what's in, or wait for more.
		if sarvam != nil && isIndian(sarvam.Language) {
			return *sarvam, true
		}
		if whisper != nil {
			lang := whisper.Language
			_, known := listenerFor(lang)
			switch {
			case indianRegional[lang]:
				// Sarvam's text (and its better Indian label) is worth waiting for.
				if sarvamID != nil && !done["sarvam"] {
					launch(*sarvamID)
					break
				}
				return *whisper, true
			case lang == "hi" && unsureHindi && pending("sarvam"):
				// wait
			case !known && !item.spec.covers(lang) && pending("sarvam"):
				// A language no listener hears may be a Whisper slip; Sarvam may know better.
			default:
				return *whisper, true
			}
		} else if len(launched) == len(done) {
			// Everyone asked so far has answered without a usable language:
			// ask the next identifier not yet asked, or give up.
			next := false
			for _, n := range a.ids {
				if !launched[n.name] {
					launch(n)
					next = true
					break
				}
			}
			if !next {
				return Identification{}, false
			}
		}
		if len(launched) == len(done) {
			if whisper != nil {
				return *whisper, true
			}
			continue
		}
		select {
		case ans := <-answers:
			done[ans.src] = true
			switch {
			case ans.err != nil:
				log.Printf("[stt:adaptive] %s couldn't identify the turn: %v", ans.src, ans.err)
			case ans.src == "sarvam":
				id := ans.id
				sarvam = &id
			case ans.id.Language != "":
				id := ans.id
				whisper = &id
			}
		case <-ctx.Done():
			if whisper != nil {
				return *whisper, true
			}
			return Identification{}, false
		}
	}
}

// isIndian: Hindi or a regional Indian language -- what Sarvam's labels can
// be trusted for.
func isIndian(lang string) bool { return lang == "hi" || indianRegional[lang] }

// switchTo moves the call to another listener. The old one keeps hearing
// the caller until the new one is connected; its results are dropped from
// the moment the switch is decided. Everything the caller said after the
// checked turn (from replayFrom) is replayed to the new listener: live, a
// Kannada caller went on talking during a 1.7s check, and without the
// replay the new listener heard only the tail -- "I'll pay" instead of
// "I'll pay on Monday".
func (a *adaptiveClient) switchTo(spec listenerSpec, replayFrom time.Time) {
	a.mu.Lock()
	prevAccept := a.acceptID
	a.acceptID = -1
	a.mu.Unlock()

	c, id, err := a.open(spec)
	if err != nil {
		log.Printf("[stt:adaptive] couldn't start %s (%v); staying on %s", spec, err, a.currentSpec())
		a.mu.Lock()
		a.acceptID = prevAccept
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	old := a.current
	if old == nil { // closed meanwhile
		a.mu.Unlock()
		c.Close()
		return
	}
	replayed := 0
	for _, f := range a.frames {
		if !f.at.Before(replayFrom) {
			if err := c.SendAudio(f.pcm); err != nil {
				break
			}
			replayed++
		}
	}
	a.current, a.spec, a.acceptID = c, spec, id
	a.turnStart = replayFrom
	a.loudSinceFinal, a.voicedSinceFinal, a.noFinalQueued, a.heardWords = 0, 0, false, false
	a.mu.Unlock()
	go old.Close()
	log.Printf("[stt:adaptive] now listening with %s (replayed %dms of audio)", spec, replayed*20)
}

func (a *adaptiveClient) currentSpec() listenerSpec {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.spec
}

// EmitsPartials answers for the listener serving now; the pipeline asks on
// every barge-in decision.
func (a *adaptiveClient) EmitsPartials() bool {
	a.mu.Lock()
	c := a.current
	a.mu.Unlock()
	if ep, ok := c.(PartialsEmitter); ok {
		return ep.EmitsPartials()
	}
	return true
}

func (a *adaptiveClient) Close() {
	a.cancel()
	a.mu.Lock()
	c := a.current
	a.current = nil
	a.mu.Unlock()
	if c != nil {
		c.Close()
	}
	<-a.done
}

// hasIndicScript: any character from Devanagari through Malayalam.
func hasIndicScript(text string) bool {
	for _, r := range text {
		if r >= 0x0900 && r <= 0x0D7F {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncateText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
