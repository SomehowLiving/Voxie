package pipeline

import "testing"

func TestSpeakable(t *testing.T) {
	for s, want := range map[string]bool{")": false, "[lang:hi] ।": false, `"`: false, "…": false, "OK.": true, "[lang:hi] नमस्ते": true, "4.": true} {
		if speakable(s) != want {
			t.Errorf("speakable(%q) = %v, want %v", s, !want, want)
		}
	}
}
