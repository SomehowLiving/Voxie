package tts

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A failover voice runs an ordered list of providers ([tts] failover =
// ["vibevoice", "cartesia"]): each sentence goes to the first provider that
// is available, and moves on to the next when one fails, is rate limited,
// is out of credit, or doesn't start speaking in time. Order is the
// deployment's preference (cheapest, best for its callers); a per-language
// order ([tts.languages] hi = [...]) overrides it for that language.
//
// Before, the server had a single voice: when it failed, the rest of the
// reply was dropped and the caller heard silence.
//
// A provider that fails is skipped for a while, then tried again:
//   - rate or concurrency limit (429): cooldown
//   - out of credit (402): longCooldown — someone has to top it up
//   - key rejected (401/403): until restart
//   - anything else, or no first audio in time: cooldown
//   - the sentence's language isn't one it speaks: not skipped, only that
//     sentence goes elsewhere
//
// A provider that fails after its audio started can't be retried for that
// sentence (the caller would hear its start twice); the sentence ends and
// the next one goes to the next provider.

const (
	defaultFirstAudioTimeout = 3 * time.Second
	defaultCooldown          = 30 * time.Second
	longCooldown             = 10 * time.Minute
	disabledCooldown         = 100 * 365 * 24 * time.Hour
)

type ttsProvider struct {
	name   string
	client Client

	mu        sync.Mutex
	downUntil time.Time
}

func (p *ttsProvider) available(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !now.Before(p.downUntil)
}

func (p *ttsProvider) markDown(now time.Time, d time.Duration, why string) {
	p.mu.Lock()
	p.downUntil = now.Add(d)
	p.mu.Unlock()
	if d >= disabledCooldown {
		log.Printf("[tts:failover] %s off until restart: %s", p.name, why)
	} else {
		log.Printf("[tts:failover] %s skipped for %s: %s", p.name, d.Round(time.Second), why)
	}
}

type failoverClient struct {
	providers  map[string]*ttsProvider
	order      []string
	byLanguage map[string][]string
	firstAudio time.Duration
	cooldown   time.Duration
	now        func() time.Time
}

// FailoverOptions configures newFailoverClient; zero values take defaults.
type FailoverOptions struct {
	Order             []string
	ByLanguage        map[string][]string
	FirstAudioTimeout time.Duration
	Cooldown          time.Duration
}

func newFailoverClient(clients map[string]Client, opt FailoverOptions) (*failoverClient, error) {
	if len(opt.Order) == 0 {
		return nil, fmt.Errorf("tts provider \"failover\" requires [tts] failover = [\"provider\", ...]")
	}
	f := &failoverClient{providers: map[string]*ttsProvider{}, order: opt.Order, byLanguage: opt.ByLanguage,
		firstAudio: opt.FirstAudioTimeout, cooldown: opt.Cooldown, now: time.Now}
	if f.firstAudio <= 0 {
		f.firstAudio = defaultFirstAudioTimeout
	}
	if f.cooldown <= 0 {
		f.cooldown = defaultCooldown
	}
	for name, c := range clients {
		f.providers[name] = &ttsProvider{name: name, client: c}
	}
	check := func(names []string) error {
		for _, n := range names {
			if f.providers[n] == nil {
				return fmt.Errorf("tts failover: provider %q is not configured", n)
			}
		}
		return nil
	}
	if err := check(opt.Order); err != nil {
		return nil, err
	}
	for _, names := range opt.ByLanguage {
		if err := check(names); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// candidates is the provider order for this sentence: the language's own
// order if it has one, the default otherwise; providers that are down go
// last rather than not at all, so a sentence is never dropped just because
// every provider failed once.
func (f *failoverClient) candidates(text string) []*ttsProvider {
	names := f.order
	if lang := sentenceLanguage(text); lang != "" {
		if l, ok := f.byLanguage[lang]; ok && len(l) > 0 {
			names = l
		}
	}
	now := f.now()
	var up, down []*ttsProvider
	for _, n := range names {
		if p := f.providers[n]; p.available(now) {
			up = append(up, p)
		} else {
			down = append(down, p)
		}
	}
	return append(up, down...)
}

func (f *failoverClient) fail(p *ttsProvider, err error) {
	d, why := classify(err, f.cooldown)
	if d == 0 {
		log.Printf("[tts:failover] %s can't speak this sentence: %s", p.name, why)
		return
	}
	p.markDown(f.now(), d, why)
}

// classify maps a provider error to how long to skip the provider.
func classify(err error, cooldown time.Duration) (time.Duration, string) {
	msg := strings.ToLower(err.Error())
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(msg, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("not supported", "unsupported language", "invalid language"):
		return 0, err.Error()
	case has("401", "403", "unauthorized", "forbidden", "invalid api key", "invalid_api_key"):
		return disabledCooldown, "key rejected: " + err.Error()
	case has("402", "payment required", "insufficient credit", "out of credit", "quota exceeded", "credits"):
		return longCooldown, "out of credit: " + err.Error()
	case has("429", "rate limit", "too many requests", "concurrency"):
		return cooldown, "rate or concurrency limit: " + err.Error()
	}
	return cooldown, err.Error()
}

var errNoFirstAudio = errors.New("no audio in time")

func (f *failoverClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	var last error
	for _, p := range f.candidates(text) {
		b, err := p.client.Synthesize(ctx, text)
		if err == nil {
			return b, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f.fail(p, err)
		last = err
	}
	return nil, fmt.Errorf("tts failover: every provider failed: %w", last)
}

func (f *failoverClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return f.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

func (f *failoverClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	var last error
	for i, p := range f.candidates(text) {
		out, err := f.try(ctx, p, text, vc)
		if err == nil {
			if i > 0 {
				log.Printf("[tts:failover] sentence spoken by %s", p.name)
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // the caller cut in: not the provider's fault
		}
		f.fail(p, err)
		last = err
	}
	return nil, fmt.Errorf("tts failover: every provider failed: %w", last)
}

// try starts one provider and waits for its first audio. On success the
// returned channel carries that audio and the rest of the stream; a failure
// after audio started ends the sentence and marks the provider down.
func (f *failoverClient) try(ctx context.Context, p *ttsProvider, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	actx, cancel := context.WithCancel(ctx)
	// Started in the background so the timer covers the start too: a
	// provider at its concurrency limit waits for a slot before it returns,
	// and that wait is exactly when the next provider should take over.
	type started struct {
		ch  <-chan StreamChunk
		err error
	}
	startCh := make(chan started, 1)
	go func() {
		var st started
		if cs, ok := p.client.(ControllableStreamer); ok {
			st.ch, st.err = cs.SynthesizeStreamWithControls(actx, text, vc)
		} else {
			st.ch, st.err = p.client.SynthesizeStream(actx, text)
		}
		startCh <- st
	}()
	timer := time.NewTimer(f.firstAudio)
	defer timer.Stop()
	var in <-chan StreamChunk
	select {
	case st := <-startCh:
		if st.err != nil {
			cancel()
			return nil, st.err
		}
		in = st.ch
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("%w (%s, still starting)", errNoFirstAudio, f.firstAudio)
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	var first StreamChunk
	for {
		select {
		case c, ok := <-in:
			if !ok {
				cancel()
				return nil, errors.New("stream ended without audio")
			}
			if c.Err != nil {
				cancel()
				return nil, c.Err
			}
			if len(c.PCM) == 0 {
				continue
			}
			first = c
		case <-timer.C:
			cancel()
			return nil, fmt.Errorf("%w (%s)", errNoFirstAudio, f.firstAudio)
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		}
		break
	}
	out := make(chan StreamChunk, 8)
	go func() {
		defer cancel()
		defer close(out)
		send := func(c StreamChunk) bool {
			select {
			case out <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(first) {
			return
		}
		for c := range in {
			if c.Err != nil && ctx.Err() == nil {
				f.fail(p, c.Err)
			}
			if !send(c) {
				return
			}
		}
	}()
	return out, nil
}

// --- language tags ------------------------------------------------------------

// leadingTags matches the routing tags an agent may put at the start of a
// sentence: "[lang:hi] ", "[voice:male-2] ", in either order.
var leadingTags = regexp.MustCompile(`^\s*(?:\[(?:lang|voice):[a-zA-Z0-9_-]{1,16}\]\s*)+`)
var langInTags = regexp.MustCompile(`\[lang:([a-zA-Z-]{2,8})\]`)

// sentenceLanguage is the "[lang:xx]" a sentence starts with ("" if none),
// as its base code ("hi-IN" → "hi").
func sentenceLanguage(text string) string {
	tags := leadingTags.FindString(text)
	m := langInTags.FindStringSubmatch(tags)
	if m == nil {
		return ""
	}
	return strings.ToLower(strings.SplitN(m[1], "-", 2)[0])
}

// LanguageStreamer is a provider that takes the sentence's language as a
// parameter instead of reading tags from the text.
type LanguageStreamer interface {
	SynthesizeStreamLanguage(ctx context.Context, text, language string, vc VoiceControls) (<-chan StreamChunk, error)
}

// tagAdapter gives each provider the sentence the way it understands it.
// Voxie's voice router (vibevoice) reads "[lang:xx]"/"[voice:…]" tags itself;
// any other provider would be handed them as words to speak, so they're
// removed, and the language is passed as a parameter where the provider
// takes one (Cartesia).
type tagAdapter struct {
	inner Client
}

func adaptTags(name string, c Client) Client {
	if name == "vibevoice" {
		return c
	}
	return &tagAdapter{inner: c}
}

func untag(text string) (string, string) {
	lang := sentenceLanguage(text)
	return strings.TrimSpace(leadingTags.ReplaceAllString(text, "")), lang
}

func (t *tagAdapter) Synthesize(ctx context.Context, text string) ([]byte, error) {
	clean, _ := untag(text)
	return t.inner.Synthesize(ctx, clean)
}

func (t *tagAdapter) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return t.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

func (t *tagAdapter) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	clean, lang := untag(text)
	if ls, ok := t.inner.(LanguageStreamer); ok && lang != "" {
		return ls.SynthesizeStreamLanguage(ctx, clean, lang, vc)
	}
	if cs, ok := t.inner.(ControllableStreamer); ok {
		return cs.SynthesizeStreamWithControls(ctx, clean, vc)
	}
	return t.inner.SynthesizeStream(ctx, clean)
}
