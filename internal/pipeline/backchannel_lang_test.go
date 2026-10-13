package pipeline

import "testing"

// Backchannels as speech-to-text writes them, in each language Voxie hears.
// Live, "हां," "अच्छा," and "हां जी." each stopped the agent and were answered.
func TestBackchannelsInEveryLanguage(t *testing.T) {
	for _, s := range []string{
		"हां,", "हाँ", "अच्छा,", "हां जी.", "ठीक है।", "हम्म", "जी", // Hindi, both nasal spellings
		"हो", "बरं", // Marathi
		"হ্যাঁ", "আচ্ছা", "ঠিক আছে", // Bengali
		"ஆமா", "சரி", "சரிங்க", // Tamil
		"అవును", "సరే", // Telugu
		"ಹೌದು", "ಸರಿ", // Kannada
		"അതെ", "ശരി", // Malayalam
		"હા", "બરાબર", "ઠીક છે", // Gujarati
		"ਹਾਂ ਜੀ", "ਅੱਛਾ", // Punjabi
		"ହଁ", "ଠିକ ଅଛି", // Odia
		"haan ji", "accha", "theek hai", // Hinglish in Latin script
		"はい", "うん", "なるほど", // Japanese
		"嗯", "对", "好的", // Chinese
		"네", "맞아요", // Korean
		"نعم", "تمام", // Arabic
		"evet", "tamam", // Turkish
		"да", "ага", "понятно", // Russian
		"так", "добре", // Ukrainian
		"tak", "dobrze", // Polish
		"ja", "oké", // Dutch
		"ano", "jasně", // Czech
		"igen", "értem", // Hungarian
		"precis",     // Swedish
		"da", "bine", // Romanian
		"joo", "kyllä", // Finnish
		"præcis",                 // Danish
		"vâng", "dạ", "đúng rồi", // Vietnamese
		"yeah", "okay", "mm-hmm", "d'accord", "vale", "sí", "oui", // as before
	} {
		if !isAcknowledgementOnly(s) {
			t.Errorf("%q should be a backchannel", s)
		}
	}
}

// A sentence that starts with a backchannel word is still a turn.
func TestSentencesThatStartLikeBackchannelsAreTurns(t *testing.T) {
	for _, s := range []string{
		"हां, मुझे अपना बिल चाहिए", "ठीक है, कल आइए", "अच्छा, कितने बजे?",
		"சரி, நாளை எத்தனை மணிக்கு?", "ಹೌದು, ನನ್ನ ಬಿಲ್ ಎಷ್ಟು?", "હા, મારે ચૂકવવું છે",
		"はい、明日は何時ですか", "好的，明天几点开门？", "да, сколько стоит?",
		"tak, chcę zapłacić", "vâng, ngày mai mấy giờ?", "yes, I want to pay",
		"no", "stop", "wait",
	} {
		if isAcknowledgementOnly(s) {
			t.Errorf("%q is a real turn, not a backchannel", s)
		}
	}
}

// Sarvam writes a one-word Tamil reply in another Indian script; spoken over
// the agent, it's a backchannel, not a language switch.
func TestMisScriptedShortRepliesAreBackchannels(t *testing.T) {
	p := &Pipeline{}
	p.turnLanguage.Store("ta")
	cases := []struct {
		text, lang string
		want       bool
	}{
		{"આમાં", "gu-IN", true}, // "ஆமா" written as Gujarati
		{"సరే సరే", "te", true}, // "சரி சரி" written as Telugu
		{"சரி", "ta", false},    // same language: up to the backchannel list
		{"નમસ્તે, મારે બિલ વિશે પૂછવું છે", "gu", false}, // a real switch: a sentence
		{"okay so", "en", false}, // not an Indian-script mix-up
	}
	for _, c := range cases {
		ev := TranscriptEvent{Text: c.text, Language: c.lang, OverAgentSpeech: true}
		if got := p.isMisScriptedShortReply(ev); got != c.want {
			t.Errorf("%q (%s) on a Tamil call: got %v, want %v", c.text, c.lang, got, c.want)
		}
	}
}
