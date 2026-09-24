package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"sync"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/vad"
)

// Sarvam AI's Speech-to-Text over REST ([sarvam] mode = "rest"): POST a
// whole utterance, get one transcript back. The default is Sarvam's
// streaming WebSocket (sarvam_stream.go), which an earlier version of this
// comment wrongly said didn't exist; this client remains for deployments
// that prefer per-utterance uploads. It is client-side-endpointed, the exact
// shape telnyx.go's in-house-engine path already solves for the identical
// problem: this reuses that pattern (StreamCore's own VAD segments
// utterances locally, one REST call per utterance) rather than inventing
// a second one.
//
// Practical consequence, stated plainly rather than glossed over:
// EmitsPartials() is false here, same as Telnyx's in-house engine, so the
// pipeline's barge-in falls back to VAD-only after the backchannel window
// (see internal/pipeline/inbound.go's own comment on that fallback). A
// provider swap to Sarvam is not free with respect to barge-in confidence,
// even though it's a one-line config change everywhere else.

// sarvamAPIURL is a var only so tests can point it at a local server.
var sarvamAPIURL = "https://api.sarvam.ai/speech-to-text"

const (
	sarvamFrameBytes         = audio.FrameSize * 2     // 20ms @ 16kHz, linear16
	sarvamOnsetLookbackBytes = 50 * sarvamFrameBytes   // 1s of pre-roll before the detector fires
	sarvamMaxUtteranceBytes  = 1500 * sarvamFrameBytes // ~30s force-flush cap, matching Sarvam's own "under 30 seconds" REST latency guidance

	sarvamVADThreshold    = 1200.0 // same base internal/vad's other users start from
	sarvamVADSpeechFrames = 10     // 200ms of speech to open an utterance
	sarvamVADSilentFrames = 30     // 600ms of silence to close one
)

type sarvamClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	cfg      config.SarvamConfig
	onResult func(TranscriptResult)

	mu            sync.Mutex
	detector      *vad.Detector
	speechActive  bool
	utterance     [][]byte
	uttBytes      int
	lookback      [][]byte
	lookbackBytes int
	wg            sync.WaitGroup
}

func NewSarvamClient(ctx context.Context, cfg config.SarvamConfig, onResult func(TranscriptResult)) (Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("sarvam stt: api_key is required")
	}
	sttCtx, cancel := context.WithCancel(ctx)
	log.Printf("[stt] sarvam: REST-only engine, no interim results -- barge-in runs VAD-only after the backchannel window")
	return &sarvamClient{
		ctx:      sttCtx,
		cancel:   cancel,
		cfg:      cfg,
		onResult: onResult,
		detector: vad.New(sarvamVADThreshold, sarvamVADSpeechFrames, sarvamVADSilentFrames),
	}, nil
}

// SendAudio mirrors telnyx.go's newTelnyxUtteranceClient.SendAudio exactly:
// buffer frames locally, and only call out to the REST API once the local
// VAD has decided an utterance actually ended (or the force-flush cap is
// hit). Never returns an error across utterance boundaries -- a failed
// upload must not stop the pipeline's inbound loop.
func (c *sarvamClient) SendAudio(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ctx.Err() != nil {
		return fmt.Errorf("sarvam stt: client closed")
	}

	started, ended := c.detector.Process(audio.Linear16BytesToPCM(data))
	if started {
		c.speechActive = true
		c.utterance = append(c.utterance, c.lookback...)
		c.uttBytes += c.lookbackBytes
		c.lookback = nil
		c.lookbackBytes = 0
	}

	frame := append([]byte(nil), data...)
	if c.speechActive {
		c.utterance = append(c.utterance, frame)
		c.uttBytes += len(frame)
	} else {
		c.lookback = append(c.lookback, frame)
		c.lookbackBytes += len(frame)
		for c.lookbackBytes > sarvamOnsetLookbackBytes {
			c.lookbackBytes -= len(c.lookback[0])
			c.lookback = c.lookback[1:]
		}
	}

	var frames [][]byte
	if ended {
		c.speechActive = false
		frames = c.utterance
		c.utterance = nil
		c.uttBytes = 0
	} else if c.uttBytes >= sarvamMaxUtteranceBytes {
		frames = c.utterance
		c.utterance = nil
		c.uttBytes = 0
	}
	if len(frames) > 0 {
		c.wg.Add(1)
		go c.transcribe(frames)
	}
	return nil
}

// transcribe uploads one finished utterance as a WAV file and reports the
// single transcript Sarvam sends back.
func (c *sarvamClient) transcribe(frames [][]byte) {
	defer c.wg.Done()

	var pcm []byte
	for _, f := range frames {
		pcm = append(pcm, f...)
	}
	if len(pcm) == 0 {
		return
	}
	wav := encodeWAV(pcm, audio.SampleRate, 1, 16) // 16kHz mono s16le, matching Sarvam's own recommended input rate; encodeWAV is openai.go's, same package

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "utterance.wav")
	if err != nil {
		log.Printf("[stt] sarvam: build request: %v", err)
		return
	}
	if _, err := part.Write(wav); err != nil {
		log.Printf("[stt] sarvam: build request: %v", err)
		return
	}
	if c.cfg.Model != "" {
		_ = w.WriteField("model", c.cfg.Model)
	}
	if c.cfg.LanguageCode != "" {
		_ = w.WriteField("language_code", c.cfg.LanguageCode)
	}
	if err := w.Close(); err != nil {
		log.Printf("[stt] sarvam: build request: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, sarvamAPIURL, &body)
	if err != nil {
		log.Printf("[stt] sarvam: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("api-subscription-key", c.cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if c.ctx.Err() == nil {
			log.Printf("[stt] sarvam: request: %v", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[stt] sarvam: unexpected status %d", resp.StatusCode)
		return
	}

	var result struct {
		Transcript string `json:"transcript"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[stt] sarvam: decode response: %v", err)
		return
	}
	if result.Transcript == "" {
		return
	}

	log.Printf("[stt] sarvam final: %q", result.Transcript)
	c.onResult(TranscriptResult{Text: result.Transcript, IsFinal: true, Confidence: 0})
}

func (c *sarvamClient) Close() {
	c.cancel()
	c.wg.Wait()
}

// EmitsPartials is false: Sarvam has no streaming endpoint, so this client
// -- like telnyx.go's in-house-engine path -- emits exactly one final per
// utterance and nothing before it. See the package-level comment above.
func (c *sarvamClient) EmitsPartials() bool { return false }
