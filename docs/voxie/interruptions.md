# Interruptions: what works, measured

A caller who talks over the agent should stop it; one who says "yeah" or
"haan" while listening should not; and the agent's own voice coming back
through a speakerphone must never count as the caller. These were measured
on 2026-10-13 with scripted callers (ElevenLabs and Sarvam voices) over
WebRTC to a local Voxie, Deepgram and Sarvam listening, the agent's audio
fed back at −12 dB for the echo case. The test scripts aren't in this
repository yet.

## Results

| Scenario | Before (Oct 12) | After (Oct 13) |
|---|---|---|
| Talk over the agent ~1.5 s into its reply ("Wait, stop…") | stopped after 0.2–2.8 s | **0.11 s** |
| Talk over ~4 s in ("Sorry to cut in…") | 1.7–2.5 s | **1.4 s** |
| Change of mind ("No wait, make it Wednesday") | 1.2–1.9 s | **1.1 s** |
| Backchannels in English ("yeah", "okay", "mm-hmm") | ignored | ignored |
| Backchannels in Hindi ("हाँ", "अच्छा", "हाँ जी") | each stopped the agent and got a reply | **ignored** |
| Backchannels in Tamil, on Sarvam ("சரி", "ஆமா") | — | **ignored**, even when Sarvam wrote them in Kannada or Gujarati script |
| Agent's own voice leaking back | the agent answered itself | **dropped as echo** |

The remaining ~1 s in the later cases is how long the speech-to-text takes
to send the caller's first words; Voxie stops as soon as they arrive.

## How it works

- **Stopping on words.** A barge-in is confirmed by the caller's words, not
  loudness alone: partial text that isn't the agent's own proves the sound
  is the caller. Clear interruptions ("wait", "stop", "no", "sorry",
  "excuse me", and "रुकिए", "இருங்க", "待って", "подождите"…) stop it at
  once; acknowledgements wait a 600 ms window.
- **Backchannels** are recognised in their own scripts in all 32 languages
  Voxie hears. A turn is ignored only if every word is one: "हां, मुझे अपना
  बिल चाहिए" is still a turn.
- **Echo.** A transcript that is mostly the agent's own recent words, while
  it speaks or just after, is its echo and is dropped.
- **What the caller heard.** Voxie counts the audio actually sent for each
  sentence; when the caller cuts in, the next request's `interrupted_text`
  is the sentences that played plus the played part of the cut one, ending
  at a word with "…". The cut-off word is an estimate (an even pace is
  assumed within a sentence).

## Limits

- Synthetic callers on one machine; not yet measured on phone lines or with
  real people.
- On Sarvam (which sends no partial words), interruption is decided by
  loudness and a 300 ms onset; it's less tested.
- No semantic end-of-turn model.
