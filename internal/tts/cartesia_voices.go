package tts

import "strings"

// Cartesia: which model and voice speak a sentence, and the locale sent with it.
//
// A Cartesia voice speaks every language in its own accent unless it has an
// accent for that language: Katie (American English) read Hindi and Tamil like
// a foreigner (live, 2026-10). So each language gets a voice that's native in
// it, from Cartesia's voice list (GET /voices?language=xx-IN, accents with
// is_native) on 2026-10-26: the first female voice per language, like Katie.
// [cartesia] voices overrides any of them.

// defaultCartesiaModel is Sonic 3.6 (GA 2026-08-27): the first to speak Odia,
// and the one that takes a locale.
const defaultCartesiaModel = "sonic-3.6"

var cartesiaNativeVoices = map[string]string{
	"hi": "4459a9a5-69d6-4680-b970-e13dc51845b6", // Siya
	"ta": "fb7d8d97-9730-4165-bd79-36b5ce61b5f2", // Janani
	"te": "4418bb06-8329-49a1-bb11-53bb64ca0547", // Shanti
	"bn": "59ba7dee-8f9a-432f-a6c0-ffb33666b654", // Pooja: Bangladeshi Bengali, Cartesia has no Indian Bengali voice yet
	"gu": "4590a461-bc68-4a50-8d14-ac04f5923d22", // Isha
	"kn": "4c00ff98-199c-4967-99ae-7b69125edcff", // Disha
	"ml": "b426013c-002b-4e89-8874-8cd20b68373a", // Latha
	"mr": "5c32dce6-936a-4892-b131-bafe474afe5f", // Anika
	"pa": "9bf2ddcd-bb35-4e90-81b3-21c8183b24f4", // Navjot
	"od": "0046dfd7-171b-4442-9eb7-0712fa712a7a", // Smruti
}

// indianLanguages are read as spoken in India: "12/10/2026" is 12 October, and
// amounts and phone numbers follow Indian conventions.
var indianLanguages = map[string]bool{
	"hi": true, "bn": true, "ta": true, "te": true, "gu": true,
	"kn": true, "ml": true, "mr": true, "pa": true, "od": true,
}

// CartesiaOptions configures a Cartesia client beyond its key.
type CartesiaOptions struct {
	Model   string            // "" means defaultCartesiaModel
	VoiceID string            // for languages without a voice of their own; "" means Katie
	Voices  map[string]string // language → voice ID, over the native defaults
	Locales map[string]string // language → locale, over cartesiaLocale's ("en" = "en-IN")
}

func (o CartesiaOptions) model() string {
	if o.Model != "" {
		return o.Model
	}
	return defaultCartesiaModel
}

// voiceFor is the voice for a sentence in language (Voxie's code, "" if
// untagged): the configured one, else a voice native in it, else the default.
func (o CartesiaOptions) voiceFor(language string) string {
	if v := o.Voices[language]; v != "" {
		return v
	}
	if v := cartesiaNativeVoices[language]; v != "" {
		return v
	}
	if o.VoiceID != "" {
		return o.VoiceID
	}
	return defaultCartesiaVoiceID
}

// localeFor is the locale Cartesia gets for a sentence in language: Indian
// languages as spoken in India ("hi-IN"), anything else its base code, and
// nothing for an untagged sentence (Cartesia then detects it). Voxie calls
// Odia "od"; Cartesia calls it "or".
func (o CartesiaOptions) localeFor(language string) string {
	if l := o.Locales[language]; l != "" {
		return l
	}
	if language == "" {
		return ""
	}
	code := language
	if code == "od" {
		code = "or"
	}
	if indianLanguages[language] {
		return code + "-IN"
	}
	return code
}

// cartesiaSpeaks reports whether model ("" for the default) speaks Voxie
// language l: Sonic 3.6 and later speak all 29; older models (sonic-3 and its
// snapshots, 3.5, 2, turbo) lack Odia (sonic-3 probed 2026-10-25).
func cartesiaSpeaks(model, l string) bool {
	if l != "od" {
		return true
	}
	for _, old := range []string{"sonic-3-", "sonic-3.5", "sonic-2", "sonic-turbo"} {
		if strings.HasPrefix(model, old) {
			return false
		}
	}
	return model != "sonic-3" && model != "sonic"
}
