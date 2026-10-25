package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/llm"
	"github.com/streamcoreai/streamcore-server/internal/procstat"
	"github.com/streamcoreai/streamcore-server/internal/rag"
	"github.com/streamcoreai/streamcore-server/internal/tts"
)

// runAgent is the central orchestrator goroutine. It receives transcript
// events from STT, calls the LLM for responses, synthesizes TTS audio,
// and pushes PCM frames to the outbound sender. It handles barge-in
// interruption by cancelling in-progress responses.
func (p *Pipeline) runAgent() {
	for {
		select {
		case <-p.ctx.Done():
			return

		case ev := <-p.transcriptCh:
			// Forward all transcripts to client for display
			p.sendEvent(transcriptMsg{
				Type:  "transcript",
				Text:  ev.Text,
				Final: ev.Final,
			})

			if !ev.Final {
				continue
			}

			if p.isPassiveAcknowledgement(ev) {
				log.Printf("[agent] acknowledgement ignored (caller talking over agent): %q", ev.Text)
				continue
			}

			log.Printf("[agent] user: %s", ev.Text)
			if ev.Language != "" && len(strings.Fields(ev.Text)) > 2 {
				p.turnLanguage.Store(ev.Language)
			}
			p.closeCallerSpokeFirst.Do(func() { close(p.callerSpokeFirst) })
			text, language := ev.Text, ev.Language
			replaces := p.continuesLastTurn()
			if replaces {
				// The caller paused mid-sentence, the first part was answered,
				// and they went on before any of that reply was heard. Answer
				// the whole sentence, not its second half as an interruption:
				// live, "En fait, je préfère parler français." / "Quand va-t-il
				// arriver ?" got two replies, the first cut off.
				text = p.lastTurn.text + " " + ev.Text
				if utf8.RuneCountInString(p.lastTurn.text) > utf8.RuneCountInString(ev.Text) && p.lastTurn.language != "" {
					language = p.lastTurn.language
				}
				// The caller may have cut in on the reply's first word or two;
				// that's not an interruption to tell the app about.
				p.interruptedText.Store("")
				log.Printf("[agent] the caller went on before the reply was heard; answering both parts: %q", text)
			} else {
				// A turn that lands while the reply is still playing cuts it
				// off just as a barge-in does: the app hears what the caller
				// heard.
				p.captureHeard()
			}
			gen := p.supersedeResponse()
			p.lastTurn = answeredTurn{text: text, language: language, at: time.Now(), gen: gen}
			p.clocks.Store(gen, newTurnClock(ev, language))
			p.sendEvent(stateMsg{Type: "state", State: "thinking"})
			go func() {
				defer p.recoverPanic("respond")
				p.respond(gen, text, language, ev.TurnStart, replaces)
			}()

		case <-p.interruptCh:
			p.captureHeard()
			log.Println("[agent] interrupted (barge-in)")
			cutsLastTurn := p.responseGen.Load() == p.lastTurn.gen
			heard := p.ledger.heardFrames(p.lastTurn.gen)
			gen := p.supersedeResponse()
			if cutsLastTurn {
				p.lastTurn.cutAt, p.lastTurn.heardFrames, p.lastTurn.cutGen = time.Now(), heard, gen
			}
		}
	}
}

// continuationWindow bounds how long after a turn was answered (or its reply
// cut off) a new turn can still be its continuation: a pause mid-sentence,
// then the rest of it. Room for the reply hold (replyHoldMax) and the final
// that follows it: live, a Hindi second half landed 5.09s after the first.
const continuationWindow = 8 * time.Second

// continuationHeardMax is the most of a reply the caller can have heard and
// still be finishing their own sentence rather than answering the reply.
// Live, with a 1-1.5s pause mid-sentence the reply had started, and the
// caller going on cut it off after "", "Sure, I…", "Je…", "मुझे खेद…",
// "申し訳ありませんが、".
const continuationHeardMax = 75 // frames, ~1.5s

// answeredTurn is a caller turn that was sent for an answer.
type answeredTurn struct {
	text, language string
	at             time.Time
	gen            uint64
	// If the caller cut its reply off: when, how much of it they had
	// heard, and the generation the barge-in opened.
	cutAt       time.Time
	heardFrames int64
	cutGen      uint64
}

