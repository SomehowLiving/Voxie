package pipeline

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// turnClock times one caller turn, from the moment the caller stopped
// speaking to the moment the first audio of the reply goes out, and logs
// where that time went:
//
//		[turn-timing] 2.41s to first audio: stt 0.62s (check 0.00s), merge 0.35s,
//		llm 0.48s (first sentence +0.20s), voice 0.43s, hold 0.00s [hi deepgram]
//
//	  - stt: caller's voice ends → final transcript (endpointing, and the
//	    adaptive listener's language check, shown separately)
//	  - merge: the turn buffer waiting for more of the sentence
//	  - llm: turn sent to the agent → its first words; then until the first
//	    whole sentence, which is what the voice needs
//	  - voice: first sentence → first audio
//	  - hold: first audio held while the caller was still talking
type turnClock struct {
	speechEnd time.Time // zero if the caller's voice wasn't measured
	finalAt   time.Time
	dispatch  time.Time
	checkMs   int64
	language  string
	provider  string

	llmFirst      atomic.Int64 // unix nanos
	firstSentence atomic.Int64
	holdNs        atomic.Int64
	logged        atomic.Bool
}

func newTurnClock(ev TranscriptEvent, language string) *turnClock {
	return &turnClock{speechEnd: ev.SpeechEndAt, finalAt: ev.TurnStart, dispatch: time.Now(),
		checkMs: ev.CheckMs, language: language, provider: ev.Provider}
}

func stampOnce(v *atomic.Int64) { v.CompareAndSwap(0, time.Now().UnixNano()) }

func secs(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }

// firstAudio logs the turn's timeline; only the first call does anything.
func (c *turnClock) firstAudio() {
	if c == nil || !c.logged.CompareAndSwap(false, true) {
		return
	}
	now := time.Now()
	start, approx := c.speechEnd, ""
	// The onset VAD can miss a short phrase; its last reading is then from
	// an earlier utterance (live: "stt 14.38s" for a final that came on time).
	if start.IsZero() || start.After(c.finalAt) || c.finalAt.Sub(start) > 5*time.Second {
		start, approx = c.finalAt, "≥"
	}
	llm, sentence := time.Unix(0, c.llmFirst.Load()), time.Unix(0, c.firstSentence.Load())
	if c.llmFirst.Load() == 0 {
		llm = c.dispatch
	}
	if c.firstSentence.Load() == 0 {
		sentence = llm
	}
	hold := time.Duration(c.holdNs.Load())
	log.Printf("[turn-timing] %s%s to first audio: stt %s (check %s), merge %s, llm %s (first sentence +%s), voice %s, hold %s [%s %s]",
		approx, secs(now.Sub(start)),
		secs(c.finalAt.Sub(start)), secs(time.Duration(c.checkMs)*time.Millisecond),
		secs(c.dispatch.Sub(c.finalAt)),
		secs(llm.Sub(c.dispatch)), secs(sentence.Sub(llm)),
		secs(now.Sub(sentence)-hold), secs(hold),
		orDash(c.language), orDash(c.provider))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (p *Pipeline) clockFor(gen uint64) *turnClock {
	if c, ok := p.clocks.Load(gen); ok {
		return c.(*turnClock)
	}
	return nil
}
