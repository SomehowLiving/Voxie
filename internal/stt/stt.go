package stt

import (
	"context"
	"fmt"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

type TranscriptResult struct {
	Text    string
	IsFinal bool
	// Confidence is the provider-reported confidence for the transcript in the
	// range [0, 1]. Zero means the provider did not report a value (or the
	// reported value was genuinely zero); callers must treat it as "unknown"
	// rather than "low confidence".
	Confidence float64
	// Language is the provider-detected language of a final ("hi", "ta",
	// "en"...), empty when the provider doesn't say.
	Language string
	// Provider names the provider that produced it, when it ran inside a
	// failover chain.
	Provider string
}

// Client is the interface that all STT providers must implement.
type Client interface {
	SendAudio(data []byte) error
	Close()
}

// PartialsEmitter is an optional capability interface for STT providers.
// A provider that streams interim (partial) transcripts while the caller
// is still talking implements it returning true; a finals-only provider —
// one whose transcripts arrive only once the caller has finished — returns
// false.
//
// The pipeline type-asserts a Client against this interface exactly once,
// at construction, and defaults to true when the provider does not
// implement it, so a provider silent on the question keeps the
// partials-driven behaviour. Returning false changes two things: live
// captions show finals only, and barge-in, which can no longer confirm
// real speech from partial text, degrades to firing on VAD alone once the
// full backchannel window has elapsed — with no text, a short burst
// inside the window cannot be classified as anything but backchannel.
type PartialsEmitter interface {
	EmitsPartials() bool
}

// NewClient returns an STT client for the configured provider.
func NewClient(ctx context.Context, cfg *config.Config, onResult func(TranscriptResult)) (Client, error) {
	return NewClientFor(ctx, cfg, Hint{}, onResult)
}

// Hint is what's known about the caller before they speak: their
// language on record and their region ("IN"). Only the adaptive provider
// uses it, to choose the starting listener.
type Hint struct {
	Language string
	Region   string
}

// NewClientFor is NewClient for a call whose caller is known.
func NewClientFor(ctx context.Context, cfg *config.Config, hint Hint, onResult func(TranscriptResult)) (Client, error) {
	switch cfg.STT.Provider {
	case "failover":
		return newChain(ctx, cfg, cfg.STT.Failover, "", onResult)
	case "adaptive":
		return newAdaptiveClient(ctx, cfg, hint, onResult)
	}
	return newProvider(ctx, cfg, cfg.STT.Provider, onResult)
}

// newChain builds a failover chain over names. A non-empty language fixes
// the language of the chain's Deepgram (only codes nova3Language knows;
// others stay multi); Sarvam keeps auto-detecting.
func newChain(ctx context.Context, cfg *config.Config, names []string, language string, onResult func(TranscriptResult)) (Client, error) {
	build := func(ctx context.Context, name string, onResult func(TranscriptResult)) (Client, error) {
		if name == "failover" || name == "adaptive" {
			return nil, fmt.Errorf("%q can't be one of a chain's providers", name)
		}
		if language != "" && name == "deepgram" {
			c := *cfg
			c.Deepgram.Language = language
			return newProvider(ctx, &c, name, onResult)
		}
		return newProvider(ctx, cfg, name, onResult)
	}
	return newFailoverClient(ctx, names, build, true, onResult)
}

// newProvider builds one named STT provider.
func newProvider(ctx context.Context, cfg *config.Config, provider string, onResult func(TranscriptResult)) (Client, error) {
	switch provider {
	case "deepgram":
		if cfg.Deepgram.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [deepgram] api_key to be set", provider)
		}
		return NewDeepgramClient(ctx, cfg.Deepgram, onResult)
	case "openai":
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [openai] api_key to be set", provider)
		}
		return NewOpenAIClient(ctx, cfg.OpenAI.APIKey, cfg.OpenAI.STTModel, onResult)
	case "assemblyai":
		if cfg.AssemblyAI.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [assemblyai] api_key to be set", provider)
		}
		return NewAssemblyAIClient(ctx, cfg.AssemblyAI, onResult)
	case "aliyun":
		if cfg.Aliyun.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [aliyun] api_key to be set", provider)
		}
		return NewAliyunClient(ctx, cfg.Aliyun, onResult)
	case "volcengine":
		if cfg.Volcengine.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [volcengine] api_key to be set", provider)
		}
		return NewVolcengineClient(ctx, cfg.Volcengine, onResult)
	case "telnyx":
		if cfg.Telnyx.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [telnyx] api_key to be set", provider)
		}
		return NewTelnyxClient(ctx, cfg.Telnyx, onResult)
	case "vibevoice":
		return NewVibeVoiceClient(ctx, cfg.VibeVoice.ASRURL, onResult)
	case "sarvam":
		if cfg.Sarvam.Mode == "rest" {
			return NewSarvamClient(ctx, cfg.Sarvam, onResult)
		}
		return NewSarvamStreamClient(ctx, cfg.Sarvam, onResult)
	default:
		return nil, fmt.Errorf("unknown stt provider %q (supported: adaptive, aliyun, assemblyai, deepgram, failover, openai, sarvam, telnyx, vibevoice, volcengine)", provider)
	}
}
