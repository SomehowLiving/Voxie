package pipeline

import "testing"

// Callers acknowledge and interrupt in their own language. Before these word
// lists covered other languages, a Spanish "vale, claro" over the agent read
// as real content and cut it off mid-sentence, and "espera" was only an
// interruption by accident of being a long word.

func TestMultilingualBackchannelsDoNotInterrupt(t *testing.T) {
	for _, s := range []string{
		"sí, vale", "claro, claro", "de acuerdo", // Spanish
		"oui, d'accord", "ouais", // French
		"sì, va bene", "certo", // Italian
		"sim, tá", "beleza", // Portuguese
		"ja, genau", "stimmt", // German
		"haan ji", "accha theek hai", // Hindi in Latin script
	} {
		if !isBackchannelTranscript(s) {
			t.Errorf("%q is an acknowledgement, want it treated as a backchannel", s)
		}
		if isMeaningfulBargeInTranscript(s) {
			t.Errorf("%q would cut the agent off, want it ignored", s)
		}
	}
}

func TestMultilingualContentStillInterrupts(t *testing.T) {
	for _, s := range []string{
		"de acuerdo pero no puedo pagar", // "de acuerdo" + real content
		"espera",                         // stop words on their own
		"attendez",
		"warte",
		"nein",
		"ruko",
	} {
		if !isMeaningfulBargeInTranscript(s) {
			t.Errorf("%q should interrupt the agent", s)
		}
	}
}

func TestNormalizationKeepsCombiningMarks(t *testing.T) {
	// "namaste" in Devanagari: its vowels are combining marks. Without them
	// the word fell apart into three fragments.
	if got := normalizedTranscriptText("नमस्ते"); got != "नमस्ते" {
		t.Errorf("normalizedTranscriptText(\"नमस्ते\") = %q, want the word intact", got)
	}
}
