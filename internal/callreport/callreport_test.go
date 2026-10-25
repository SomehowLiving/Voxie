package callreport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCall(t *testing.T, id string) (*Call, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	return newCall(w, id, false, func() time.Time { clock = clock.Add(time.Second); return clock }), dir
}

func spoken(total int64) *Stages { return &Stages{STT: 300, LLM: 400, Voice: 200, Total: total} }

func has(sigs []string, s string) bool {
	for _, x := range sigs {
		if x == s {
			return true
		}
	}
	return false
}

func TestReportsNeverHoldWhatWasSaid(t *testing.T) {
	c, dir := testCall(t, "c1")
	c.Turn(TurnInput{Text: "my card number is 4111 1111 1111 1111", Lang: "en", Spoke: true, Ms: spoken(1200)})
	c.End("caller", false)
	b, err := os.ReadFile(filepath.Join(dir, "calls-2026-10-25.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("4111")) || bytes.Contains(b, []byte("card")) {
		t.Fatalf("the caller's words reached the report:\n%s", b)
	}
	if n := bytes.Count(b, []byte("\n")); n != 2 {
		t.Fatalf("got %d lines; want one turn and one call", n)
	}
}

func TestSignals(t *testing.T) {
	cases := []struct {
		name  string
		turns []TurnInput
		want  string // the last turn's signal; "" for none
	}{
		{"fast reply", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(1400)}}, ""},
		{"slow", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(3400)}}, SignalSlow},
		{"silent", []TurnInput{{Text: "book a table", AgentError: true}}, SignalSilent},
		{"superseded is not silent", []TurnInput{{Text: "book a", Superseded: true}}, ""},
		{"cut before speaking is not silent", []TurnInput{{Text: "book a table", Cut: true}}, SignalCutEarly},
		{"repeat", []TurnInput{{Text: "I want to book a table", Spoke: true, Ms: spoken(900)}, {Text: "I want to book a table!", Spoke: true, Ms: spoken(900)}}, SignalRepeat},
		{"different words", []TurnInput{{Text: "I want to book a table", Spoke: true, Ms: spoken(900)}, {Text: "for two people at eight", Spoke: true, Ms: spoken(900)}}, ""},
		{"check-in mid-call", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(900)}, {Text: "Hello? Are you there?", Spoke: true, Ms: spoken(900)}}, SignalCheckIn},
		{"hello to start is fine", []TurnInput{{Text: "Hello?", Spoke: true, Ms: spoken(900)}}, ""},
		{"a sentence with hello", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(900)}, {Text: "hello I need to change my booking", Spoke: true, Ms: spoken(900)}}, ""},
		{"Hindi check-in", []TurnInput{{Text: "टेबल बुक करो", Spoke: true, Ms: spoken(900)}, {Text: "हेलो? सुन रहे हो?", Spoke: true, Ms: spoken(900)}}, SignalCheckIn},
		{"language flip-back", []TurnInput{{Text: "one", Lang: "en", Spoke: true}, {Text: "two", Lang: "ur", Spoke: true}, {Text: "three", Lang: "en", Spoke: true}}, SignalLangFlip},
		{"a real switch", []TurnInput{{Text: "one", Lang: "en", Spoke: true}, {Text: "two", Lang: "hi", Spoke: true}, {Text: "three", Lang: "hi", Spoke: true}}, ""},
		{"unsure", []TurnInput{{Text: "book a table", Confidence: 0.42, Spoke: true}}, SignalLowConfidence},
		{"cut early", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(900), Cut: true, CutAfterMs: 600}}, SignalCutEarly},
		{"cut later", []TurnInput{{Text: "book a table", Spoke: true, Ms: spoken(900), Cut: true, CutAfterMs: 4000}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCall(t, "c")
			var last Turn
			for _, in := range tc.turns {
				last = c.Turn(in)
			}
			if tc.want == "" && len(last.Signals) > 0 {
				t.Fatalf("signals %v; want none", last.Signals)
			}
			if tc.want != "" && !has(last.Signals, tc.want) {
				t.Fatalf("signals %v; want %s", last.Signals, tc.want)
			}
		})
	}
}