// continuesLastTurn reports whether a turn arriving now continues the last
// one: it came soon after, and the caller has heard at most the first word or
// two of that turn's reply, which hasn't finished -- or they cut in on its
// start and nothing else has happened since.
func (p *Pipeline) continuesLastTurn() bool {
	lt := p.lastTurn
	if lt.text == "" {
		return false
	}
	gen := p.responseGen.Load()
	switch {
	case gen == lt.gen:
		// The reply is still playing (or hasn't started): the new final cuts
		// it off. Live, "Mi número de pedido es" / "cuatro." landed on
		// "Lo siento,". A reply that finished playing was heard in full.
		if time.Since(lt.at) > continuationWindow {
			return false
		}
		heard := p.ledger.heardFrames(lt.gen)
		return heard == 0 || (p.ledger.unheard() && heard <= continuationHeardMax)
	case !lt.cutAt.IsZero() && gen == lt.cutGen:
		return time.Since(lt.cutAt) <= continuationWindow && lt.heardFrames <= continuationHeardMax
	}
	return false
}

// isPassiveAcknowledgement reports whether a completed turn is just the caller
// signalling "I'm still with you" over the agent's voice, rather than a turn
// that wants an answer.
//
// These arrive as ordinary finals, and the backchannel suppression in
// runInbound only guards the VAD barge-in path — it never sees them. So
// without this check an "okay" dropped mid-explanation starts a full LLM turn
// whose audio lands on top of the sentences still in flight.
func (p *Pipeline) isPassiveAcknowledgement(ev TranscriptEvent) bool {
	// Said into silence, an acknowledgement is a real (if minimal) turn — the
	// caller is handing the floor back and deserves a reply. Only one spoken
	// over the agent is passive.
	if !ev.OverAgentSpeech {
		return false
	}
	if !isAcknowledgementOnly(ev.Text) && !p.isMisScriptedShortReply(ev) {
		return false
	}

	// A confirmed barge-in already cut the agent off mid-sentence. Swallowing
	// the turn now would leave the caller in dead air, so answer it even
	// though the words themselves carry nothing.
	if interrupted, _ := p.interruptedText.Load().(string); interrupted != "" {
		return false
	}

	return true
}

// indicLanguages are the languages Sarvam's auto-detection mixes up on very
// short words.
var indicLanguages = map[string]bool{"hi": true, "bn": true, "ta": true, "te": true, "kn": true, "ml": true, "mr": true, "gu": true, "pa": true, "od": true}

// isMisScriptedShortReply reports a one- or two-word reply, spoken over the
// agent, that came back labelled as a different Indian language from the
// call's. Sarvam detects the language per utterance, and on a single word it
// often picks the wrong script: live, a Tamil caller's "சரி" came back as
// Kannada "ಸರಿ" and "ஆமா" as Gujarati "આમાં" -- a real Gujarati word, so it
// cut the agent off. A caller who really switches language says a sentence.
func (p *Pipeline) isMisScriptedShortReply(ev TranscriptEvent) bool {
	call, _ := p.turnLanguage.Load().(string)
	heard := baseLang(ev.Language)
	call = baseLang(call)
	if call == "" || heard == "" || heard == call {
		return false
	}
	if !indicLanguages[call] || !indicLanguages[heard] {
		return false
	}
	return len(strings.Fields(normalizedTranscriptText(ev.Text))) <= 2
}

// baseLang turns "ta-IN" into "ta".
func baseLang(code string) string {
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	return strings.ToLower(code)
}

