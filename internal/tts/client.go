package tts

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// StreamChunk represents a chunk of synthesized PCM audio streamed from the
// TTS provider. The channel closes when synthesis is complete. If Err is
// non-nil the synthesis failed and no further chunks will arrive.
type StreamChunk struct {
	PCM []byte
	Err error
}

// Client synthesizes text to PCM audio (linear16, 16kHz mono).
type Client interface {
	// Synthesize produces the full audio for text in one shot.
	Synthesize(ctx context.Context, text string) ([]byte, error)

	// SynthesizeStream produces audio incrementally via a channel, so playback
	// can begin before the full utterance is ready. The channel is always
	// closed by the producer (even on error). Cancel ctx to abort early.
	//
	// Providers that return audio in a single envelope (a JSON body, say)
	// satisfy this by emitting one chunk — callers need not care which.
	SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error)
}

// NewClient returns a TTS client for the configured provider. The
// returned client wraps the provider in a sanitizer that strips Markdown
// formatting from every input — TTS engines read characters like '*' and
// '#' aloud literally, so RAG snippets or LLM-emitted markdown must be
// scrubbed before synthesis.
// Voices names the voices a deployment is configured with, in order: the
// failover chain, or the one provider.
func Voices(cfg *config.Config) []string {
	if cfg.TTS.Provider == "failover" {
		return append([]string(nil), cfg.TTS.Failover...)
	}
	return []string{cfg.TTS.Provider}
}

// NewClientFor is NewClient for a call that asked for a voice: with
// [tts] client_choice on and the voice in the chain, it goes first for this
// call, the rest of the chain behind it. Anything else gets the deployment's
// order.
func NewClientFor(cfg *config.Config, voice string) (Client, error) {
	if voice == "" || !cfg.TTS.ClientChoice || cfg.TTS.Provider != "failover" {
		return NewClient(cfg)
	}
	if !slices.Contains(cfg.TTS.Failover, voice) {
		log.Printf("[tts] call asked for voice %q, which isn't configured; using %v", voice, cfg.TTS.Failover)
		return NewClient(cfg)
	}
	c := *cfg
	c.TTS.Failover = voiceFirst(cfg.TTS.Failover, voice)
	c.TTS.Languages = make(map[string][]string, len(cfg.TTS.Languages))
	for lang, order := range cfg.TTS.Languages {
		c.TTS.Languages[lang] = voiceFirst(order, voice)
	}
	log.Printf("[tts] this call asked for %s: %s", voice, strings.Join(c.TTS.Failover, " → "))
	return NewClient(&c)
}

// voiceFirst moves voice to the front of order, if it's in it.
func voiceFirst(order []string, voice string) []string {
	if !slices.Contains(order, voice) {
		return append([]string(nil), order...)
	}
	out := []string{voice}
	for _, n := range order {
		if n != voice {
			out = append(out, n)
		}
	}
	return out
}

func NewClient(cfg *config.Config) (Client, error) {
	inner, err := newProviderClient(cfg)
	if err != nil {
		return nil, err
	}
	return &sanitizingClient{inner: inner}, nil
}

func newProviderClient(cfg *config.Config) (Client, error) {
	if cfg.TTS.Provider == "failover" {
		return newFailoverFromConfig(cfg)
	}
	c, err := newNamedProvider(cfg, cfg.TTS.Provider)
	if err != nil {
		return nil, err
	}
	return adaptTags(cfg.TTS.Provider, c), nil
}

// newFailoverFromConfig builds every provider named in [tts] failover and
// [tts.languages] and chains them.
func newFailoverFromConfig(cfg *config.Config) (Client, error) {
	clients := map[string]Client{}
	add := func(name string) error {
		if _, ok := clients[name]; ok {
			return nil
		}
		if name == "failover" {
			return fmt.Errorf("tts failover can't contain \"failover\"")
		}
		c, err := newNamedProvider(cfg, name)
		if err != nil {
			return fmt.Errorf("tts failover: %w", err)
		}
		clients[name] = adaptTags(name, c)
		return nil
	}
	for _, n := range cfg.TTS.Failover {
		if err := add(n); err != nil {
			return nil, err
		}
	}
	for _, names := range cfg.TTS.Languages {
		for _, n := range names {
			if err := add(n); err != nil {
				return nil, err
			}
		}
	}
	speaks := map[string]func(string) bool{}
	for name := range clients {
		if s := speaksFor(cfg, name); s != nil {
			speaks[name] = s
		}
	}
	return newFailoverClient(clients, FailoverOptions{
		Speaks:            speaks,
		Order:             cfg.TTS.Failover,
		ByLanguage:        cfg.TTS.Languages,
		FirstAudioTimeout: time.Duration(cfg.TTS.FirstAudioTimeoutMs) * time.Millisecond,
		Cooldown:          time.Duration(cfg.TTS.CooldownMs) * time.Millisecond,
	})
}

