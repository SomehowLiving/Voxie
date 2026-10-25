package stt

import (
	"time"

	"testing"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
)

func TestNova3LanguageStripsLocale(t *testing.T) {
	cases := map[string]string{
		"":      "en",
		"en":    "en",
		"en-US": "en",
		"en-IN": "en",
		"en-NZ": "en",
		"en-GB": "en",
		"en-ZA": "en",
		"es":    "es",
		"es-ES": "es",
		// Nova-3 transcribes Mandarin directly; routing it to "multi" returns
		// garbage rather than a worse-but-usable transcript.
		"zh":    "zh",
		"zh-CN": "zh",
		"zh-TW": "zh",
		// Checked against real audio with the language fixed (see nova3Language).
		"ja-JP": "ja",
		"ko-KR": "ko",
		"hi-IN": "hi",
		"ta-IN": "ta",
		"ar":    "ar",
		"pl-PL": "pl",
		"sv-SE": "sv",
		"hu":    "hu",
		// Malayalam isn't offered by Nova-3; unverified codes stay multi.
		"ml-IN": "multi",
		"mi-NZ": "multi",
		"multi": "multi",
	}
	for in, want := range cases {
		if got := nova3Language(in); got != want {
			t.Errorf("nova3Language(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeepgramCallbackDedupesRepeatedPartials(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("hello there", false, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.Message(deepgramMessage("hello there", false, false)); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Text != "hello there" || got[0].IsFinal {
		t.Fatalf("got %+v, want one non-final hello there", got[0])
	}
}

func TestDeepgramCallbackUtteranceEndDoesNotPromotePartial(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("change the email address", false, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d results, want only the partial", len(got))
	}
	if got[0].Text != "change the email address" || got[0].IsFinal {
		t.Fatalf("got %+v, want non-final transcript", got[0])
	}
}

func TestDeepgramCallbackSpeechStartedDoesNotClearFinalizedChunk(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("first finalized chunk", true, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.SpeechStarted(&msginterfaces.SpeechStartedResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want partial and final", len(got))
	}
	if got[1].Text != "first finalized chunk" || !got[1].IsFinal {
		t.Fatalf("got final %+v, want preserved finalized chunk", got[1])
	}
}

func TestMergeTranscriptChunksDedupesOrdinaryOverlap(t *testing.T) {
	got := mergeTranscriptChunks("I would like a booking", "booking for tomorrow")
	want := "I would like a booking for tomorrow"
	if got != want {
		t.Fatalf("mergeTranscriptChunks ordinary overlap = %q, want %q", got, want)
	}
}

func TestMergeTranscriptChunksPreservesRepeatedSpokenPhoneDigit(t *testing.T) {
	got := mergeTranscriptChunks("o two one", "one two three four five six seven")
	want := "o two one one two three four five six seven"
	if got != want {
		t.Fatalf("mergeTranscriptChunks repeated spoken digit = %q, want %q", got, want)
	}
}

func deepgramMessage(text string, isFinal, speechFinal bool) *msginterfaces.MessageResponse {
	return deepgramMessageWithConfidence(text, isFinal, speechFinal, 0)
}

func deepgramMessageWithConfidence(text string, isFinal, speechFinal bool, confidence float64) *msginterfaces.MessageResponse {
	return &msginterfaces.MessageResponse{
		Channel: msginterfaces.Channel{
			Alternatives: []msginterfaces.Alternative{
				{Transcript: text, Confidence: confidence},
			},
		},
		IsFinal:     isFinal,
		SpeechFinal: speechFinal,
	}
}

func TestDeepgramCallbackPropagatesFinalConfidence(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessageWithConfidence("hello world", true, true, 0.93)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsFinal {
		t.Fatalf("got %+v, want one final", got)
	}
	if got[0].Confidence != 0.93 {
		t.Fatalf("final confidence = %v, want 0.93", got[0].Confidence)
	}
}

func TestDeepgramCallbackPropagatesPartialConfidence(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessageWithConfidence("hello there", false, false, 0.71)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IsFinal {
		t.Fatalf("got %+v, want one partial", got)
	}
	if got[0].Confidence != 0.71 {
		t.Fatalf("partial confidence = %v, want 0.71", got[0].Confidence)
	}
}

func TestDeepgramCallbackMissingConfidenceFallsBackToZero(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("hello world", true, true)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsFinal {
		t.Fatalf("got %+v, want one final", got)
	}
	if got[0].Confidence != 0 {
		t.Fatalf("missing-confidence final = %v, want 0", got[0].Confidence)
	}
	if got[0].Text != "hello world" {
		t.Fatalf("text = %q, want preserved transcript", got[0].Text)
	}
}

// fakeClock steps time for the callback.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time       { return c.t }
func (c *fakeClock) step(d time.Duration) { c.t = c.t.Add(d) }
func finals(got []TranscriptResult) []string {
	var out []string
	for _, r := range got {
		if r.IsFinal {
			out = append(out, r.Text)
		}
	}
	return out
}

// Live, on a line with a faint hiss: Deepgram re-sent the same interim words
// for 2-3 s and ended the sentence only on UtteranceEnd.
func TestDeepgramEndsASentenceWhoseWordsStopChanging(t *testing.T) {
	var got []TranscriptResult
	clock := &fakeClock{t: time.Unix(1000, 0)}
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }, now: clock.now}
	_ = cb.Message(deepgramMessage("Ich brauch eine", false, false))
	clock.step(900 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Ich brauch eine Rückerstattung.", false, false))
	clock.step(900 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Ich brauch eine Rückerstattung.", false, false))
	if f := finals(got); len(f) != 1 || f[0] != "Ich brauch eine Rückerstattung." {
		t.Fatalf("finals = %q, want the sentence once its words stopped changing", f)
	}
	// Deepgram's own late final adds nothing: dropped.
	clock.step(900 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Ich brauch eine Rückerstattung.", true, false))
	_ = cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{})
	if f := finals(got); len(f) != 1 {
		t.Fatalf("finals = %q; Deepgram's late final repeated the sentence", f)
	}
}

func TestDeepgramLateFinalSendsOnlyTheNewWords(t *testing.T) {
	var got []TranscriptResult
	clock := &fakeClock{t: time.Unix(1000, 0)}
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }, now: clock.now}
	_ = cb.Message(deepgramMessage("Mi número de pedido es", false, false))
	clock.step(800 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Mi número de pedido es", false, false))
	clock.step(1500 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Mi número de pedido es cuatro cinco.", true, true))
	if f := finals(got); len(f) != 2 || f[1] != "cuatro cinco." {
		t.Fatalf("finals = %q, want the early part then only the new words", f)
	}
}

func TestDeepgramWordsStillChangingAreNotFinal(t *testing.T) {
	var got []TranscriptResult
	clock := &fakeClock{t: time.Unix(1000, 0)}
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }, now: clock.now}
	for _, w := range []string{"I want", "I want to book", "I want to book a table"} {
		_ = cb.Message(deepgramMessage(w, false, false))
		clock.step(900 * time.Millisecond)
	}
	if f := finals(got); len(f) != 0 {
		t.Fatalf("finals = %q while the caller was still talking", f)
	}
}

func TestDeepgramSameWordsLaterAreANewSentence(t *testing.T) {
	var got []TranscriptResult
	clock := &fakeClock{t: time.Unix(1000, 0)}
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }, now: clock.now}
	_ = cb.Message(deepgramMessage("Hello?", false, false))
	clock.step(800 * time.Millisecond)
	_ = cb.Message(deepgramMessage("Hello?", false, false))
	clock.step(time.Second)
	_ = cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{}) // Deepgram ends that utterance
	clock.step(8 * time.Second)
	_ = cb.Message(deepgramMessage("Hello?", true, true))
	if f := finals(got); len(f) != 2 {
		t.Fatalf("finals = %q; a second Hello? 8 s later is a new turn", f)
	}
}

// Live: two sentences ended early within one Deepgram utterance, then
// Deepgram's own final for all of it repeated the first sentence whole.
func TestDeepgramTwoEarlyFinalsThenDeepgramsOwnSendNothingTwice(t *testing.T) {
	var got []TranscriptResult
	clock := &fakeClock{t: time.Unix(1000, 0)}
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }, now: clock.now}
	stable := func(text string) {
		_ = cb.Message(deepgramMessage(text, false, false))
		clock.step(800 * time.Millisecond)
		_ = cb.Message(deepgramMessage(text, false, false))
	}
	stable("Scusi, parlo meglio italiano.")
	stable("Scusi, parlo meglio italiano. Quanto tempo ci vuole?")
	_ = cb.Message(deepgramMessage("Scusi, parlo meglio italiano. Quanto tempo ci vuole?", true, false))
	_ = cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{})
	want := []string{"Scusi, parlo meglio italiano.", "Quanto tempo ci vuole?"}
	if f := finals(got); len(f) != 2 || f[0] != want[0] || f[1] != want[1] {
		t.Fatalf("finals = %q, want %q", f, want)
	}
}