// respond runs the LLM → TTS → outbound flow for a single user utterance.
// gen is the response generation issued by supersedeResponse; it scopes every
// mutation of shared speaking state to this response, so a barge-in or a newer
// turn cleanly takes ownership of the audio path.
func (p *Pipeline) respond(gen uint64, userText, language string, turnStart time.Time, replacesLastTurn bool) {
	respCtx, cancel := context.WithCancel(p.ctx)

	// Superseded between the turn being dispatched and this goroutine getting
	// scheduled — a newer turn already owns the audio path, and synthesizing
	// now would put two responses on outPCMCh at once.
	if !p.registerResponse(gen, cancel) {
		cancel()
		return
	}

	defer func() {
		cancel()
		p.finishResponse(gen)
		p.clocks.Delete(gen)
	}()
	p.ledger.reset(gen)
	clock := p.clockFor(gen)

	// NOTE: speaking flag is set in synthesizeSentences when first TTS audio
	// is actually produced, not here. This prevents false barge-in triggers
	// during LLM thinking time.

	// Build LLM input with interruption context if the user barged in.
	// For short redirections ("no", "wait", "stop") we focus the LLM on the
	// user's intent without dumping the previous response — like a human would.
	// For longer interruptions we include brief prior context so the LLM can
	// pick up naturally.
	// turn.Text stays the caller's words throughout; turn.Prompt accumulates the
	// same context inline for providers that can only read one message.
	turn := llm.Turn{Text: userText, Prompt: userText, Language: language, ReplacesLastTurn: replacesLastTurn}
	if interrupted, _ := p.interruptedText.Load().(string); interrupted != "" {
		p.interruptedText.Store("")
		trimmedUser := strings.TrimSpace(strings.ToLower(userText))
		words := strings.Fields(trimmedUser)
		if len(words) <= 2 {
			// Short interruption — user is redirecting, not adding new info.
			// Just let the LLM know it was cut off so it doesn't repeat itself.
			turn.Prompt = fmt.Sprintf("[You were interrupted mid-response. The user said: '%s'. Respond to what they said — do not continue or repeat your previous answer.]", userText)
		} else {
			// Longer interruption — include brief context of what agent was saying.
			// The tail of what was heard is where the caller cut in.
			context := interrupted
			if r := []rune(context); len(r) > 200 {
				context = "…" + string(r[len(r)-200:])
			}
			turn.Prompt = fmt.Sprintf("[You were interrupted. The user heard only: '%s'. The user said: '%s'. Address what the user said.]", context, userText)
		}
		turn.InterruptedText = interrupted
	}

	timing := &procstat.TurnTiming{}
	if !turnStart.IsZero() {
		timing.EndpointMs = time.Since(turnStart).Milliseconds()
	}

	// Retrieve relevant context via RAG before calling the LLM. Turns with no
	// content-bearing words ("okay, sure, thanks") can't anchor a vector
	// search, so skipping them removes a network round trip from the turn.
	if p.ragClient != nil {
		if skip, reason := shouldSkipRAG(p, userText); skip {
			timing.RAGSkipped, timing.RAGSkipReason = true, reason
			log.Printf("[agent] RAG skipped: %s", reason)
		} else {
			query := p.ragSearchQuery(userText)
			var chunks []string
			var err error

			// A prefetch started during the turn-merge window has already paid
			// the embedding + vector-search latency.
			if st := p.takeRAGPrefetch(query); st != nil {
				chunks, err = st.await(respCtx)
				timing.RAGPrefetched = true
				timing.EmbeddingMs, timing.VectorSearchMs = st.timing.EmbedMs, st.timing.QueryMs
			} else {
				rt := &rag.Timing{}
				chunks, err = p.ragClient.Search(rag.WithTiming(respCtx, rt), query, 0)
				timing.EmbeddingMs, timing.VectorSearchMs = rt.EmbedMs, rt.QueryMs
			}

			if err != nil {
				log.Printf("[agent] RAG search error: %v", err)
			} else if len(chunks) > 0 {
				timing.RAGChunks = len(chunks)
				turn.Context = chunks
				turn.Prompt = fmt.Sprintf("[Context:\n%s]\n\nUser: %s", strings.Join(chunks, "\n---\n"), turn.Prompt)
			}
		}
	}

	// Prepend the rolling summary so facts from early in a long call survive
	// after the LLM's own history window has dropped them.
	if summary := p.currentRollingSummary(); summary != "" {
		turn.Summary = summary
		if block := summaryContextBlock(summary); block != "" {
			turn.Prompt = block + turn.Prompt
		}
	}

	// When the caller's speech came through garbled, tell the model to ask
	// rather than guess. Escalates with consecutive low-confidence turns.
	if note := p.misunderstandingNote(userText); note != "" {
		turn.Note = note
		turn.Prompt += note
	}

	p.lastAgentText.Store("")
	if p.transcriptLog != nil {
		if replacesLastTurn {
			p.transcriptLog.ReplaceLastTurn(userText)
		} else {
			p.transcriptLog.Add("user", userText)
		}
	}
	p.maybeRefreshRollingSummary()

	// Channel decouples LLM sentence production from TTS synthesis so the
	// LLM stream isn't blocked while waiting for audio.
	sentences := make(chan string, 8)
	var responseText strings.Builder

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer p.recoverPanic("synthesizeSentences")
		p.synthesizeSentences(respCtx, gen, sentences, turnStart)
	}()

	llmFirstToken := true
	_, err := p.llmClient.Chat(respCtx, turn,
		func(chunk string) {
			if clock != nil {
				stampOnce(&clock.llmFirst)
			}
			if llmFirstToken && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				llmFirstToken = false
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "llm_first_token",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
			}
			// Accumulate response text for interruption context. Delivery tags
			// are stripped: this text is quoted back to the LLM verbatim when
			// the user barges in, and feeding it "[warm] ..." would teach the
			// model that tags are part of normal prose.
			acc := chunk
			if prev, _ := p.lastAgentText.Load().(string); prev != "" {
				acc = prev + chunk
			}
			p.lastAgentText.Store(tts.StripVoiceTags(acc))
			responseText.WriteString(chunk)
			p.sendEvent(responseMsg{
				Type: "response",
				Text: chunk,
			})
		},
		func(sentence string) {
			if clock != nil {
				stampOnce(&clock.firstSentence)
			}
			select {
			case sentences <- sentence:
			case <-respCtx.Done():
			}
		},
	)
	close(sentences)

	if err != nil && respCtx.Err() == nil {
		log.Printf("[agent] LLM error: %v", err)
	}
	if ender, ok := p.llmClient.(llm.CallEnder); ok && ender.TakeEndCall() {
		log.Printf("[agent] backend asked to end the call after this response")
		p.endCallGen.Store(gen + 1)
	}

	wg.Wait()

	if p.transcriptLog != nil {
		if txt, _ := p.lastAgentText.Load().(string); txt != "" {
			p.transcriptLog.Add("agent", txt)
		}
	}
	if err == nil {
		p.emitAssistantResponseCompleted(gen, userText, tts.StripVoiceTags(responseText.String()))
	}
	if p.cfg.Pipeline.Debug {
		timing.Log()
	}
}

