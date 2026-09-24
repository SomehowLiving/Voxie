package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/llm"
)

// fakeGreeterClient is an llm.Client that also implements llm.Greeter.
// Only Greeting is exercised; the embedded nil Client panics if anything
// else is called, which is the point -- resolveGreeting must not touch it.
type fakeGreeterClient struct {
	llm.Client
	text string
	err  error
}

func (f fakeGreeterClient) Greeting(context.Context) (string, error) { return f.text, f.err }

// plainClient implements llm.Client but not llm.Greeter.
type plainClient struct{ llm.Client }

func TestResolveGreeting_UsesAgentGreetingWhenEnabled(t *testing.T) {
	got := resolveGreeting(context.Background(), true,
		fakeGreeterClient{text: "Hi Arjun, this is REX about your failed payment."}, "static hello")
	if got != "Hi Arjun, this is REX about your failed payment." {
		t.Fatalf("got %q, want the agent's greeting", got)
	}
}

func TestResolveGreeting_IgnoresAgentWhenDisabled(t *testing.T) {
	got := resolveGreeting(context.Background(), false, fakeGreeterClient{text: "agent line"}, "static hello")
	if got != "static hello" {
		t.Fatalf("got %q, want the static greeting when greeting_from_agent is off", got)
	}
}

func TestResolveGreeting_FallsBackOnAgentErrorOrEmpty(t *testing.T) {
	if got := resolveGreeting(context.Background(), true, fakeGreeterClient{err: errors.New("boom")}, "static hello"); got != "static hello" {
		t.Fatalf("agent error: got %q, want static fallback", got)
	}
	if got := resolveGreeting(context.Background(), true, fakeGreeterClient{text: "   "}, "static hello"); got != "static hello" {
		t.Fatalf("empty agent reply: got %q, want static fallback", got)
	}
	if got := resolveGreeting(context.Background(), true, fakeGreeterClient{err: errors.New("boom")}, ""); got != "" {
		t.Fatalf("agent error with no static greeting: got %q, want silence", got)
	}
}

func TestResolveGreeting_ClientWithoutGreeterUsesStatic(t *testing.T) {
	if got := resolveGreeting(context.Background(), true, plainClient{}, "static hello"); got != "static hello" {
		t.Fatalf("got %q, want static greeting for a client without llm.Greeter", got)
	}
}

// TestGreetingRace_FiresWhenCallerStaysSilent confirms the timer wins the
// race when nothing closes callerSpokeFirst -- the "introduce yourself if
// the caller doesn't speak first" behavior this feature exists for.
func TestGreetingRace_FiresWhenCallerStaysSilent(t *testing.T) {
	ctx := context.Background()
	callerSpokeFirst := make(chan struct{}) // never closed

	should := greetingRace(ctx, callerSpokeFirst, 20*time.Millisecond)
	if !should {
		t.Fatal("greetingRace returned false with the caller silent -- greeting should have fired")
	}
}

// TestGreetingRace_SkipsWhenCallerSpeaksFirst confirms that a
// callerSpokeFirst already closed before the delay elapses cancels the
// greeting outright and returns promptly, rather than waiting out the
// delay regardless.
func TestGreetingRace_SkipsWhenCallerSpeaksFirst(t *testing.T) {
	ctx := context.Background()
	callerSpokeFirst := make(chan struct{})
	close(callerSpokeFirst)

	start := time.Now()
	should := greetingRace(ctx, callerSpokeFirst, 5*time.Second)
	elapsed := time.Since(start)

	if should {
		t.Fatal("greetingRace returned true even though the caller spoke first -- greeting should have been skipped")
	}
	if elapsed > time.Second {
		t.Fatalf("greetingRace took %s -- expected it to return almost immediately on an already-closed channel, not wait out the 5s delay", elapsed)
	}
}

// TestGreetingRace_SkipsOnShutdown confirms pipeline shutdown also cancels
// a pending greeting rather than leaving it to fire into a dead session.
func TestGreetingRace_SkipsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callerSpokeFirst := make(chan struct{})

	should := greetingRace(ctx, callerSpokeFirst, 5*time.Second)
	if should {
		t.Fatal("greetingRace returned true after context cancellation -- greeting should have been skipped")
	}
}

// TestCloseCallerSpokeFirst_SafeOnRepeatedCalls is the regression sync.Once
// exists to prevent: runAgent's real call site closes callerSpokeFirst on
// every real (non-passive) turn, and a second turn arriving must not
// attempt to close an already-closed channel, which panics.
func TestCloseCallerSpokeFirst_SafeOnRepeatedCalls(t *testing.T) {
	callerSpokeFirst := make(chan struct{})
	var once sync.Once
	closeIt := func() { once.Do(func() { close(callerSpokeFirst) }) }

	closeIt()
	select {
	case <-callerSpokeFirst:
	default:
		t.Fatal("channel was not closed on first call")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("closing callerSpokeFirst a second time panicked: %v", r)
		}
	}()
	closeIt()
}

// --- awaitCallerWords: the caller is audibly mid-utterance when the delay
// elapses, but their finished turn hasn't reached runAgent yet. This is the
// case the live test caught: "Hello? Who is this?" said one second after
// pickup only became a transcript six seconds later.

func TestAwaitCallerWords_NoSoundGreetsImmediately(t *testing.T) {
	start := time.Now()
	skip := awaitCallerWords(context.Background(), make(chan struct{}),
		func() time.Time { return time.Time{} }, time.Second, 5*time.Second, 10*time.Millisecond)
	if skip {
		t.Fatal("no caller sound at all -- should greet")
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("waited despite hearing nothing")
	}
}

func TestAwaitCallerWords_WaitsForTheTurnOfACallerStillTalking(t *testing.T) {
	turn := make(chan struct{})
	heard := time.Now()
	go func() { time.Sleep(80 * time.Millisecond); close(turn) }()
	skip := awaitCallerWords(context.Background(), turn,
		func() time.Time { return heard }, time.Second, 5*time.Second, 10*time.Millisecond)
	if !skip {
		t.Fatal("caller's turn arrived while we were holding -- greeting should be skipped")
	}
}

func TestAwaitCallerWords_NoiseWithoutWordsStillGetsAGreeting(t *testing.T) {
	heard := time.Now() // one burst of sound, then nothing
	start := time.Now()
	skip := awaitCallerWords(context.Background(), make(chan struct{}),
		func() time.Time { return heard }, 150*time.Millisecond, 5*time.Second, 10*time.Millisecond)
	if skip {
		t.Fatal("sound but no words -- must greet rather than leave dead air")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("gave up after %s, want roughly the settle window", elapsed)
	}
}

func TestAwaitCallerWords_ContinuousNoiseHitsTheCap(t *testing.T) {
	start := time.Now()
	skip := awaitCallerWords(context.Background(), make(chan struct{}),
		time.Now, time.Second, 150*time.Millisecond, 10*time.Millisecond)
	if skip {
		t.Fatal("continuous noise with no turn -- must greet at the cap")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("held for %s, past the cap", elapsed)
	}
}