// newNamedProvider builds one voice provider by name.
func newNamedProvider(cfg *config.Config, name string) (Client, error) {
	switch name {
	case "deepgram":
		if cfg.Deepgram.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "deepgram", Field: "[deepgram] api_key"}
		}
		return NewDeepgramClient(cfg.Deepgram.APIKey, cfg.Deepgram.TTSModel), nil
	case "cartesia":
		if cfg.Cartesia.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "cartesia", Field: "[cartesia] api_key"}
		}
		client := NewCartesiaWSClient(cfg.Cartesia.APIKey, cfg.Cartesia.VoiceID, cfg.Cartesia.WSURL)
		// Gate generations to the account's TTS concurrency limit so extra
		// requests queue locally instead of getting 429s. A negative limit
		// disables gating; the limiter is shared process-wide (one ceiling
		// per Cartesia key).
		if cfg.Cartesia.MaxConcurrency > 0 {
			client = newLimitedClient(client, sharedConcurrencyLimiter(cfg.Cartesia.MaxConcurrency))
		}
		return client, nil
	case "elevenlabs":
		if cfg.ElevenLabs.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "elevenlabs", Field: "[elevenlabs] api_key"}
		}
		return NewElevenLabsClient(cfg.ElevenLabs.APIKey, cfg.ElevenLabs.VoiceID, cfg.ElevenLabs.Model), nil
	case "speechify":
		if cfg.Speechify.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "speechify", Field: "[speechify] api_key"}
		}
		return NewSpeechifyClient(cfg.Speechify.APIKey, cfg.Speechify.VoiceID, cfg.Speechify.Model), nil
	case "telnyx":
		if cfg.Telnyx.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "telnyx", Field: "[telnyx] api_key"}
		}
		return NewTelnyxClient(cfg.Telnyx.APIKey, cfg.Telnyx.Voice, cfg.Telnyx.VoiceSpeed), nil
	case "minimax":
		if cfg.MiniMax.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "minimax", Field: "[minimax] api_key"}
		}
		return NewMiniMaxClient(cfg.MiniMax.APIKey, cfg.MiniMax.VoiceID, cfg.MiniMax.Model, cfg.MiniMax.BaseURL), nil
	case "mimo":
		if cfg.MiMo.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "mimo", Field: "[mimo] api_key"}
		}
		return NewMiMoClient(cfg.MiMo.APIKey, cfg.MiMo.Voice, cfg.MiMo.Model, cfg.MiMo.BaseURL), nil
	case "aliyun":
		if cfg.Aliyun.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "aliyun", Field: "[aliyun] api_key"}
		}
		return NewAliyunClient(cfg.Aliyun.APIKey, cfg.Aliyun.Voice, cfg.Aliyun.TTSModel, cfg.Aliyun.URL), nil
	case "volcengine":
		if cfg.Volcengine.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "volcengine", Field: "[volcengine] api_key"}
		}
		return NewVolcengineClient(cfg.Volcengine.APIKey, cfg.Volcengine.Speaker,
			cfg.Volcengine.TTSResourceID, cfg.Volcengine.TTSURL), nil
	case "vibevoice":
		return NewVibeVoiceClient(cfg.VibeVoice.TTSURL, cfg.VibeVoice.Voice), nil
	default:
		return nil, fmt.Errorf("unknown tts provider %q (supported: aliyun, cartesia, deepgram, elevenlabs, failover, mimo, minimax, speechify, telnyx, vibevoice, volcengine)", name)
	}
}

// sanitizingClient strips Markdown from every synthesis request before
// handing it to the wrapped provider.
type sanitizingClient struct {
	inner Client
}

func (s *sanitizingClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return s.inner.Synthesize(ctx, SanitizeForTTS(text))
}

func (s *sanitizingClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return s.inner.SynthesizeStream(ctx, SanitizeForTTS(text))
}

// SynthesizeStreamWithControls forwards per-utterance voice controls to the
// wrapped provider when it supports them, degrading to a plain stream when it
// doesn't — callers never need to know which provider is configured.
func (s *sanitizingClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	if cs, ok := s.inner.(ControllableStreamer); ok {
		return cs.SynthesizeStreamWithControls(ctx, SanitizeForTTS(text), vc)
	}
	return s.inner.SynthesizeStream(ctx, SanitizeForTTS(text))
}

// elevenLabsV25 is what ElevenLabs' turbo and flash v2.5 models speak.
var elevenLabsV25 = setOfLanguages("en", "ja", "zh", "de", "hi", "fr", "ko", "pt", "it", "es", "id", "nl",
	"tr", "tl", "pl", "sv", "bg", "ro", "ar", "cs", "el", "fi", "hr", "ms", "sk", "da", "ta", "uk", "ru",
	"hu", "no", "vi")

func setOfLanguages(langs ...string) map[string]bool {
	m := make(map[string]bool, len(langs))
	for _, l := range langs {
		m[l] = true
	}
	return m
}

// speaksFor is the languages a configured voice speaks natively, for the
// failover chain to prefer it for them; nil when any (or unknown). The voice
// router picks a native voice per language itself.
func speaksFor(cfg *config.Config, name string) func(string) bool {
	switch name {
	case "deepgram":
		// Aura voices are one language each, named in the model:
		// aura-2-thalia-en, aura-2-celeste-es.
		model := cfg.Deepgram.TTSModel
		if model == "" {
			model = defaultDeepgramTTSModel
		}
		lang := model[strings.LastIndex(model, "-")+1:]
		return func(l string) bool { return l == lang }
	case "elevenlabs":
		model := cfg.ElevenLabs.Model
		if model == "" || strings.Contains(model, "v2_5") || strings.Contains(model, "multilingual") || strings.Contains(model, "v3") {
			return func(l string) bool { return elevenLabsV25[l] }
		}
		return func(l string) bool { return l == "en" }
	case "cartesia":
		// sonic-3 speaks 28 of Voxie's 29: not Odia (probed 2026-10-25).
		return func(l string) bool { return l != "od" }
	}
	return nil
}