// replyHoldMax bounds how long a reply's first audio waits for a caller who
// is still talking. Live, the caller went on after a pause ("I want to book
// a table." … "for four people") and the reply started over them; barge-in
// only fires on speech that starts while the agent talks, so nothing stopped
// it. Bounded, so a noisy line costs at most this.
var replyHoldMax = 4 * time.Second

// staleReplyQuiet: once the caller has talked during the hold, the reply
// answers a turn they hadn't finished, and their final is on its way --
// live, 1.5-1.8s after they stopped. Wait for it (it supersedes this reply)
// rather than start the outdated reply and be cut off by it.
var staleReplyQuiet = 1800 * time.Millisecond

// callerTalkingQuiet: no caller sound for this long and they've paused.
// The onset VAD can read a gap between words as that, so partial words from
// the listener also count: live, the hold released mid-sentence on the VAD
// alone. Partials stream every few hundred ms while someone talks.
const (
	callerTalkingQuiet   = 400 * time.Millisecond
	callerPartialsRecent = 800 * time.Millisecond
)

// callerTalking reports whether the caller is speaking right now.
func (p *Pipeline) callerTalking() bool {
	if n := p.lastCallerSoundAt.Load(); n != 0 && time.Since(time.Unix(0, n)) < callerTalkingQuiet {
		return true
	}
	n := p.lastPartialAt.Load()
	return n != 0 && time.Since(time.Unix(0, n)) < callerPartialsRecent
}