func TestCallSummary(t *testing.T) {
	c, _ := testCall(t, "twilio-CA1")
	c.Turn(TurnInput{Text: "hi there friend", Lang: "en", Voice: "cartesia", Spoke: true, Ms: spoken(1000)})
	c.Turn(TurnInput{Text: "what time do you open", Lang: "en", Voice: "cartesia", Spoke: true, Ms: spoken(2000)})
	c.Turn(TurnInput{Text: "kal subah", Lang: "hi", Voice: "vibevoice", Spoke: true, Ms: spoken(4000)})
	c.Turn(TurnInput{Text: "ok", Lang: "hi", Spoke: true, Ms: spoken(9000), Approx: true})
	yes := true
	c.SetOutcome(Outcome{Success: &yes, Reason: "booked"})
	s := c.End("caller", true)
	if s.Transport != "phone" || s.EndedBy != "caller" || s.Turns != 4 || s.Spoken != 4 {
		t.Fatalf("summary %+v", s)
	}
	if strings.Join(s.Languages, ",") != "en,hi" || s.Voices["cartesia"] != 2 {
		t.Fatalf("languages %v voices %v", s.Languages, s.Voices)
	}
	// The approximate 9s turn is left out: p50 of 1000, 2000, 4000.
	if s.ReplyP50 != 2000 || s.ReplyP95 != 4000 {
		t.Fatalf("p50 %d p95 %d", s.ReplyP50, s.ReplyP95)
	}
	if s.Signals[SignalSlow] != 2 || s.Signals[SignalHungUpMidReply] != 1 || s.TurnsWithSignals != 2 {
		t.Fatalf("signals %v, %d turns with signals", s.Signals, s.TurnsWithSignals)
	}
	if s.Outcome == nil || !*s.Outcome.Success || s.Outcome.Reason != "booked" {
		t.Fatalf("outcome %+v", s.Outcome)
	}
	if again := c.End("agent", false); again.EndedBy != "caller" {
		t.Fatal("a second End rewrote the call")
	}
}

func TestNilCallRecordsNothing(t *testing.T) {
	var c *Call
	c.Turn(TurnInput{Text: "hi"})
	c.SetOutcome(Outcome{})
	c.End("caller", false)
	SetDefault(nil)
	if NewCall("x", false) != nil {
		t.Fatal("NewCall without a writer must return nil")
	}
}

func TestSummarize(t *testing.T) {
	c, dir := testCall(t, "c1")
	c.Turn(TurnInput{Text: "book a table please", Lang: "en", Listener: "deepgram", Voice: "cartesia", Spoke: true, Ms: spoken(1100)})
	c.Turn(TurnInput{Text: "book a table please", Lang: "en", Listener: "deepgram", Voice: "cartesia", Spoke: true, Ms: spoken(3500)})
	c.Turn(TurnInput{Text: "for two", Lang: "en", Superseded: true})
	no := false
	c.SetOutcome(Outcome{Success: &no})
	c.End("agent", false)

	turns, calls, err := Load([]string{dir})
	if err != nil || len(turns) != 3 || len(calls) != 1 {
		t.Fatalf("loaded %d turns, %d calls, %v", len(turns), len(calls), err)
	}
	var out bytes.Buffer
	Summarize(&out, turns, calls)
	got := out.String()
	for _, want := range []string{
		"Calls: 1", "the agent 1", "Outcome (reported by the app): 0 of 1 calls succeeded",
		// The superseded half-sentence isn't an answered turn.
		"No sign of trouble on 50% of 2 answered turns",
		"By language", "By voice", "cartesia", "By listener", "deepgram",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestSummarizeNothing(t *testing.T) {
	var out bytes.Buffer
	Summarize(&out, nil, nil)
	if !strings.Contains(out.String(), "No calls recorded yet") {
		t.Fatal(out.String())
	}
}
