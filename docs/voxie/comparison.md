# How Voxie compares

As of October 2026. Voxie is a call runtime: it listens, takes turns, handles
interruptions, picks the speech-to-text for the caller's language, and speaks
the reply. Voice and speech-to-text providers (Deepgram, Sarvam, Cartesia,
ElevenLabs, AssemblyAI, Groq) are parts it uses, not alternatives to it.
The comparable products are other call runtimes: hosted platforms (Vapi,
Retell, ElevenLabs Agents, Bland) and open-source frameworks (LiveKit
Agents, Pipecat).

Everything below cites the other projects' own documentation as of the date
above. Corrections are welcome: open an issue.

## Capabilities

| | Voxie | LiveKit Agents | Pipecat | Vapi | Retell |
|---|---|---|---|---|---|
| Open source | ✓ Apache-2.0 | ✓ Apache-2.0 | ✓ BSD-2 | – | – |
| Self-hosted | ✓ | ✓ | ✓ | – | – |
| Choose speech-to-text, LLM and voice providers | ✓ | ✓ | ✓ | ✓ | ✓ (most) |
| Switch speech-to-text by the caller's language mid-call | ✓ built in¹ | example code² | build it yourself³ | docs example | not documented |
| Backchannels ("haan", "சரி", "はい") don't interrupt | ✓ in 32 languages, native scripts¹ | adaptive interruption model (LiveKit Cloud)⁴ | word-count rules | acknowledgement phrase list | interruption-sensitivity setting |
| Tell the app what the caller heard when cut off | ✓ from audio actually played (estimated within a sentence)¹ | ✓ truncated to what was heard⁴ | via word timestamps where the voice provides them | not documented | not documented |
| Server-side guard against the agent's own echo | ✓ (transcript and loudness)¹ | left to the client⁵ | left to the client⁵ | not documented | not documented |
| Semantic end-of-turn model | – (silence and word rules) | ✓ turn detector⁴ | ✓ Smart Turn | ✓ | ✓ |
| Turn model's licence | – (no model) | v1-mini: open weights under the LiveKit Model License (LiveKit Agents only); v1: hosted, metered for self-hosted agents⁶ | Smart Turn: BSD-2, weights and training data⁷ | – | – |
| Phone calls | Twilio Media Streams (early) | LiveKit SIP | Twilio, Telnyx, Plivo, Exotel, … | ✓ mature | ✓ mature |
| Per-turn latency dashboard / tracing | per-turn [call reports](call-reports.md) and `voxie report`; no dashboard or tracing | ✓ | ✓ OpenTelemetry | ✓ | ✓ |
| Failover | speech-to-text and voice chains, voices ordered by the sentence's language | ✓ fallback adapters | service switcher | ✓ fallback plans | partial |

¹ Measured: see [languages](languages.md) and [interruptions](interruptions.md).
² docs.livekit.io / livekit.com blog "multilingual voice agent", Feb 2026.
³ Pipecat issue #2989. ⁴ docs.livekit.io/agents/logic/turns.
⁵ Pipecat issue #670, LiveKit issue #315.
⁶ huggingface.co/livekit/turn-detector (LICENSE) and livekit.com/pricing, October 2026.
⁷ github.com/pipecat-ai/smart-turn.

## Where Voxie is behind

- **Production use.** Most results come from scripted callers (synthetic
  voices); there's one real phone call so far.
- **Its own voices.** Kokoro and Piper (self-hosted) sound less natural than
  Cartesia or ElevenLabs; you can use those instead (see [voices](voices.md)).
- **Telephony** is early: Twilio only, no SIP in this repository.
- **Turn detection** uses silence and word rules, not a turn model.
- **Reliability:** speech-to-text and the voice fail over to the next
  provider, but if your agent errors or times out, the caller hears silence
  for that turn.
- **Observability:** per-turn timing and [call reports](call-reports.md);
  no dashboards or tracing.
- **Scale:** single server, not load-tested.
- **Known language gaps:** see [languages](languages.md#known-gaps).
