package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/tts"
)

// stallingTTS never sends anything for a sentence containing "stuck" (until
// cancelled), and one frame of audio for any other.
type stallingTTS struct {
	cancelled chan struct{}
}

func (s *stallingTTS) Synthesize(context.Context, string) ([]byte, error) { return nil, nil }
func (s *stallingTTS) SynthesizeStream(ctx context.Context, text string) (<-chan tts.StreamChunk, error) {
	ch := make(chan tts.StreamChunk, 1)
	if strings.Contains(text, "stuck") {
		go func() {
			<-ctx.Done()
			close(s.cancelled)
			close(ch)
		}()
		return ch, nil
	}
	ch <- tts.StreamChunk{PCM: make([]byte, 640)}
	close(ch)
	return ch, nil
}

func TestStalledVoiceSkipsTheSentence(t *testing.T) {
	old := ttsStallTimeout
	ttsStallTimeout = 150 * time.Millisecond
	defer func() { ttsStallTimeout = old }()

	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	voice := &stallingTTS{cancelled: make(chan struct{})}
	p.ttsClient = voice

	sentences := make(chan string, 2)
	sentences <- "This one is stuck."
	sentences <- "This one plays."
	close(sentences)
	done := make(chan struct{})
	go func() { p.synthesizeSentences(p.ctx, 0, sentences, time.Time{}); close(done) }()

	select {
	case <-p.outPCMCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the sentence after a stalled one never played")
	}
	select {
	case <-voice.cancelled:
	case <-time.After(time.Second):
		t.Fatal("the stalled request was not cancelled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("synthesizeSentences did not finish")
	}
}

// oneFrameTTS returns one frame of audio for any sentence.
type oneFrameTTS struct{}

func (oneFrameTTS) Synthesize(context.Context, string) ([]byte, error) { return nil, nil }
func (oneFrameTTS) SynthesizeStream(context.Context, string) (<-chan tts.StreamChunk, error) {
	ch := make(chan tts.StreamChunk, 1)
	ch <- tts.StreamChunk{PCM: make([]byte, 640)}
	close(ch)
	return ch, nil
}

func firstAudioAfter(t *testing.T, p *Pipeline) time.Duration {
	t.Helper()
	p.ttsClient = oneFrameTTS{}
	sentences := make(chan string, 1)
	sentences <- "Sure."
	close(sentences)
	start := time.Now()
	go p.synthesizeSentences(p.ctx, 0, sentences, time.Time{})
	select {
	case <-p.outPCMCh:
		return time.Since(start)
	case <-time.After(5 * time.Second):
		t.Fatal("no audio")
		return 0
	}
}

func TestReplyWaitsForATalkingCallerToPause(t *testing.T) {
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	stop := time.Now().Add(600 * time.Millisecond)
	go func() {
		for time.Now().Before(stop) {
			p.lastCallerSoundAt.Store(time.Now().UnixNano())
			time.Sleep(50 * time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if d := firstAudioAfter(t, p); d < 600*time.Millisecond+staleReplyQuiet-100*time.Millisecond {
		t.Fatalf("reply started after %s; the caller's final hadn't had time to arrive", d)
	}
}

func TestReplyHoldIsBounded(t *testing.T) {
	old := replyHoldMax
	replyHoldMax = 300 * time.Millisecond
	defer func() { replyHoldMax = old }()
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				p.lastCallerSoundAt.Store(time.Now().UnixNano())
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	time.Sleep(30 * time.Millisecond)
	if d := firstAudioAfter(t, p); d > replyHoldMax+300*time.Millisecond {
		t.Fatalf("held %s on a line that never goes quiet", d)
	}
}

func TestReplyToASilentCallerIsNotHeld(t *testing.T) {
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	p.lastCallerSoundAt.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if d := firstAudioAfter(t, p); d > 100*time.Millisecond {
		t.Fatalf("held %s", d)
	}
}

// The VAD reads a gap between words as a pause; partial words still mean
// the caller is talking.
func TestReplyWaitsWhilePartialWordsArrive(t *testing.T) {
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	stop := time.Now().Add(700 * time.Millisecond)
	go func() {
		for time.Now().Before(stop) {
			p.lastPartialAt.Store(time.Now().UnixNano())
			time.Sleep(300 * time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if d := firstAudioAfter(t, p); d < 700*time.Millisecond+staleReplyQuiet-100*time.Millisecond {
		t.Fatalf("reply started after %s while words were still arriving", d)
	}
}

// The caller's words become a turn during the hold: the held reply is
// dropped, never played.
func TestHeldReplyIsDroppedWhenTheCallersTurnArrives(t *testing.T) {
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	p.lastCallerSoundAt.Store(time.Now().UnixNano())
	ctx, stop := context.WithCancel(p.ctx)
	go func() { time.Sleep(300 * time.Millisecond); stop() }() // superseded
	if p.waitForCallerPause(ctx) {
		t.Fatal("a superseded reply was let through")
	}
}

// midStreamTTS sends one frame, then nothing until cancelled.
type midStreamTTS struct{ cancelled chan struct{} }

func (m *midStreamTTS) Synthesize(context.Context, string) ([]byte, error) { return nil, nil }
func (m *midStreamTTS) SynthesizeStream(ctx context.Context, text string) (<-chan tts.StreamChunk, error) {
	ch := make(chan tts.StreamChunk, 1)
	ch <- tts.StreamChunk{PCM: make([]byte, 640)}
	go func() { <-ctx.Done(); close(m.cancelled); close(ch) }()
	return ch, nil
}

func TestVoiceThatStopsMidSentenceIsCutAfterTheGapTimeout(t *testing.T) {
	oldGap := ttsGapTimeout
	ttsGapTimeout = 150 * time.Millisecond
	defer func() { ttsGapTimeout = oldGap }()
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	voice := &midStreamTTS{cancelled: make(chan struct{})}
	p.ttsClient = voice
	sentences := make(chan string, 1)
	sentences <- "Puedo ayudarle con otra cosa?"
	close(sentences)
	go p.synthesizeSentences(p.ctx, 0, sentences, time.Time{})
	go func() {
		for range p.outPCMCh {
		}
	}()
	select {
	case <-voice.cancelled:
	case <-time.After(time.Second):
		t.Fatal("a voice stuck mid-sentence wasn't cut after the gap timeout")
	}
}
