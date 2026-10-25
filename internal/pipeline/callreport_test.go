package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/callreport"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

// A reply the caller cut into is recorded as cut, with how long they heard it;
// one their next words replaced before it played is superseded, not silent.
func TestReportTurnCutAndSuperseded(t *testing.T) {
	dir := t.TempDir()
	w, err := callreport.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	callreport.SetDefault(w)
	defer callreport.SetDefault(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Pipeline{ctx: ctx, cancel: cancel, cfg: &config.Config{}, report: callreport.NewCall("s1", false)}

	heard := newTurnClock(TranscriptEvent{TurnStart: time.Now(), Provider: "deepgram"}, "en")
	p.responseGen.Store(1)
	p.clocks.Store(uint64(1), heard)
	heard.voice.Store("cartesia")
	heard.firstAudio()
	time.Sleep(20 * time.Millisecond)
	p.markCut(true)
	p.reportTurn(heard, "tell me about the menu", "en", nil, true)

	unheard := newTurnClock(TranscriptEvent{TurnStart: time.Now()}, "en")
	p.responseGen.Store(2)
	p.clocks.Store(uint64(2), unheard)
	p.markCut(true) // the caller went on before any audio: not a cut
	p.reportTurn(unheard, "I want to", "en", nil, true)

	sum := p.report.End("caller", false)
	b, _ := os.ReadFile(filepath.Join(dir, "calls-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), b)
	}
	if !strings.Contains(lines[0], `"cut":true`) || !strings.Contains(lines[0], `"voice":"cartesia"`) || !strings.Contains(lines[0], `"listener":"deepgram"`) {
		t.Fatalf("cut turn: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"superseded":true`) || strings.Contains(lines[1], "silent") || strings.Contains(lines[1], `"cut"`) {
		t.Fatalf("superseded turn: %s", lines[1])
	}
	if sum.Turns != 2 || sum.Spoken != 1 {
		t.Fatalf("summary %+v", sum)
	}
}
