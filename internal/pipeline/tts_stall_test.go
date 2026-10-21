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
