package pipeline

import (
	"testing"
	"time"
)

func TestMergeWait(t *testing.T) {
	const base = 350 * time.Millisecond

	cases := []struct {
		name string
		text string
		want time.Duration
	}{
		// Terminal punctuation beats a pronoun tail: the provider already judged
		// the thought complete, so the tail word is not evidence of a pause.
		{"punctuated sentence ending on a pronoun", "I already fixed it.", base},
		{"punctuated question", "can you check it?", base},
		{"punctuated exclamation", "that worked!", base},

		// The case the tail list exists for — same words, no closer.
		{"unpunctuated pronoun tail", "I already fixed it", turnMergeIncomplete},
		{"dangling conjunction", "I tried that and", turnMergeIncomplete},
		{"filler tail", "so it was, um", turnMergeIncomplete},

		// Dictation still extends: callers pause much longer between spelled
		// segments than between words.
		{"mid-dictation separator", "my email is gary dot", turnMergeIncomplete},
		{"spelled letters", "it's g a r", turnMergeIncomplete},
		{"digit run", "the number is 0 4 1 2", turnMergeIncomplete},
		{"completed address is not mid-dictation", "gary@example.com.", base},

		{"ordinary finished clause", "yes that's right", base},
		{"empty", "", base},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeWait(c.text, base); got != c.want {
				t.Errorf("mergeWait(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// Live: "Mi número de pedido es", a 1.5s pause, "cuatro, cinco, seis,
// siete." -- the first half was answered. The caller is audibly speaking
// again when the wait runs out, so the turn waits for the rest.
func TestTurnBufferWaitsForTheRestOfAnUnfinishedSentence(t *testing.T) {
	p := newTurnBufferPipeline(t)
	p.finalCh <- TranscriptEvent{Text: "Mi número de pedido es", Final: true, Language: "es"}
	time.Sleep(1100 * time.Millisecond) // the pause
	for i := 0; i < 25; i++ {           // 2.5s of digits
		p.lastCallerSoundAt.Store(time.Now().UnixNano())
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	p.finalCh <- TranscriptEvent{Text: "cuatro, cinco, seis, siete.", Final: true, Language: "es"}
	select {
	case turn := <-p.transcriptCh:
		if turn.Text != "Mi número de pedido es cuatro, cinco, seis, siete." {
			t.Fatalf("turn = %q, want the whole sentence", turn.Text)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("turn buffer never emitted the turn")
	}
}

// Unfinished, but the caller stays silent: answered after the wait.
func TestTurnBufferAnswersAnUnfinishedSentenceIfTheCallerStaysSilent(t *testing.T) {
	p := newTurnBufferPipeline(t)
	start := time.Now()
	p.finalCh <- TranscriptEvent{Text: "明日の予約ですが、", Final: true, Language: "ja"}
	select {
	case <-p.transcriptCh:
		if waited := time.Since(start); waited > turnMergeIncomplete+400*time.Millisecond {
			t.Fatalf("held %v", waited)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("turn buffer never emitted the turn")
	}
}

func TestEndsIncompleteInOtherLanguages(t *testing.T) {
	for text, want := range map[string]bool{
		"Mi número de pedido es":              true,
		"Je voudrais réserver pour":           true,
		"Ich brauche eine Rückerstattung und": true,
		"मुझे दिल्ली के":                      true,
		"明日の予約ですが、":                           true,
		"明日の予約ですが":                            true,
		"시간이 있는데":                             true,
		"Ich komme mit":                       false, // separable particle
		"待って":                                 false,
		"No":                                  false,
		"Quand va-t-il arriver":               false,
		"मुझे एक टिकट बुक करना है": false,
	} {
		if got := endsIncomplete(text); got != want {
			t.Errorf("endsIncomplete(%q) = %v, want %v", text, got, want)
		}
	}
}
