package pipeline

import (
	"testing"
	"time"
)

func TestTurnClockIgnoresAStaleSpeechEnd(t *testing.T) {
	now := time.Now()
	c := newTurnClock(TranscriptEvent{TurnStart: now.Add(-time.Second), SpeechEndAt: now.Add(-20 * time.Second)}, "en")
	c.firstAudio() // must not panic, and logs from the final
	if !c.logged.Load() {
		t.Fatal("not logged")
	}
	c.firstAudio() // only once
}
