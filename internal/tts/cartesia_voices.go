package tts

import "strings"

// Cartesia: which model and voice speak a sentence, and the locale sent with it.
//
// A Cartesia voice speaks every language in its own accent unless it has an
// accent for that language: Katie (American English) read Hindi and Tamil like
// a foreigner (live, 2026-10). So each language gets a voice that's native in
// it, from Cartesia's voice list (GET /voices?language=xx-IN, accents with
// is_native) on 2026-10-26: the first female voice per language, like Katie
// (Mandarin from the mainland), or the only one: Ukrainian's is Oleh, male. A
// native voice of either gender beats a matching one with a foreign accent.
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

	// Live, Katie read Japanese in an American accent.
	"es": "3597a26f-80ef-4bd5-8101-9699bc764917", // Ximena, es-MX
	"fr": "7c58f4a4-a72c-42fa-a503-41b9408820f3", // Inès
	"it": "00e9ec78-2002-41dd-8d19-6b1d3b17a461", // Elena
	"pt": "8a6d0b8e-8cd8-4952-a41e-b7af18662135", // Helena, pt-BR
	"ja": "cb21b4af-0e46-44f7-9128-51be7f898311", // Yuzuki
	"zh": "6eb8965c-e295-47bd-a9e4-3eeebb3abcff", // Jing, zh-CN
	"de": "9b4d08b6-0494-4301-ab92-9150f4ee2718", // Marlene
	"nl": "96355f3d-0179-4c9a-a8d8-11ef0779a9b8", // Noa
	"ru": "064b17af-d36b-4bfb-b003-be07dba1b649", // Tatiana
	"pl": "f8aacd6e-1ac2-42d3-bcbf-125336ecd0f2", // Ewa
	"cs": "b66b3a19-31b3-401f-8b08-40d46a61e4f1", // Milena
	"hu": "e97c3b37-1aa5-46af-afb7-9545086aaa92", // Eszter
	"sv": "bcdcad6d-4eb6-4a71-89fd-1ae2ce9f95d8", // Freja
	"ro": "34acfaee-c556-41ee-a5f6-c687fb20357c", // Andrada
	"fi": "2835e382-643b-4ac6-8f6c-74df549a7ad0", // Milla
	"da": "eb929394-68e7-4e08-bd2f-e7055728a5e1", // Mette
	"vi": "8e8f222d-c817-4cc5-822b-8bf76ca7e98d", // Lien
	"uk": "05ffab9c-d380-4909-8375-cd12f59238c3", // Oleh: the only native Ukrainian voice
	"ko": "1a602be4-3b57-4bef-a270-dfa5efed0541", // Yeji
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

// speaks reports whether Cartesia speaks Voxie language l natively here: the
// model has it, and a voice native in it is set (configured, a default in
// cartesiaNativeVoices, or the English default voice for English). Without
// one the language is still spoken, by voice_id in its own accent, but the
// failover chain should prefer a voice that's native.
func (o CartesiaOptions) speaks(l string) bool {
	if !cartesiaSpeaks(o.Model, l) {
		return false
	}
	return l == "en" || o.Voices[l] != "" || cartesiaNativeVoices[l] != ""
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
