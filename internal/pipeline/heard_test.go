package pipeline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// play records a sentence of n frames, of which sent reached the wire.
func play(l *playbackLedger, gen uint64, text string, n, sent int, done bool) {
	s := l.sentence(gen, text)
	if s == nil {
		return
	}
	s.frames.Add(int64(n))
	s.sent.Add(int64(sent))
	s.done.Store(done)
}

func TestHeardIsTheFullReplyWhenItAllPlayed(t *testing.T) {
	var l playbackLedger
	l.reset(1)
	play(&l, 1, "[lang:en] Your appointment is on Monday.", 50, 50, true)
	play(&l, 1, "Bring your ID.", 40, 40+heardLatencyFrames, true)
	if l.unheard() {
		t.Fatal("a reply that played to the end wasn't cut off")
	}
	if got := l.heard(); got != "Your appointment is on Monday. Bring your ID." {
		t.Fatalf("heard %q", got)
	}
}

// The case the old code got wrong: two sentences generated, the caller cut
// in a third of the way through the first.
func TestHeardStopsWhereTheCallerCutIn(t *testing.T) {
	var l playbackLedger
	l.reset(1)
	play(&l, 1, "I'm sorry, but I don't have access to real booking systems.", 100, 40, true)
	play(&l, 1, "I can only offer general information.", 80, 0, true)
	if !l.unheard() {
		t.Fatal("a reply cut off mid-sentence counts as unheard")
	}
	got := l.heard()
	if !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "I'm sorry") {
		t.Fatalf("heard %q", got)
	}
	if strings.Contains(got, "general information") || strings.Contains(got, "booking systems") {
		t.Fatalf("heard %q: includes words that never played", got)
	}
}

func TestHeardLeavesOutAudioStillInFlight(t *testing.T) {
	var l playbackLedger
	l.reset(1)
	// The first sentence just left the sender; its last ~200 ms hasn't reached the caller.
	play(&l, 1, "Your total is four thousand rupees.", 50, 50, true)
	play(&l, 1, "Shall I send the link?", 40, 0, true)
	got := l.heard()
	if got == "Your total is four thousand rupees." || got == "" {
		t.Fatalf("heard %q: the in-flight tail should be partial", got)
	}
}

func TestHeardCutsIndicTextOnAWordWithoutBreakingCharacters(t *testing.T) {
	var l playbackLedger
	l.reset(1)
	play(&l, 1, "[lang:hi] मुझे खेद है, लेकिन मेरे पास असली बुकिंग सिस्टम नहीं है।", 100, 55, true)
	got := l.heard()
	if !utf8.ValidString(got) {
		t.Fatalf("heard %q: invalid UTF-8", got)
	}
	if !strings.HasPrefix(got, "मुझे") || !strings.HasSuffix(got, "…") {
		t.Fatalf("heard %q", got)
	}
	words := strings.Fields(strings.TrimSuffix(got, "…"))
	full := strings.Fields("मुझे खेद है, लेकिन मेरे पास असली बुकिंग सिस्टम नहीं है।")
	for i, w := range words {
		if w != full[i] {
			t.Fatalf("heard %q: word %d %q is cut", got, i, w)
		}
	}
}

func TestHeardIsEmptyBeforeAnyAudio(t *testing.T) {
	var l playbackLedger
	l.reset(1)
	play(&l, 1, "Hello there.", 30, 5, false)
	if got := l.heard(); got != "" {
		t.Fatalf("heard %q before any audio reached the caller", got)
	}
}

func TestASupersededResponseRecordsNothing(t *testing.T) {
	var l playbackLedger
	l.reset(2)
	if l.sentence(1, "stale") != nil {
		t.Fatal("an old response's sentence was recorded")
	}
}
