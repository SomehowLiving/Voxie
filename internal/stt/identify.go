package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/streamcoreai/streamcore-server/internal/audio"
)

// Identifying which language a turn was spoken in, from its audio. The
// adaptive listener asks when its listener may have misheard a turn.
//
// Measured on the same 24 languages at phone quality: Groq's Whisper
// large-v3 named every one correctly in 0.5-1.1s (the turbo model named six
// Indian languages wrongly, so it isn't the default). Sarvam names every
// Indian language correctly but labels every world language "en-IN" --
// often after quietly translating it into English -- so its "en" means
// "not an Indian language", never "English".

// Identification is one identifier's answer for a turn.
type Identification struct {
	Language string // ISO 639-1: "ta", "zh"...
	Text     string // its transcript of the turn
	Source   string // "groq" or "sarvam"
}

type identifyFunc func(ctx context.Context, pcm []byte) (Identification, error)

// groqTranscriptionsURL is a var so tests can point it at a local server.
var groqTranscriptionsURL = "https://api.groq.com/openai/v1/audio/transcriptions"

func groqIdentifier(apiKey, model string) identifyFunc {
	if model == "" {
		model = "whisper-large-v3"
	}
	return func(ctx context.Context, pcm []byte) (Identification, error) {
		body, contentType, err := wavForm(pcm, map[string]string{"model": model, "response_format": "verbose_json"})
		if err != nil {
			return Identification{}, err
		}
		var out struct {
			Text     string `json:"text"`
			Language string `json:"language"`
		}
		if err := postForm(ctx, groqTranscriptionsURL, body, contentType, map[string]string{"Authorization": "Bearer " + apiKey}, &out); err != nil {
			return Identification{}, fmt.Errorf("groq: %w", err)
		}
		return Identification{Language: whisperLanguage(out.Language), Text: strings.TrimSpace(out.Text), Source: "groq"}, nil
	}
}

func sarvamIdentifier(apiKey string) identifyFunc {
	return func(ctx context.Context, pcm []byte) (Identification, error) {
		body, contentType, err := wavForm(pcm, map[string]string{"model": "saarika:v2.5", "language_code": "unknown"})
		if err != nil {
			return Identification{}, err
		}
		var out struct {
			Transcript   string `json:"transcript"`
			LanguageCode string `json:"language_code"`
		}
		if err := postForm(ctx, sarvamAPIURL, body, contentType, map[string]string{"api-subscription-key": apiKey}, &out); err != nil {
			return Identification{}, fmt.Errorf("sarvam: %w", err)
		}
		return Identification{Language: baseLanguage(out.LanguageCode), Text: strings.TrimSpace(out.Transcript), Source: "sarvam"}, nil
	}
}

// wavForm builds a multipart body carrying pcm (16kHz mono linear16) as a
// WAV file plus the given fields.
func wavForm(pcm []byte, fields map[string]string) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "turn.wav")
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(encodeWAV(pcm, audio.SampleRate, 1, 16)); err != nil {
		return nil, "", err
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &body, w.FormDataContentType(), nil
}

func postForm(ctx context.Context, url string, body io.Reader, contentType string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// baseLanguage turns "ta-IN" into "ta".
func baseLanguage(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	return code
}

// whisperLanguages maps Whisper's language names to ISO 639-1.
var whisperLanguages = map[string]string{
	"english": "en", "spanish": "es", "french": "fr", "german": "de", "italian": "it",
	"portuguese": "pt", "dutch": "nl", "russian": "ru", "japanese": "ja", "chinese": "zh",
	"korean": "ko", "arabic": "ar", "turkish": "tr", "polish": "pl", "ukrainian": "uk",
	"vietnamese": "vi", "thai": "th", "indonesian": "id", "malay": "ms", "persian": "fa",
	"hebrew": "he", "greek": "el", "swedish": "sv", "urdu": "ur", "nepali": "ne",
	"hindi": "hi", "tamil": "ta", "telugu": "te", "kannada": "kn", "malayalam": "ml",
	"bengali": "bn", "gujarati": "gu", "panjabi": "pa", "punjabi": "pa", "marathi": "mr",
	"odia": "od", "oriya": "od",
}

// whisperLanguage accepts a name ("Tamil") or a code ("ta").
func whisperLanguage(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if code, ok := whisperLanguages[name]; ok {
		return code
	}
	return baseLanguage(name)
}
