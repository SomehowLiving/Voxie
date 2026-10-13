package pipeline

import "testing"

func TestEchoOfTheAgentsOwnWords(t *testing.T) {
	agent := "I can help you check your booking status, change dates or rooms, and cancel a reservation."
	for _, c := range []struct {
		heard string
		echo  bool
	}{
		{"I can help you check your booking status.", true}, // live: came back as a caller turn
		{"change dates or rooms", true},
		{"Wait, stop. Just tell me your opening hours.", false},
		{"No, I want to cancel my booking tomorrow", false}, // shares words, but mostly the caller's
		{"booking", false}, // one word proves nothing
		{"okay", false},
	} {
		if got := isEchoOfAgent(c.heard, agent); got != c.echo {
			t.Errorf("%q: echo=%v, want %v", c.heard, got, c.echo)
		}
	}
	if isEchoOfAgent("I can help you", "") {
		t.Error("nothing said, nothing to echo")
	}
}

func TestEchoInHindi(t *testing.T) {
	agent := "हाँ, मैं हिंदी में बात कर सकता हूँ। मैं आपकी कैसे मदद कर सकता हूँ?"
	if !isEchoOfAgent("मैं हिंदी में बात कर सकता हूं", agent) {
		t.Error("the agent's Hindi coming back is an echo (both nasal spellings)")
	}
	if isEchoOfAgent("मुझे अपने बिल के बारे में जानना है", agent) {
		t.Error("the caller's own Hindi is not an echo")
	}
}

func TestInterruptionsInOtherLanguagesCutIn(t *testing.T) {
	for _, s := range []string{"sorry to cut in", "excuse me", "रुकिए", "नहीं नहीं", "இருங்க", "ఆగండి", "ಬೇಡ", "待って", "잠깐만요", "подождите", "czekaj"} {
		if !isMeaningfulBargeInTranscript(s) {
			t.Errorf("%q should cut the agent off", s)
		}
	}
	for _, s := range []string{"हां", "अच्छा", "சரி", "はい", "yeah okay"} {
		if isMeaningfulBargeInTranscript(s) {
			t.Errorf("%q is a backchannel, not an interruption", s)
		}
	}
}
