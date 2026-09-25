package pipeline

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/llm"
	"github.com/streamcoreai/streamcore-server/internal/stt"
	"github.com/streamcoreai/streamcore-server/internal/vad"
)

// runReader reads the caller's audio from the media link (Opus over RTP
// for WebRTC, μ-law for a phone stream) as PCM, and pushes frames into
// inPCMCh.
func (p *Pipeline) runReader() {
	var frameCount uint64
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		pcm, err := p.media.ReadFrame()
		if err != nil {
			if p.ctx.Err() == nil {
				log.Printf("[reader] media read error: %v", err)
			}
			return
		}

		// Diagnostic: log PCM levels for first frames and periodically
		frameCount++
		if frameCount <= 5 || frameCount%100 == 0 {
			var maxAbs int16
			for _, s := range pcm {
				if s < 0 && -s > maxAbs {
					maxAbs = -s
				} else if s > maxAbs {
					maxAbs = s
				}
			}
			log.Printf("[reader] frame=%d pcm_samples=%d max_abs=%d", frameCount, len(pcm), maxAbs)
		}

		select {
		case p.inPCMCh <- PCMFrame{Samples: pcm}:
		case <-p.ctx.Done():
			return
		}
	}
}

// runInbound consumes decoded PCM frames from inPCMCh, feeds them to the
// STT provider, and runs barge-in detection via the energy-based VAD.
func (p *Pipeline) runInbound() {
	// hasPartialText tracks whether STT has recognized actual words during
	// the current speech segment. Barge-in only fires when both VAD detects
	// sustained energy AND STT confirms real speech — preventing false
	// interrupts from noise, coughs, or keyboard clicks.
	var hasPartialText atomic.Bool

	// latestPartial stores the most recent STT partial text (lowercased,
	// trimmed) for backchannel detection.
	var latestPartial sync.Map // key: "text", value: string

	sttCallback := func(result stt.TranscriptResult) {
		ev := TranscriptEvent{Text: result.Text, Final: result.IsFinal, Language: result.Language}
		if result.IsFinal {
			ev.TurnStart = time.Now()
			// Sampled here, not in runAgent: the turn buffer may hold this
			// final for up to turnMergeMax, by which point the agent has
			// finished and the talking-over question can no longer be asked.
			ev.OverAgentSpeech = p.agentSpeechRecent()
			hasPartialText.Store(false)
			latestPartial.Store("text", "")
			// The utterance is over, so a tool that fired on it may fire again
			// on the next thing said.
			p.reflex.endUtterance()
			p.storeLastUserConfidence(result.Confidence)
			// Finals go to the turn buffer, which merges a caller's
			// mid-sentence pauses into one turn before the agent responds.
			select {
			case p.finalCh <- ev:
			case <-p.ctx.Done():
			}
			return
		}
		{
			trimmed := strings.ToLower(strings.TrimSpace(result.Text))
			latestPartial.Store("text", trimmed)
			if len(trimmed) >= 2 {
				// Require at least 2 non-whitespace characters to count as real
				// speech. Single-char noise artifacts ("uh", "m") are ignored.
				hasPartialText.Store(true)
			}
			// Before the turn buffer, before the model. A manifest that
			// declared on_partial gets its packet out now; everything else
			// carries on unchanged.
			p.reflexOnPartial(trimmed)
		}
		select {
		case p.transcriptCh <- ev:
		case <-p.ctx.Done():
		}
	}

	go func() { defer p.recoverPanic("runTurnBuffer"); p.runTurnBuffer() }()

	sttClient, err := stt.NewClientFor(p.ctx, p.cfg, p.listenHint(), sttCallback)
	if err != nil {
		log.Printf("[inbound] STT start error: %v", err)
		return
	}
	defer sttClient.Close()

	// Finals-only providers never confirm speech with partial text
	// (issue #75). A provider that does not implement stt.PartialsEmitter
	// keeps the partials-driven path below exactly as it was. Asked on every
	// decision, not once: a failover or adaptive client can move mid-call
	// between a partials provider (Deepgram) and a finals-only one (Sarvam).
	partialsEmitter, _ := sttClient.(stt.PartialsEmitter)
	emitsPartials := func() bool { return partialsEmitter == nil || partialsEmitter.EmitsPartials() }

	// Backchannel suppression state machine
	var bargeInPending bool
	var bargeInStart time.Time
	var bargeInHeld bool
	// When the barge-in VAD's current run of speech started (zero when
	// silent). A finals-only provider has no partial text to confirm that
	// sound is the caller talking, so the window -- and the duck that comes
	// with it -- waits for finalsOnlyOnset of continuous sound. At the VAD's
	// 60ms onset, the agent's own voice leaking back through a speaker, or a
	// bump, dipped the agent's volume again and again mid-sentence (heard
	// live with Sarvam as "the voice hangs").
	var vadSpeechSince time.Time
	const finalsOnlyOnset = 300 * time.Millisecond
	const backchannelWindow = 600 * time.Millisecond
	// A backchannel can outlast the window ("yeah, okay, sure..."). While the
	// partial text says it is only acknowledgement, the agent stays ducked
	// instead of being cut off; this caps that hold in case the partials
	// stall, after which the old speech-length rule decides.
	const backchannelHoldMax = 3 * time.Second

	// Reusable conversion buffer — one per call instead of one per 20ms
	// frame. Safe: every SendAudio implementation writes the bytes out (or
	// copies them) before returning.
	sttBuf := make([]byte, 0, audio.FrameSize*2)

	// While a delayed greeting is still undecided, track when the caller last
	// made sound. A finished transcript only exists once they've stopped
	// talking and STT has caught up -- seconds after they started -- so the
	// greeting has to look at raw speech onset instead, or it talks over a
	// caller who picked up and said "hello?". Nothing else is speaking at
	// this point in the call, so there's no agent echo to mistake for them.
	var onsetVAD *vad.Detector
	if p.cfg.Pipeline.GreetingDelayMs > 0 {
		onsetVAD = vad.NewDefault()
	}

	for {
		select {
		case <-p.ctx.Done():
			return
		case frame := <-p.inPCMCh:
			// Feed all audio to STT continuously
			data := audio.PCMToLinear16BytesInto(sttBuf, frame.Samples)
			sttBuf = data[:0]
			if err := sttClient.SendAudio(data); err != nil {
				if p.ctx.Err() == nil {
					log.Printf("[inbound] STT send error: %v", err)
				}
				return
			}

			if onsetVAD != nil && !p.greetingDecided.Load() {
				onsetVAD.Process(frame.Samples)
				if onsetVAD.IsSpeaking() {
					p.lastCallerSoundAt.Store(time.Now().UnixNano())
				}
			}

			// Barge-in detection with backchannel suppression.
			// Uses the fast bargeInVAD (60ms onset) for responsiveness.
			if *p.cfg.Pipeline.BargeIn {
				p.bargeInVAD.Process(frame.Samples)
				if !p.bargeInVAD.IsSpeaking() {
					vadSpeechSince = time.Time{}
				} else if vadSpeechSince.IsZero() {
					vadSpeechSince = time.Now()
				}

				if bargeInPending {
					elapsed := time.Since(bargeInStart)
					// Past the window, speech whose partial text is still
					// nothing but acknowledgement keeps the agent ducked rather
					// than cutting it off. It is confirmed the moment the text
					// turns into content ("yeah okay but I never..."), and
					// classified as a backchannel below once it ends. Only for
					// providers that stream partials: without text there is no
					// basis for telling a long "mm-hm" from an interruption.
					holdBackchannel := false
					if emitsPartials() && elapsed >= backchannelWindow && elapsed < backchannelHoldMax {
						partial, _ := latestPartial.Load("text")
						partialStr, _ := partial.(string)
						holdBackchannel = isBackchannelTranscript(partialStr)
						if holdBackchannel && !bargeInHeld {
							bargeInHeld = true
							log.Printf("[inbound] barge-in held past 600ms: still a backchannel (%q)", partialStr)
						}
					}
					if elapsed >= backchannelWindow && !holdBackchannel {
						// Speech continued past the suppression window — real interruption.
						log.Println("[inbound] barge-in confirmed (speech > 600ms)")
						bargeInPending = false
						p.maybeRestoreDuck()
						p.bargeInVAD.Reset()
						select {
						case p.interruptCh <- struct{}{}:
						default:
						}
					} else if !p.bargeInVAD.IsSpeaking() {
						// Speech ended within the window — check for backchannel.
						// A finals-only provider has no partial text here, so
						// every burst that ends inside the window classifies as
						// backchannel: with no text, there is no basis to cut
						// the agent off mid-word.
						partial, _ := latestPartial.Load("text")
						partialStr, _ := partial.(string)
						if !isMeaningfulBargeInTranscript(partialStr) {
							log.Printf("[inbound] backchannel suppressed: %q", partialStr)
							bargeInPending = false
							hasPartialText.Store(false)
							p.maybeRestoreDuck()
						} else if p.readbackBargeInGuardEnabled() && p.readbackInProgress() &&
							!isStrongBargeInCommand(partialStr) {
							// Mid-readback, a weak correction is usually the
							// caller agreeing along. Only an explicit command
							// (stop, cancel, hang up) cuts the readback off.
							log.Printf("[inbound] readback guard held barge-in: %q", partialStr)
							bargeInPending = false
							hasPartialText.Store(false)
							p.maybeRestoreDuck()
						} else {
							// Short but not a backchannel — fire immediately.
							log.Printf("[inbound] barge-in detected (short utterance: %q)", partialStr)
							bargeInPending = false
							p.bargeInVAD.Reset()
							select {
							case p.interruptCh <- struct{}{}:
							default:
							}
						}
					}
					// else: still speaking within window, keep waiting
				} else if p.bargeInVAD.IsSpeaking() && p.speaking.Load() &&
					((emitsPartials() && hasPartialText.Load()) || (!emitsPartials() && time.Since(vadSpeechSince) >= finalsOnlyOnset)) {
					// Conditions met — start backchannel suppression window.
					// A finals-only provider opens the window on VAD alone,
					// since partial text never arrives to confirm the speech;
					// the interrupt still cannot fire until the window has
					// fully elapsed.
					bargeInPending = true
					bargeInStart = time.Now()
					bargeInHeld = false
					hasPartialText.Store(false)
					// Duck rather than cut: the caller hears the agent lower
					// its voice immediately, and it recovers if this turns out
					// to be a backchannel.
					p.maybeStartDuck()
					log.Println("[inbound] barge-in candidate, checking for backchannel...")
				}
			}
		}
	}
}

// listenHintTimeout bounds the "listen" request: the call's ears must not
// wait on it. Past it, the adaptive listener starts on its default.
const listenHintTimeout = 800 * time.Millisecond

// listenHint asks the agent what it knows of the caller's language, for the
// adaptive listener only (no other provider uses it, so none pays for it).
func (p *Pipeline) listenHint() stt.Hint {
	if p.cfg.STT.Provider != "adaptive" || p.llmClient == nil {
		return stt.Hint{}
	}
	h, ok := p.llmClient.(llm.ListenHinter)
	if !ok {
		return stt.Hint{}
	}
	ctx, cancel := context.WithTimeout(p.ctx, listenHintTimeout)
	defer cancel()
	language, region, err := h.ListenHint(ctx)
	if err != nil {
		log.Printf("[inbound] no caller hint for the listener: %v", err)
		return stt.Hint{}
	}
	return stt.Hint{Language: language, Region: region}
}
