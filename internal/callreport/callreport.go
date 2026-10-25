// Package callreport records what happened on each call as JSON lines: one
// line per reply (where its time went, which listener and voice, whether it
// was cut off, and the signals of a caller having a bad time) and one summary
// line per call. `voxie report` reads them back (see Summarize).
//
// Reports hold numbers and labels only, never what anyone said: the caller's
// words are used, in memory, to notice a repeat, and dropped. That is what
// lets reports stay on by default, on every deployment.
package callreport

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stages is a reply's timeline in milliseconds, as the [turn-timing] log line
// shows it: from the caller's voice ending to the reply's first audio.
type Stages struct {
	STT           int64 `json:"stt"`            // caller's voice ends → final transcript
	Check         int64 `json:"check"`          // of which: the adaptive listener's language check
	Merge         int64 `json:"merge"`          // turn buffer waiting for more of the sentence
	LLM           int64 `json:"llm"`            // turn sent → agent's first words
	FirstSentence int64 `json:"first_sentence"` // first words → first whole sentence
	Voice         int64 `json:"voice"`          // first sentence → first audio
	Hold          int64 `json:"hold"`           // first audio held while the caller was talking
	Total         int64 `json:"total"`
}

// Turn is one caller turn and the reply to it.
type Turn struct {
	Type       string    `json:"type"` // "turn"
	CallID     string    `json:"call_id"`
	Turn       int       `json:"turn"`
	At         time.Time `json:"at"`
	Lang       string    `json:"lang,omitempty"`       // the caller's language
	ReplyLang  string    `json:"reply_lang,omitempty"` // the reply's [lang:xx]
	Listener   string    `json:"listener,omitempty"`   // speech-to-text provider
	Confidence float64   `json:"confidence,omitempty"`
	Voice      string    `json:"voice,omitempty"` // voice provider of the first sentence
	Ms         *Stages   `json:"ms,omitempty"`    // nil when no audio went out
	// Approx: the caller's voice ending wasn't measured, so the timeline
	// starts at the final transcript and understates the wait.
	Approx     bool     `json:"approx,omitempty"`
	Spoke      bool     `json:"spoke"`                  // any of the reply reached the caller
	Cut        bool     `json:"cut,omitempty"`          // the caller cut in
	CutAfterMs int64    `json:"cut_after_ms,omitempty"` // reply audio before the cut
	Superseded bool     `json:"superseded,omitempty"`   // replaced by the caller's next words, not cut off
	AgentError bool     `json:"agent_error,omitempty"`
	Signals    []string `json:"signals,omitempty"`
}

// TurnInput is what the pipeline knows about a finished reply. Text is the
// caller's words: used to notice a repeat, never written.
type TurnInput struct {
	Text       string
	Lang       string
	ReplyLang  string
	Listener   string
	Confidence float64
	Voice      string
	Ms         *Stages
	Approx     bool
	Spoke      bool
	Cut        bool
	CutAfterMs int64
	Superseded bool
	AgentError bool
}

