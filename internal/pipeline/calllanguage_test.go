package pipeline

import "testing"

func TestCallLanguage(t *testing.T) {
	type turn struct{ lang, text, want string }
	cases := map[string][]turn{
		// Live, 2026-10-27: a Kannada call, with English words dropped in.
		"Kannada with English words": {
			{"kn", "ಕನ್ನಡದಲ್ಲಿ ಮಾತಾಡು.", "kn"},
			{"en", "Okay", "kn"},
			{"kn", "ಬೇಡ ಸ್ವಲ್ಪ", "kn"},
			{"en", "Oh sari, thank you", "kn"},
			{"en", "Okay. Thank you.", "kn"},
		},
		"a real sentence switches": {
			{"kn", "ನನಗೆ ಸ್ವಲ್ಪ ಬುಕ್ಕಿಂಗ್ ಮಾಡಬೇಕಿತ್ತು.", "kn"},
			{"en", "Can you speak English with me?", "en"},
		},
		"three short turns in a row switch": {
			{"hi", "मुझे कल का appointment चाहिए", "hi"},
			{"en", "Yes", "hi"},
			{"en", "Tomorrow morning", "hi"},
			{"en", "Okay great", "en"},
		},
		"a turn in the call's language resets the streak": {
			{"hi", "मुझे कल का appointment चाहिए", "hi"},
			{"en", "Tomorrow morning", "hi"},
			{"en", "Okay great", "hi"},
			{"hi", "हाँ ठीक है", "hi"},
			{"en", "Yes", "hi"},
			{"en", "Okay", "hi"},
		},
		"a turn with no language keeps the call's": {
			{"ta", "நாளைக்கு எத்தனை மணிக்கு திறப்பீங்க?", "ta"},
			{"", "Okay, cool. Bye.", "ta"},
		},
		"Chinese counts characters": {
			{"en", "Hello, who is this?", "en"},
			{"zh", "谢谢", "en"},
			{"zh", "你能告诉我明天几点开门吗", "zh"},
		},
		"the first turn sets it, however short": {
			{"es", "Hola", "es"},
		},
	}
	for name, turns := range cases {
		t.Run(name, func(t *testing.T) {
			var c callLanguage
			for i, tr := range turns {
				if got := c.observe(tr.lang, tr.text); got != tr.want {
					t.Fatalf("turn %d (%s %q): call language %q, want %q", i+1, tr.lang, tr.text, got, tr.want)
				}
			}
		})
	}
}

// Asked to switch, the agent answers in Japanese: the call is in Japanese,
// and a short Japanese reply keeps it there.
func TestCallLanguageFollowsTheAgentsReply(t *testing.T) {
	var c callLanguage
	c.observe("en", "Can you speak in Japanese with me?")
	c.replied("ja")
	if got := c.observe("ja", "はい"); got != "ja" {
		t.Fatalf("got %q", got)
	}
	c.replied("") // a reply with no tag changes nothing
	if c.get() != "ja" {
		t.Fatal("an untagged reply moved the call")
	}
}
