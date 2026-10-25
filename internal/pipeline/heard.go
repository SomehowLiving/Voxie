package pipeline

import (
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/streamcoreai/streamcore-server/internal/tts"
)

// heardLatencyFrames is audio that has left the sender but that the caller
// has not heard yet: network transit plus the far end's jitter buffer. It's
// subtracted before working out how much of a cut-off sentence was heard.
const heardLatencyFrames = 10 // ~200ms

// heardSegment is one sentence of a response on its way to the caller.
// frames counts the PCM frames synthesized for it; sent counts the frames the
// sender actually put on the wire.
type heardSegment struct {
	text   string
	frames atomic.Int64
	sent   atomic.Int64
	done   atomic.Bool // every frame of the sentence has been synthesized
}

// playbackLedger records, per response, which sentences reached the caller's
// ears and how much of each. It answers "what did the caller actually hear?"
// when they interrupt: the generated text may run sentences ahead of the
// audio, and none of what is still queued was heard.
type playbackLedger struct {
	mu       sync.Mutex
	gen      uint64
	segments []*heardSegment
}

// reset starts a new response's record.
func (l *playbackLedger) reset(gen uint64) {
	l.mu.Lock()
	l.gen, l.segments = gen, nil
	l.mu.Unlock()
}

// sentence opens a segment for one sentence of response gen. It returns nil
// when gen is no longer current, so a superseded response records nothing.
func (l *playbackLedger) sentence(gen uint64, text string) *heardSegment {
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen != l.gen {
		return nil
	}
	s := &heardSegment{text: spokenText(text)}
	l.segments = append(l.segments, s)
	return s
}

// heardFrames is how many frames of response gen the caller has heard: 0 if
// it hasn't started, or its audio is still on its way.
func (l *playbackLedger) heardFrames(gen uint64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gen != gen {
		return 0 // the response hasn't opened its ledger yet
	}
	var sent int64
	for _, s := range l.segments {
		sent += s.sent.Load()
	}
	return max(sent-heardLatencyFrames, 0)
}

// unheard reports whether some of the response's audio never reached the
// caller: the response was cut off rather than finished.
func (l *playbackLedger) unheard() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.segments {
		if !s.done.Load() || s.sent.Load() < s.frames.Load() {
			return true
		}
	}
	return false
}

// heard is what the caller heard of the current response: the sentences that
// played in full, then the played share of the one that was cut off, ended at
// a word and marked with "…". Empty if nothing was heard.
func (l *playbackLedger) heard() string {
	l.mu.Lock()
	segs := append([]*heardSegment(nil), l.segments...)
	l.mu.Unlock()

	// The newest frames on the wire haven't reached the caller yet.
	var total int64
	for _, s := range segs {
		total += s.sent.Load()
	}
	budget := total - heardLatencyFrames

	var parts []string
	for _, s := range segs {
		sent, frames := s.sent.Load(), s.frames.Load()
		if sent > budget {
			sent = budget
		}
		budget -= sent
		if sent <= 0 || s.text == "" {
			break
		}
		if s.done.Load() && sent >= frames {
			parts = append(parts, s.text)
			continue
		}
		// Cut off mid-sentence: assume speech runs at an even pace through
		// the sentence and keep the words whose audio had played.
		if frames > 0 {
			if part := prefixWords(s.text, float64(sent)/float64(frames)); part != "" {
				parts = append(parts, part+"…")
			}
		}
		break
	}
	return strings.Join(parts, " ")
}

// spokenText is a sentence as the caller hears it: delivery and language
// tags are never spoken.
func spokenText(text string) string {
	return strings.TrimSpace(tts.StripVoiceTags(text))
}

// prefixWords returns the words of text that fit in the first share
// (0..1) of its characters, never splitting a word or a multi-byte rune.
func prefixWords(text string, share float64) string {
	runes := []rune(text)
	cut := int(float64(len(runes)) * share)
	if cut <= 0 {
		return ""
	}
	if cut >= len(runes) {
		return text
	}
	// Back up to the end of the last whole word.
	for cut > 0 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	return strings.TrimSpace(string(runes[:cut]))
}
