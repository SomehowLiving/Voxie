package callreport

import (
	"strings"
	"unicode"
)

// Signals of a caller having a bad time. Each is cheap to compute, holds
// without anyone listening to the call, and points at a cause.
const (
	// The reply's first audio came more than SlowMs after the caller stopped.
	SignalSlow = "slow"
	// The caller got no reply at all: the agent failed, or every voice did.
	SignalSilent = "silent"
	// The caller said nearly the same thing again: misheard, or a wrong answer.
	SignalRepeat = "repeat"
	// "Hello? Are you there?" after the call has started: dead air.
	SignalCheckIn = "check_in"
	// The language went A → B → A: the switch to B was probably wrong.
	SignalLangFlip = "lang_flip"
	// Speech-to-text was unsure of what it heard.
	SignalLowConfidence = "low_confidence"
	// The caller cut in within CutEarlyMs of the reply starting: a false
	// barge-in, or a reply they knew at once was wrong.
	SignalCutEarly = "cut_early"
	// Call level: the caller hung up while the agent was talking.
	SignalHungUpMidReply = "hung_up_mid_reply"
)

const (
	SlowMs         = 3000
	CutEarlyMs     = 1500
	LowConfidence  = 0.6
	repeatMinWords = 3
	repeatOverlap  = 0.7
)

// checkIns are what callers say when they think the line has gone quiet. A
// short turn made mostly of these words is a check-in.
var checkIns = []string{
	"hello", "hallo", "hola", "allo", "allô", "alo", "ciao", "olá", "oi",
	"there", "you", "are", "can", "hear", "me", "anyone",
	"हेलो", "हैलो", "हलो", "सुन", "रहे", "हो", "हैं", "क्या",
}

func signals(t Turn, words, prev []string, langs []string) []string {
	var out []string
	if t.Spoke && t.Ms != nil && t.Ms.Total > SlowMs {
		out = append(out, SignalSlow)
	}
	if !t.Spoke && !t.Cut && !t.Superseded {
		out = append(out, SignalSilent)
	}
	if similar(words, prev) {
		out = append(out, SignalRepeat)
	}
	if t.Turn > 1 && isCheckIn(words) {
		out = append(out, SignalCheckIn)
	}
	if n := len(langs); n >= 3 && langs[n-1] != "" && langs[n-1] == langs[n-3] && langs[n-2] != "" && langs[n-2] != langs[n-1] {
		out = append(out, SignalLangFlip)
	}
	if t.Confidence > 0 && t.Confidence < LowConfidence {
		out = append(out, SignalLowConfidence)
	}
	if t.Cut && t.CutAfterMs < CutEarlyMs {
		out = append(out, SignalCutEarly)
	}
	return out
}

func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r)
	})
}

// similar: both turns have a few words and share most of them.
func similar(a, b []string) bool {
	if len(a) < repeatMinWords || len(b) < repeatMinWords {
		return false
	}
	in := map[string]bool{}
	for _, w := range b {
		in[w] = true
	}
	union := map[string]bool{}
	shared := 0
	for _, w := range a {
		if in[w] && !union[w] {
			shared++
		}
		union[w] = true
	}
	for w := range in {
		union[w] = true
	}
	return float64(shared)/float64(len(union)) >= repeatOverlap
}

func isCheckIn(words []string) bool {
	if len(words) == 0 || len(words) > 5 {
		return false
	}
	known := 0
	hello := false
	for _, w := range words {
		for _, c := range checkIns {
			if w == c {
				known++
				break
			}
		}
		switch w {
		case "hello", "hallo", "hola", "allo", "allô", "alo", "हेलो", "हैलो", "हलो", "there", "hear", "सुन":
			hello = true
		}
	}
	return hello && known*2 >= len(words)+1
}