// Outcome is the app's verdict on the call (the agent reply's "outcome").
type Outcome struct {
	Success *bool  `json:"success,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Summary is one call, written when it ends.
type Summary struct {
	Type      string         `json:"type"` // "call"
	CallID    string         `json:"call_id"`
	Transport string         `json:"transport"` // "webrtc" or "phone"
	Resumed   bool           `json:"resumed,omitempty"`
	Start     time.Time      `json:"start"`
	End       time.Time      `json:"end"`
	DurationS float64        `json:"duration_s"`
	EndedBy   string         `json:"ended_by"` // "caller", "agent" or "error"
	Turns     int            `json:"turns"`
	Spoken    int            `json:"spoken"` // turns whose reply reached the caller
	Languages []string       `json:"languages,omitempty"`
	Voices    map[string]int `json:"voices,omitempty"` // first sentences per voice provider
	ReplyP50  int64          `json:"reply_p50_ms,omitempty"`
	ReplyP95  int64          `json:"reply_p95_ms,omitempty"`
	// Turns with at least one signal, and each signal's count, the call's
	// own (hung_up_mid_reply) included.
	TurnsWithSignals int            `json:"turns_with_signals"`
	Signals          map[string]int `json:"signals,omitempty"`
	Outcome          *Outcome       `json:"outcome,omitempty"`
}

// Writer appends records to one JSON-lines file per day in a directory:
// reports/calls-2026-10-25.jsonl. Safe for concurrent calls.
type Writer struct {
	dir string
	mu  sync.Mutex
}

// NewWriter creates dir if needed.
func NewWriter(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Writer{dir: dir}, nil
}

func (w *Writer) write(at time.Time, v any) {
	line, err := json.Marshal(v)
	if err != nil {
		log.Printf("[callreport] encode: %v", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Opened per record: a few lines a minute per call, and a file that's
	// never held open survives log rotation and a crash mid-call.
	name := filepath.Join(w.dir, "calls-"+at.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[callreport] %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("[callreport] %v", err)
	}
}

var defaultWriter atomic.Pointer[Writer]

// SetDefault sets where new calls write; nil turns reports off.
func SetDefault(w *Writer) { defaultWriter.Store(w) }

// Call records one call. A nil *Call is valid and records nothing, so the
// pipeline needs no checks when reports are off.
type Call struct {
	w     *Writer
	now   func() time.Time
	mu    sync.Mutex
	sum   Summary
	turns []Turn // kept for the summary's percentiles
	prev  []string
	langs []string // each turn's language, in order, for flip-backs
	ended bool
}

// NewCall starts a call's report with the default writer, or returns nil
// when reports are off.
func NewCall(id string, resumed bool) *Call {
	w := defaultWriter.Load()
	if w == nil {
		return nil
	}
	return newCall(w, id, resumed, time.Now)
}

func newCall(w *Writer, id string, resumed bool, now func() time.Time) *Call {
	transport := "webrtc"
	if strings.HasPrefix(id, "twilio-") {
		transport = "phone"
	}
	return &Call{w: w, now: now, sum: Summary{Type: "call", CallID: id, Transport: transport, Resumed: resumed, Start: now()}}
}

// Turn records a finished reply and returns the record it wrote.
func (c *Call) Turn(in TurnInput) Turn {
	if c == nil {
		return Turn{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := Turn{
		Type: "turn", CallID: c.sum.CallID, Turn: len(c.turns) + 1, At: c.now(),
		Lang: in.Lang, ReplyLang: in.ReplyLang, Listener: in.Listener, Confidence: in.Confidence,
		Voice: in.Voice, Ms: in.Ms, Approx: in.Approx, Spoke: in.Spoke, Cut: in.Cut,
		CutAfterMs: in.CutAfterMs, Superseded: in.Superseded, AgentError: in.AgentError,
	}
	words := wordsOf(in.Text)
	c.langs = append(c.langs, in.Lang)
	t.Signals = signals(t, words, c.prev, c.langs)
	c.prev = words
	c.turns = append(c.turns, t)
	c.w.write(t.At, t)
	return t
}

// SetOutcome keeps the app's latest verdict on the call.
func (c *Call) SetOutcome(o Outcome) {
	if c == nil {
		return
	}
	if len(o.Reason) > 60 {
		o.Reason = o.Reason[:60]
	}
	c.mu.Lock()
	c.sum.Outcome = &o
	c.mu.Unlock()
}

// End writes the call's summary; only the first call does anything.
// speaking: the agent was mid-reply when the call ended.
func (c *Call) End(endedBy string, speaking bool) Summary {
	if c == nil {
		return Summary{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return c.sum
	}
	c.ended = true
	s := &c.sum
	s.End = c.now()
	s.DurationS = round1(s.End.Sub(s.Start).Seconds())
	s.EndedBy = endedBy
	s.Turns = len(c.turns)
	s.Voices = map[string]int{}
	s.Signals = map[string]int{}
	seen := map[string]bool{}
	var totals []int64
	for _, t := range c.turns {
		if t.Spoke {
			s.Spoken++
		}
		if t.Lang != "" && !seen[t.Lang] {
			seen[t.Lang] = true
			s.Languages = append(s.Languages, t.Lang)
		}
		if t.Voice != "" {
			s.Voices[t.Voice]++
		}
		if t.Ms != nil && !t.Approx {
			totals = append(totals, t.Ms.Total)
		}
		if len(t.Signals) > 0 {
			s.TurnsWithSignals++
		}
		for _, sig := range t.Signals {
			s.Signals[sig]++
		}
	}
	if endedBy == "caller" && speaking {
		s.Signals[SignalHungUpMidReply]++
	}
	s.ReplyP50, s.ReplyP95 = percentile(totals, 50), percentile(totals, 95)
	if len(s.Voices) == 0 {
		s.Voices = nil
	}
	if len(s.Signals) == 0 {
		s.Signals = nil
	}
	c.w.write(s.End, s)
	return *s
}

func round1(x float64) float64 { return float64(int64(x*10+0.5)) / 10 }

// percentile by nearest rank; 0 for no values.
func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (p*len(s) + 99) / 100
	if i < 1 {
		i = 1
	}
	return s[i-1]
}
