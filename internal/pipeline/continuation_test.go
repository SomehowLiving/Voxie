package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/llm"
)

// thinkingLLM records each turn and takes `think` to answer, so a second
// turn can land while the first is still being answered.
type thinkingLLM struct {
	think time.Duration
	mu    sync.Mutex
	turns []llm.Turn
}

func (l *thinkingLLM) Chat(ctx context.Context, turn llm.Turn, onChunk func(string), onSentence func(string)) (string, error) {
	l.mu.Lock()
	l.turns = append(l.turns, turn)
	l.mu.Unlock()
	select {
	case <-time.After(l.think):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	onChunk("Okay.")
	onSentence("Okay.")
	return "Okay.", nil
}
func (l *thinkingLLM) OneShot(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("unexpected OneShot call")
}
func (l *thinkingLLM) SetTools([]llm.ToolDefinition)                                      {}
func (l *thinkingLLM) SetToolHandler(func(context.Context, llm.ToolCall) (string, error)) {}
func (l *thinkingLLM) AppendSystemPrompt(string)                                          {}
func (l *thinkingLLM) Reset()                                                             {}

func (l *thinkingLLM) waitTurns(t *testing.T, n int) []llm.Turn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		if len(l.turns) >= n {
			out := append([]llm.Turn(nil), l.turns...)
			l.mu.Unlock()
			return out
		}
		l.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d turns", n)
	return nil
}

func newAgentPipeline(t *testing.T, model llm.Client) *Pipeline {
	t.Helper()
	p, cancel := newLifecyclePipeline(t, nil, nil)
	t.Cleanup(cancel)
	p.llmClient = model
	p.ttsClient = &fakeTTS{}
	p.transcriptCh = make(chan TranscriptEvent, 4)
	p.interruptCh = make(chan struct{}, 1)
	p.callerSpokeFirst = make(chan struct{})
	go p.runAgent()
	return p
}

func TestTurnThatGoesOnBeforeTheReplyIsHeardIsAnsweredWhole(t *testing.T) {
	model := &thinkingLLM{think: 500 * time.Millisecond}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "En fait, je préfère parler français.", Final: true, Language: "fr"}
	model.waitTurns(t, 1)
	p.transcriptCh <- TranscriptEvent{Text: "Quand va-t-il arriver ?", Final: true, Language: "fr"}
	turns := model.waitTurns(t, 2)
	got := turns[1]
	if got.Text != "En fait, je préfère parler français. Quand va-t-il arriver ?" {
		t.Fatalf("second request text = %q, want both parts", got.Text)
	}
	if !got.ReplacesLastTurn {
		t.Error("the app must be told the request replaces the last turn")
	}
	if got.InterruptedText != "" || strings.Contains(got.Prompt, "interrupted") {
		t.Errorf("not an interruption: nothing was heard, got %+v", got)
	}
	if entries := p.transcriptLog.Entries(); len(entries) == 0 || entries[len(entries)-1].Text != got.Text {
		t.Errorf("transcript should hold the whole turn once, got %+v", entries)
	}
}

func TestTurnAfterTheReplyWasHeardIsNotJoined(t *testing.T) {
	model := &thinkingLLM{}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "What time do you open?", Final: true}
	model.waitTurns(t, 1)
	// The reply played.
	gen := p.responseGen.Load()
	seg := p.ledger.sentence(gen, "We open at nine.")
	if seg == nil {
		p.ledger.reset(gen)
		seg = p.ledger.sentence(gen, "We open at nine.")
	}
	seg.frames.Store(50)
	seg.sent.Store(50)
	seg.done.Store(true)
	p.transcriptCh <- TranscriptEvent{Text: "And on Sunday?", Final: true}
	if got := model.waitTurns(t, 2)[1]; got.Text != "And on Sunday?" || got.ReplacesLastTurn {
		t.Fatalf("got %+v; a turn after a heard reply is a turn of its own", got)
	}
}

func TestTurnLongAfterIsNotJoined(t *testing.T) {
	model := &thinkingLLM{}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "Hello.", Final: true}
	model.waitTurns(t, 1)
	p.lastTurn.at = time.Now().Add(-continuationWindow - time.Second)
	p.transcriptCh <- TranscriptEvent{Text: "Hello?", Final: true}
	if got := model.waitTurns(t, 2)[1]; got.ReplacesLastTurn {
		t.Fatalf("got %+v", got)
	}
}

// playReply marks frames of the current reply as played.
func playReply(p *Pipeline, frames int64) {
	gen := p.responseGen.Load()
	p.ledger.reset(gen)
	seg := p.ledger.sentence(gen, "Sure, I can help with that.")
	seg.frames.Store(frames)
	seg.sent.Store(frames)
}

func TestCallerWhoCutsInOnTheFirstWordAndFinishesIsAnsweredWhole(t *testing.T) {
	model := &thinkingLLM{}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "I want to book a table.", Final: true}
	model.waitTurns(t, 1)
	playReply(p, 30) // "Sure, I…"
	cutIn(t, p)
	p.transcriptCh <- TranscriptEvent{Text: "For four people tomorrow at eight.", Final: true}
	got := model.waitTurns(t, 2)[1]
	if got.Text != "I want to book a table. For four people tomorrow at eight." || !got.ReplacesLastTurn {
		t.Fatalf("got %+v", got)
	}
	if got.InterruptedText != "" {
		t.Errorf("interrupted_text = %q; a word or two of the reply isn't an interruption to report", got.InterruptedText)
	}
}

func TestCallerWhoHeardTheReplyAndCutInIsAnInterruption(t *testing.T) {
	model := &thinkingLLM{}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "What are your opening hours?", Final: true}
	model.waitTurns(t, 1)
	playReply(p, 150) // three seconds of the reply
	cutIn(t, p)
	p.transcriptCh <- TranscriptEvent{Text: "Wait, what about Sunday?", Final: true}
	got := model.waitTurns(t, 2)[1]
	if got.Text != "Wait, what about Sunday?" || got.ReplacesLastTurn {
		t.Fatalf("got %+v", got)
	}
	if got.InterruptedText == "" {
		t.Error("the app should hear what the caller heard before cutting in")
	}
}

// cutIn barges in and waits until runAgent has handled it.
func cutIn(t *testing.T, p *Pipeline) {
	t.Helper()
	before := p.responseGen.Load()
	p.interruptCh <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for p.responseGen.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("barge-in not handled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Live: "Mi número de pedido es" / "cuatro." -- the rest of the sentence
// landed on the reply's first words, with no barge-in before it.
func TestRestOfTheSentenceLandingOnTheReplysFirstWordsIsJoined(t *testing.T) {
	model := &thinkingLLM{}
	p := newAgentPipeline(t, model)
	p.transcriptCh <- TranscriptEvent{Text: "Mi número de pedido es", Final: true, Language: "es"}
	model.waitTurns(t, 1)
	playReply(p, 25) // "Lo siento," and still playing
	p.transcriptCh <- TranscriptEvent{Text: "cuatro, cinco, seis, siete.", Final: true, Language: "es"}
	got := model.waitTurns(t, 2)[1]
	if got.Text != "Mi número de pedido es cuatro, cinco, seis, siete." || !got.ReplacesLastTurn || got.InterruptedText != "" {
		t.Fatalf("got %+v", got)
	}
}
