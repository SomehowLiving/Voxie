package llm

import (
	"strings"
	"testing"
)

func TestSplitSentencesCutsAtEachEnder(t *testing.T) {
	cases := map[string][]string{
		"I'll keep it active until Monday. I'm sending the link now. Anything else?": {
			"I'll keep it active until Monday. ", "I'm sending the link now. ", "Anything else?",
		},
		"मैं खाता सक्रिय रखूंगा। मैं लिंक भेज रहा हूँ। और कुछ?": {
			"मैं खाता सक्रिय रखूंगा। ", "मैं लिंक भेज रहा हूँ। ", "और कुछ?",
		},
		"お支払いが完了しませんでした。少しお時間いただけますか？": {"お支払いが完了しませんでした。", "少しお時間いただけますか？"},
		// Not a sentence end: an email, a decimal, an amount.
		"Write to help@example.com about the 4.5% fee of ₹4.999 today.": {"Write to help@example.com about the 4.5% fee of ₹4.999 today."},
		"No ender at all": {"No ender at all"},
	}
	for in, want := range cases {
		got := splitSentences(in)
		if strings.Join(got, "") != in {
			t.Errorf("splitSentences(%q) lost text: %q", in, got)
		}
		if len(got) != len(want) {
			t.Errorf("splitSentences(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitSentences(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestFindSentenceEndKnowsTheDandaAndCJKEnders(t *testing.T) {
	s := "मैं खाता सक्रिय रखूंगा। मैं लिंक"
	end := findSentenceEnd(s)
	if end < 0 || s[:end+1] != "मैं खाता सक्रिय रखूंगा।" {
		t.Fatalf("findSentenceEnd(%q) = %d, want the index ending at the danda", s, end)
	}
	if end := findSentenceEnd("完了しました。次"); end < 0 {
		t.Fatal("the CJK full stop must end a sentence")
	}
	if end := findSentenceEnd("help@example.com"); end >= 0 {
		t.Fatal("a dot inside an email is not a sentence end")
	}
}

func TestJSONReplyIsEmittedSentenceBySentence(t *testing.T) {
	var got []string
	_, err := readJSONReply(strings.NewReader(`{"text":"First. Second। Third?"}`), func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("emitted %q, want three sentences", got)
	}
}
