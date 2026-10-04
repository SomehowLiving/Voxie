package pipeline

import (
	"strings"
	"unicode"
)

// checkInGreetings are the words a caller uses to check the line is live:
// "hello?", "allô?", "喂". A turn needs one of them to be a check-in.
var checkInGreetings = setOfWords(
	"hello", "helo", "hallo", "hullo", "hi", "hey", "hola", "allo", "allô", "alo", "aló",
	"pronto", "bonjour", "oi", "olá", "ola", "moshi", "もしもし", "喂", "你好", "여보세요",
	"ألو", "الو", "هلو", "हेलो", "हैलो", "हलो", "हॅलो", "ஹலோ", "హలో", "হ্যালো", "હેલો",
	"ಹಲೋ", "ഹലോ", "ਹੈਲੋ", "ହେଲୋ",
	"there", "hear", // "are you there?", "can you hear me?"
)

// checkInFillers may sit around a greeting without making the turn a
// request: "haan hello", "hello ji", "are you there".
var checkInFillers = setOfWords(
	"haan", "han", "ha", "ji", "हाँ", "हां", "जी", "yes", "yeah", "ok", "okay",
	"are", "you", "can", "me", "anyone", "sir", "madam", "ma'am",
)

func setOfWords(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// isCheckIn reports whether a turn is only the caller checking the line
// ("hello?", "haan hello", "hello? hello?", "are you there?"), not a request.
// "Hello, I want to pay my bill" is not one: it carries words of its own.
func isCheckIn(text string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && r != '\''
	})
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	greeted := false
	for _, w := range words {
		switch {
		case checkInGreetings[w]:
			greeted = true
		case !checkInFillers[w]:
			return false
		}
	}
	return greeted
}
