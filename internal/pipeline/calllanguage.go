package pipeline

import (
	"strings"
	"sync"
	"unicode"
)

// callLanguage is the language the conversation is in, which isn't always
// the language of the last turn. Callers speaking Kannada or Hindi say
// "okay", "thank you" and "bye" in English all the time; live, "Okay" in a
// Kannada call got an English reply. So a turn changes the call's language
// only on real evidence:
//   - a sentence in another language: three words that aren't fillers
//     ("okay", "thank you", "haan", "sari"...), or, in Chinese or Japanese
//     script, four characters;
//   - three short turns in a row in the same other language;
//   - the agent replying in another language: it was asked to switch
//     ("can you speak Japanese?"), or chose to.
//
// A turn with no language (a listener that doesn't label) keeps the call's.
type callLanguage struct {
	mu      sync.Mutex
	current string
	pending string // a different language heard in short turns
	streak  int    // short turns in a row in pending
}

const (
	callLanguageWords  = 3 // non-filler words that make a turn a sentence
	callLanguageCJK    = 4 // letters, in scripts written without spaces
	callLanguageStreak = 3 // short turns in a row that switch the call
)

// observe takes a caller turn heard in language and returns the call's
// language after it.
func (c *callLanguage) observe(language, text string) string {
	language = baseLang(language)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case language == "":
	case c.current == "":
		c.current = language
	case language == c.current:
		c.pending, c.streak = "", 0
	case isSentence(language, text):
		c.current, c.pending, c.streak = language, "", 0
	default:
		if language == c.pending {
			c.streak++
		} else {
			c.pending, c.streak = language, 1
		}
		if c.streak >= callLanguageStreak {
			c.current, c.pending, c.streak = language, "", 0
		}
	}
	return c.current
}

// replied records the language the agent answered in.
func (c *callLanguage) replied(language string) {
	language = baseLang(language)
	if language == "" {
		return
	}
	c.mu.Lock()
	c.current, c.pending, c.streak = language, "", 0
	c.mu.Unlock()
}

func (c *callLanguage) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// isSentence reports whether text says enough to show the caller is
// speaking language, not dropping in a word of it.
func isSentence(language, text string) bool {
	if language == "zh" || language == "ja" {
		letters := 0
		for _, r := range text {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		return letters >= callLanguageCJK
	}
	words := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && r != '\''
	}) {
		if !languageFreeWords[w] {
			words++
		}
	}
	return words >= callLanguageWords
}

// languageFreeWords carry no language: callers say them in any language's
// conversation. English ones dominate, since Indian callers use them all
// the time; the rest are short replies in Indian languages, written as
// speech-to-text writes them in Latin script.
var languageFreeWords = setOfWords(
	"ok", "okay", "okey", "oh", "ah", "aha", "um", "uh", "hmm", "mm", "yes", "yeah", "yep", "no", "nope",
	"thanks", "thank", "you", "bye", "goodbye", "hello", "hi", "hey", "sorry", "please", "cool", "fine",
	"sure", "great", "alright", "right", "good", "nice", "welcome", "wow", "so", "and", "the", "very", "much",
	"haan", "han", "ha", "nahi", "theek", "thik", "accha", "acha", "ji", "sari", "seri", "aama", "houdu", "avunu",
)