// waitForCallerPause holds a reply's first audio while the caller is talking.
// If their words become a turn meanwhile, it supersedes this reply (and is
// joined to it: nothing of it was heard). False if ctx ended.
func (p *Pipeline) waitForCallerPause(ctx context.Context) bool {
	start := time.Now()
	logged := false
	var lastTalking time.Time
	for {
		talking := p.callerTalking()
		if talking {
			lastTalking = time.Now()
		}
		quietEnough := !talking && (lastTalking.IsZero() || time.Since(lastTalking) >= staleReplyQuiet)
		if quietEnough || time.Since(start) >= replyHoldMax {
			if logged && quietEnough {
				log.Printf("[agent] the caller paused; reply starts after %s", time.Since(start).Round(10*time.Millisecond))
			} else if logged {
				log.Printf("[agent] the caller is still talking after %s; reply starts anyway", replyHoldMax)
			}
			return ctx.Err() == nil
		}
		if !logged {
			logged = true
			log.Printf("[agent] holding the reply: the caller is still talking")
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ttsStallTimeout: a voice that sends nothing for this long, before or during
// a sentence, has stalled; the sentence is skipped rather than leaving the
// caller in silence. Generous, since the voice router returns a whole long
// sentence at once.
var ttsStallTimeout = 8 * time.Second

// ttsGapTimeout: once a sentence's audio is streaming, a gap this long is a
// stall. Live, Cartesia sent a quarter second of a sentence and then nothing,
// with no error; the caller sat through the full 8 s.
var ttsGapTimeout = 2 * time.Second

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// synthesizeSentences reads sentences from the channel, calls TTS for each,
// and pushes the resulting PCM frames into outPCMCh for the sender goroutine.
func (p *Pipeline) synthesizeSentences(ctx context.Context, gen uint64, sentences <-chan string, turnStart time.Time) {
	emittedTTSTiming := false
	emittedSpeaking := false

	// leftover holds samples that did not fill a complete frame. Streaming
	// chunks arrive on arbitrary byte boundaries, so the remainder carries
	// into the next chunk rather than being padded with silence mid-utterance,
	// which would insert an audible click every chunk.
	var leftover []int16
	talkspurtSent := false
	var seg *heardSegment // the sentence being synthesized, for the playback ledger

	clock := p.clockFor(gen)
	emitFrame := func(frame PCMFrame) bool {
		if !emittedSpeaking {
			held := time.Now()
			if !p.waitForCallerPause(ctx) {
				return false
			}
			if clock != nil {
				clock.holdNs.Store(int64(time.Since(held)))
			}
		}
		select {
		case p.outPCMCh <- frame:
			if !emittedSpeaking {
				clock.firstAudio()
			}
			if frame.heard != nil {
				frame.heard.frames.Add(1)
			}
			if !emittedTTSTiming && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				emittedTTSTiming = true
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "tts_first_byte",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
			}
			// Send "speaking" state when the first audio frame is actually
			// produced, not when respond() begins. This keeps the "thinking"
			// state visible while the LLM and TTS are working.
			if !emittedSpeaking {
				emittedSpeaking = true
				p.setSpeaking(true)
				p.sendEvent(stateMsg{Type: "state", State: "speaking"})
			}
			return true
		case <-ctx.Done():
			return false
		}
	}

	for sentence := range sentences {
		if ctx.Err() != nil {
			return
		}

		// The LLM may prefix a sentence with a delivery tag ([warm], [calm], …)
		// per the voice-output rules. The tag is stripped here — the caller
		// never hears it — and mapped to provider voice controls when the
		// configured TTS supports them.
		controls, clean := tts.ParseVoiceTag(sentence)
		if seg != nil {
			seg.done.Store(true)
		}
		seg = p.ledger.sentence(gen, clean)

		// Stream so audio starts playing as soon as the first PCM chunk
		// arrives, instead of waiting for the whole sentence to synthesize.
		sctx, scancel := context.WithCancel(ctx)
		var stream <-chan tts.StreamChunk
		var err error
		if cs, ok := p.ttsClient.(tts.ControllableStreamer); ok && !controls.IsZero() {
			stream, err = cs.SynthesizeStreamWithControls(sctx, clean, controls)
		} else {
			stream, err = p.ttsClient.SynthesizeStream(sctx, clean)
		}
		if err != nil {
			scancel()
			if ctx.Err() == nil {
				log.Printf("[agent] TTS stream error: %v", err)
			}
			return
		}

		// A voice that stops sending without an error would otherwise leave
		// the caller in silence until they hang up. Only time spent waiting
		// on the voice counts, not time spent pacing audio out.
		stall := time.NewTimer(ttsStallTimeout)
		received := 0
		for {
			var chunk tts.StreamChunk
			var ok bool
			select {
			case chunk, ok = <-stream:
			case <-stall.C:
				wait := ttsStallTimeout
				if received > 0 {
					wait = ttsGapTimeout
				}
				log.Printf("[agent] TTS stalled: no audio for %s after %d bytes; skipping the sentence %q",
					wait, received, truncateRunes(clean, 60))
				scancel()
				ok = false
			case <-ctx.Done():
				ok = false
			}
			if !ok {
				break
			}
			if chunk.Err != nil {
				scancel()
				if ctx.Err() == nil {
					log.Printf("[agent] TTS error: %v", chunk.Err)
				}
				return
			}
			if ctx.Err() != nil {
				scancel()
				return
			}
			received += len(chunk.PCM)

			samples := append(leftover, audio.Linear16BytesToPCM(chunk.PCM)...)
			leftover = nil

			// Emit complete frames only; the partial tail waits for the next
			// chunk, or is flushed once every sentence is done.
			i := 0
			for ; i+audio.FrameSize <= len(samples); i += audio.FrameSize {
				frame := PCMFrame{
					Samples:      make([]int16, audio.FrameSize),
					NewTalkspurt: !talkspurtSent,
					heard:        seg,
				}
				copy(frame.Samples, samples[i:i+audio.FrameSize])
				talkspurtSent = true
				if !emitFrame(frame) {
					scancel()
					return
				}
			}
			if i < len(samples) {
				leftover = append([]int16{}, samples[i:]...)
			}
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(ttsGapTimeout)
		}
		stall.Stop()
		scancel()
		if ctx.Err() != nil {
			return
		}
	}

	// Flush the final partial frame, padded with silence. Only the very end of
	// the turn gets padding, so the click lands where the agent stops talking.
	if len(leftover) > 0 {
		frame := PCMFrame{
			Samples:      make([]int16, audio.FrameSize),
			NewTalkspurt: !talkspurtSent,
			heard:        seg,
		}
		copy(frame.Samples, leftover)
		emitFrame(frame)
	}
	if seg != nil {
		seg.done.Store(true)
	}
}

// captureHeard records, for the caller's next turn, what they heard of a
// reply that is being cut off. Nothing is recorded if the reply had finished
// playing: then it wasn't an interruption.
func (p *Pipeline) captureHeard() {
	if !p.ledger.unheard() {
		return
	}
	heard := p.ledger.heard()
	p.interruptedText.Store(heard)
	log.Printf("[agent] cut off; the caller heard: %q", heard)
}

// waitOutboundDrain waits until the outbound PCM channel is empty, meaning
// runSender has picked up all queued frames. It gives up as soon as
// generation gen is superseded — the frames it would be waiting on have been
// discarded, and the new response is already filling the channel behind them.
func (p *Pipeline) waitOutboundDrain(gen uint64) {
	p.waitOutboundEmpty(func() bool { return p.responseGen.Load() != gen })
}

// waitOutboundEmpty polls the outbound PCM channel until it is empty, meaning
// runSender has picked up all queued frames. It polls briefly with a timeout
// to avoid blocking forever, and returns early if superseded reports true.
func (p *Pipeline) waitOutboundEmpty(superseded func() bool) {
	deadline := time.After(5 * time.Second)
	for {
		if superseded() {
			return
		}
		if len(p.outPCMCh) == 0 {
			// Frames consumed by sender; add a small grace period for the
			// last few RTP packets to reach the client and be played.
			time.Sleep(300 * time.Millisecond)
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// supersedeResponse retires whatever the agent is currently saying so a new
// turn can start on a clear audio path, and returns the generation that the
// incoming response owns.
//
// All three steps are load-bearing:
//
//   - bumping the generation invalidates the outgoing response, so that when
//     it finally unwinds it leaves speaking / responseCancel / the client
//     state alone — those now belong to its successor
//   - cancelling stops the LLM and TTS from producing any MORE audio
//   - draining discards audio the old response ALREADY queued; cancellation
//     does nothing about the frames sitting in outPCMCh (up to outPCMChSize,
//     ~2s worth), and leaving them there is what makes the caller hear the
//     previous answer keep playing while the new one stacks up behind it
func (p *Pipeline) supersedeResponse() uint64 {
	gen := p.responseGen.Add(1)
	p.cancelResponse()
	p.drainOutbound()
	// The queued audio is gone, so the agent is no longer speaking. The next
	// response re-raises this when its first frame actually reaches the wire.
	p.setSpeaking(false)
	return gen
}

// backchannelGrace is how long after the agent's last frame an incoming final
// still counts as having been spoken over it. STT endpointing sits on a final
// for ~600ms after the caller stops, so an "okay" landing on the agent's
// closing words reaches the agent goroutine well after speaking cleared.
const backchannelGrace = 1500 * time.Millisecond

// setSpeaking updates the speaking flag, stamping the moment agent speech
// ended so agentSpeechRecent can cover the STT endpointing lag.
func (p *Pipeline) setSpeaking(speaking bool) {
	if prev := p.speaking.Swap(speaking); prev && !speaking {
		p.speechEndedAt.Store(time.Now().UnixNano())
	}
}

// agentSpeechRecent reports whether the agent is speaking, or stopped recently
// enough that a caller utterance arriving now was plausibly spoken over it.
func (p *Pipeline) agentSpeechRecent() bool { return p.agentSpeechWithin(backchannelGrace) }

// finalsOnlyBackchannelGrace is backchannelGrace for a listener that sends
// only finished sentences, which land seconds after the words.
const finalsOnlyBackchannelGrace = 3 * time.Second

// agentSpeechWithin reports whether the agent is speaking or stopped less
// than grace ago.
func (p *Pipeline) agentSpeechWithin(grace time.Duration) bool {
	if p.speaking.Load() {
		return true
	}
	endedAt := p.speechEndedAt.Load()
	return endedAt != 0 && time.Since(time.Unix(0, endedAt)) < grace
}

// registerResponse installs cancel as the cancel func for generation gen,
// reporting false if gen has already been superseded.
func (p *Pipeline) registerResponse(gen uint64, cancel context.CancelFunc) bool {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	if p.responseGen.Load() != gen {
		return false
	}
	if p.responseCancel != nil {
		p.responseCancel()
	}
	p.responseCancel = cancel
	p.responseCancelGen = gen
	return true
}

// finishResponse releases the shared speaking state owned by generation gen.
//
// A superseded response returns without touching anything. This guard is the
// fix for responses talking over each other: a cancelled response unwinds
// asynchronously (its LLM stream has to return first), by which point its
// successor is already registered, and the unconditional cleanup this
// replaced would nil out the successor's cancel func — leaving it immune to
// the next barge-in and audible underneath the turn after it.
func (p *Pipeline) finishResponse(gen uint64) {
	p.responseMu.Lock()
	if p.responseCancelGen == gen {
		p.responseCancel = nil
	}
	stale := p.responseGen.Load() != gen
	p.responseMu.Unlock()
	if stale {
		return
	}

	p.setSpeaking(false)

	// Wait for the outbound PCM queue to drain before signalling "listening",
	// so the sender has RTP-sent every frame before the client is told the
	// agent finished speaking.
	p.waitOutboundDrain(gen)

	// The drain can block for seconds; a new turn may have started inside it,
	// in which case "listening" would contradict the response now speaking.
	if p.responseGen.Load() != gen {
		return
	}
	p.sendEvent(stateMsg{Type: "state", State: "listening"})

	// The backend ended the call with this response, and every frame of it
	// has now been sent. Ending the pipeline closes the peer, which a SIP
	// bridge reads as the far end hanging up -- after the goodbye, not over
	// it. A caller who barged in superseded gen above, so they're answered
	// instead of cut off.
	if p.endCallGen.Load() == gen+1 {
		log.Printf("[agent] final response played; ending the call")
		p.cancel()
	}
}

// cancelResponse cancels any in-progress LLM/TTS response.
func (p *Pipeline) cancelResponse() {
	p.responseMu.Lock()
	if p.responseCancel != nil {
		p.responseCancel()
		p.responseCancel = nil
	}
	p.responseMu.Unlock()
}

// drainOutbound discards any queued outbound PCM frames, and any the far
// side has buffered but not yet played (a phone stream).
func (p *Pipeline) drainOutbound() {
	if c, ok := p.media.(MediaClearer); ok {
		c.Clear()
	}
	for {
		select {
		case <-p.outPCMCh:
		default:
			return
		}
	}
}

// greetingRace reports whether the delayed greeting should actually fire:
// true if the delay elapsed with the caller still silent, false if the
// caller's first turn (runAgent's closeCallerSpokeFirst) or pipeline
// shutdown won the race first. A pure, allocation-free decision on purpose
// -- kept separate from greetAfterDelay so it's testable without any of
// greet()'s TTS/response-tracking machinery.
func greetingRace(ctx context.Context, callerSpokeFirst <-chan struct{}, delay time.Duration) bool {
	select {
	case <-time.After(delay):
		return true
	case <-callerSpokeFirst:
		// The caller spoke before the delay elapsed -- runAgent is already
		// answering what they said; the scripted greeting would talk over it.
		return false
	case <-ctx.Done():
		return false
	}
}

// The default for pipeline.greeting_speech_settle_ms, and the longest a
// greeting is held for a caller who keeps making sound without a turn ever
// arriving (continuous line noise).
const (
	defaultGreetingSpeechSettle = 5 * time.Second
	greetingHoldCap             = 20 * time.Second
)

func (p *Pipeline) greetingSpeechSettle() time.Duration {
	if ms := p.cfg.Pipeline.GreetingSpeechSettleMs; ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultGreetingSpeechSettle
}

// greetAfterDelay holds the greeting for up to delay, then speaks it --
// unless the caller spoke first (their finished turn arrived, or they're
// audibly mid-utterance and it arrives shortly) or the pipeline stopped. The
// text is resolved only once the greeting is actually going ahead, so an
// agent that supplies it is never asked for one that won't be spoken.
func (p *Pipeline) greetAfterDelay(delay time.Duration) {
	defer p.greetingDecided.Store(true)
	if !greetingRace(p.ctx, p.callerSpokeFirst, delay) {
		return
	}
	lastSound := func() time.Time {
		if n := p.lastCallerSoundAt.Load(); n != 0 {
			return time.Unix(0, n)
		}
		return time.Time{}
	}
	if awaitCallerWords(p.ctx, p.callerSpokeFirst, lastSound, p.greetingSpeechSettle(), greetingHoldCap, 100*time.Millisecond) {
		return
	}
	p.greetResolved()
}

// awaitCallerWords runs once the greeting delay has elapsed with no finished
// turn. If the caller has made sound recently, their words may still be in
// flight -- a turn only reaches runAgent after they stop talking and STT
// catches up -- so it waits for that turn rather than greeting over it. It
// gives up (greet after all: that sound was noise, not words) once the caller
// has been quiet for settle, or holdCap has passed. Returns true if a turn
// arrived or the pipeline stopped, i.e. the greeting should not be spoken.
func awaitCallerWords(ctx context.Context, turnArrived <-chan struct{}, lastSound func() time.Time, settle, holdCap, poll time.Duration) bool {
	deadline := time.Now().Add(holdCap)
	for {
		select {
		case <-turnArrived:
			return true
		case <-ctx.Done():
			return true
		default:
		}
		last := lastSound()
		if last.IsZero() || time.Since(last) >= settle || time.Now().After(deadline) {
			return false
		}
		select {
		case <-turnArrived:
			return true
		case <-ctx.Done():
			return true
		case <-time.After(poll):
		}
	}
}

// greetResolved speaks whatever resolveGreeting picks, if anything.
func (p *Pipeline) greetResolved() {
	text := resolveGreeting(p.ctx, p.cfg.Pipeline.GreetingFromAgent, p.llmClient, p.greetingText())
	if text == "" {
		return
	}
	// The agent round trip takes time; a caller who started talking during
	// it gets answered by runAgent instead of talked over by the greeting.
	select {
	case <-p.callerSpokeFirst:
		return
	default:
	}
	p.greet(text)
}

// resolveGreeting picks the call's opening line: the LLM client's own, when
// greeting_from_agent is on and the client implements llm.Greeter and
// returns something; otherwise the static greeting. "" means say nothing.
func resolveGreeting(ctx context.Context, fromAgent bool, client llm.Client, static string) string {
	if fromAgent {
		if g, ok := client.(llm.Greeter); ok {
			text, err := g.Greeting(ctx)
			if err == nil && strings.TrimSpace(text) != "" {
				return text
			}
			if err != nil {
				log.Printf("[agent] greeting from agent failed, using static greeting: %v", err)
			}
		}
	}
	return static
}

// greet synthesizes the greeting text via TTS and pushes PCM to outPCMCh.
// This runs once, right after the pipeline starts.
func (p *Pipeline) greet(text string) {
	log.Printf("[agent] greeting: %s", text)
	// NOTE: speaking flag and "speaking" state event are set inside
	// synthesizeSentences when TTS audio is actually produced.

	// The greeting goes through the same ownership handshake as a response so
	// that a caller who starts talking over it can actually cut it off. Run on
	// the pipeline context instead, and cancelResponse has nothing to cancel:
	// the greeting keeps synthesizing underneath the first real answer.
	gen := p.responseGen.Load()
	ctx, cancel := context.WithCancel(p.ctx)
	if !p.registerResponse(gen, cancel) {
		cancel()
		return
	}
	defer func() {
		cancel()
		p.finishResponse(gen)
	}()

	// Recorded like any response's text, so a barge-in on the greeting
	// captures it as interruptedText. Without it, a caller who cut the
	// greeting off with "yeah, okay" had their turn dropped as a passive
	// acknowledgement -- leaving dead air after the greeting was stopped.
	p.lastAgentText.Store(tts.StripVoiceTags(text))
	p.ledger.reset(gen)

	sentences := make(chan string, 1)
	sentences <- text
	close(sentences)

	p.synthesizeSentences(ctx, gen, sentences, time.Time{})
}

// greetingText returns the appropriate greeting based on call direction.
// For outbound calls it prefers greeting_outgoing, falling back to greeting.
func (p *Pipeline) greetingText() string {
	if p.direction == "outbound" && p.cfg.Pipeline.GreetingOutgoing != "" {
		return p.cfg.Pipeline.GreetingOutgoing
	}
	return p.cfg.Pipeline.Greeting
}
