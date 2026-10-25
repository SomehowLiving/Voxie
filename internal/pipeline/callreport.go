package pipeline

import (
	"time"

	"github.com/streamcoreai/streamcore-server/internal/callreport"
)

// markCut notes that the caller cut into the reply now playing. With
// onlyIfSpoken, a reply nobody heard yet isn't cut but superseded: the
// caller simply went on talking.
func (p *Pipeline) markCut(onlyIfSpoken bool) {
	c := p.clockFor(p.responseGen.Load())
	if c == nil || (onlyIfSpoken && !c.logged.Load()) {
		return
	}
	c.cutAt.CompareAndSwap(0, time.Now().UnixNano())
}

// reportTurn records a finished reply in the call report. cancelled: the
// reply was stopped before it finished (cut off, superseded, or the call
// ended).
func (p *Pipeline) reportTurn(clock *turnClock, text, language string, agentErr error, cancelled bool) {
	if p.report == nil {
		return
	}
	in := callreport.TurnInput{Text: text, Lang: language, AgentError: agentErr != nil && !cancelled}
	if clock != nil {
		in.Listener = clock.provider
		in.Confidence = clock.confidence
		in.Ms = clock.stages.Load()
		in.Approx = clock.approx.Load()
		in.Spoke = clock.logged.Load()
		in.Voice, _ = clock.voice.Load().(string)
		in.ReplyLang, _ = clock.replyLang.Load().(string)
		if cut := clock.cutAt.Load(); cut != 0 {
			in.Cut = true
			if first := clock.firstAudioAt.Load(); first != 0 && cut > first {
				in.CutAfterMs = (cut - first) / int64(time.Millisecond)
			}
		}
	}
	// Ended early but not cut: the caller's next words replaced it, or the
	// call ended. Neither is a reply the caller went without.
	in.Superseded = cancelled && !in.Cut
	if in.Spoke && in.Voice == "" {
		in.Voice = p.cfg.TTS.Provider // a single voice, not a failover chain
	}
	p.report.Turn(in)
}
