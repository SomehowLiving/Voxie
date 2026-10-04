package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

func TestIsCheckIn(t *testing.T) {
	cases := map[string]bool{
		"Hello?":                               true,
		"hello, hello?":                        true,
		"हाँ hello":                            true,
		"हाँ hello hello,":                     true,
		"Are you there?":                       true,
		"Can you hear me?":                     true,
		"Allô ?":                               true,
		"喂":                                    true,
		"ஹலோ":                                  true,
		"Hello, I want to pay my bill":         false,
		"Hi. I have a question about my bill.": false,
		"yes":                                  false, // an acknowledgement, not a check-in
		"okay":                                 false,
		"":                                     false,
	}
	for text, want := range cases {
		if got := isCheckIn(text); got != want {
			t.Errorf("isCheckIn(%q) = %v, want %v", text, got, want)
		}
	}
}

func newTurnBufferPipeline(t *testing.T) *Pipeline {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := newResponseStatePipeline(nil)
	p.ctx = ctx
	p.cfg = &config.Config{}
	p.cfg.Pipeline.TurnMergeMs = 60
	p.finalCh = make(chan TranscriptEvent, transcriptChSize)
	p.transcriptCh = make(chan TranscriptEvent, transcriptChSize)
	go p.runTurnBuffer()
	return p
}

// Live: "हाँ hello", then 1.7s later "hello," -- answered twice. The caller
// was audibly saying it again, so the check-in waits and the two merge.
func TestTurnBufferMergesARepeatedHello(t *testing.T) {
	p := newTurnBufferPipeline(t)
	p.finalCh <- TranscriptEvent{Text: "हाँ hello", Final: true, Language: "hi"}

	// The caller speaks again from 0.8s to 1.6s; as live, the final lands
	// ~450ms after the sound stops.
	time.Sleep(800 * time.Millisecond)
	for i := 0; i < 8; i++ {
		p.lastCallerSoundAt.Store(time.Now().UnixNano())
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(450 * time.Millisecond)
	p.finalCh <- TranscriptEvent{Text: "hello,", Final: true, Language: "hi"}

	select {
	case turn := <-p.transcriptCh:
		if turn.Text != "हाँ hello hello," {
			t.Fatalf("turn = %q, want both hellos in one turn", turn.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn buffer never emitted the turn")
	}
}

// A lone "hello?" with silence after it is answered after checkInWait.
func TestTurnBufferAnswersALoneHelloAfterTheCheckInWait(t *testing.T) {
	p := newTurnBufferPipeline(t)
	start := time.Now()
	p.finalCh <- TranscriptEvent{Text: "Hello?", Final: true}
	select {
	case <-p.transcriptCh:
		if waited := time.Since(start); waited < checkInWait || waited > checkInWait+500*time.Millisecond {
			t.Fatalf("answered after %v, want about %v", waited, checkInWait)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn buffer never emitted the turn")
	}
}

// A request that starts with a greeting is not held for the check-in wait.
func TestTurnBufferDoesNotHoldARequestThatStartsWithHello(t *testing.T) {
	p := newTurnBufferPipeline(t)
	start := time.Now()
	p.finalCh <- TranscriptEvent{Text: "Hello, I want to pay my bill.", Final: true}
	select {
	case <-p.transcriptCh:
		if waited := time.Since(start); waited >= checkInWait/2 {
			t.Fatalf("a request waited %v", waited)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn buffer never emitted the turn")
	}
}

// A check-in is never held past turnMergeMax, even if sound never stops
// (a noisy line).
func TestTurnBufferCheckInHoldIsBounded(t *testing.T) {
	p := newTurnBufferPipeline(t)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				p.lastCallerSoundAt.Store(time.Now().UnixNano())
			}
		}
	}()
	start := time.Now()
	p.finalCh <- TranscriptEvent{Text: "Hello?", Final: true}
	select {
	case <-p.transcriptCh:
		if waited := time.Since(start); waited > turnMergeMax+300*time.Millisecond {
			t.Fatalf("held %v, past the %v cap", waited, turnMergeMax)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("check-in held forever on a noisy line")
	}
}
