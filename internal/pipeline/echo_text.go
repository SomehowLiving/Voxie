package pipeline

import "strings"

// echoTextShare is how much of a transcript must be the agent's own recent
// words for it to count as the agent's voice coming back.
const echoTextShare = 0.7

// echoGrace is how long after the agent stops that its echo can still turn
// up as a transcript (speech-to-text lags the audio).
const echoGraceSeconds = 2.5

// isEchoOfAgent reports whether a transcript is mostly the agent's own words:
// its voice leaking back through a speakerphone and transcribed as the
// caller. Live, with the agent's audio fed back at -12 dB, "I can help you
// check your booking status" came back as a caller turn and the agent
// answered itself. Loudness can't tell that echo from a caller (the echo
// guard's bound either blocks quiet callers or lets echo through); the words
// can, because a caller says something else.
//
// Needs two or more words: a lone "okay" or "booking" proves nothing.
func isEchoOfAgent(transcript, agentSaid string) bool {
	words := strings.Fields(normalizedTranscriptText(transcript))
	if len(words) < 2 || agentSaid == "" {
		return false
	}
	said := map[string]bool{}
	for _, w := range strings.Fields(normalizedTranscriptText(agentSaid)) {
		said[w] = true
	}
	match := 0
	for _, w := range words {
		if said[w] {
			match++
		}
	}
	return float64(match) >= echoTextShare*float64(len(words))
}

// agentRecentText is what the agent has been saying in the current (or just
// finished) response, for telling its echo from the caller.
func (p *Pipeline) agentRecentText() string {
	txt, _ := p.lastAgentText.Load().(string)
	return txt
}
