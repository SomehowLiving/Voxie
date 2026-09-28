# The agent contract

Voxie owns the call: audio, hearing, turn-taking, interruptions, language
switching and the voice. **Your app decides what to say.** Voxie posts
each event of the call to one HTTP endpoint of yours (`[agent] url`), and
speaks what you send back.

This extends upstream StreamCore's "bring your own agent" contract
([`../bring-your-own-agent.md`](../bring-your-own-agent.md)) with the
`listen` and `greeting` request types and the `end_call` reply field.

## Request

`POST <[agent] url>`, `Content-Type: application/json`, and
`Authorization: Bearer <[agent] api_key>` if a key is set (env
`STREAMCORE_AGENT_API_KEY`).

```json
{
  "session_id": "9f2c…",
  "resource_id": "+919812345678",
  "type": "chat",
  "text": "मुझे दोबारा फ़ोन मत करना",
  "language": "hi",
  "interrupted_text": "…what the agent was saying when cut off",
  "summary": "…rolling digest of earlier turns"
}
```

- **`session_id`** is stable for one call. Key your call state on it.
- **`resource_id`** says who is on the call: the dialled number from a
  phone bridge, or whatever the client sent as `X-StreamCore-Resource-Id`.
  It's absent when unknown.
- **`language`** (on `chat`) is the language the caller spoke this turn,
  as an ISO 639-1 code, when the listener knows it. It's the adaptive
  listener's verdict: identified and corrected turns carry the identified
  language. Treat it as a strong hint, not a verdict on the whole call. A
  one-word "Hello" is labelled English whatever the caller speaks, so
  switch your reply language only on a real sentence.
- **`interrupted_text`** is present when the caller cut the agent off. It
  says what they actually heard, so don't assume the whole last reply
  landed.

## The four types

| `type` | When | Reply |
|---|---|---|
| `listen` | Once, before speech-to-text starts (adaptive listener only) | `{"language"?: "ta", "region"?: "IN"}` |
| `greeting` | The caller stayed silent for `greeting_delay_ms` | `{"text": "Hi Priya, …"}` |
| `chat` | The caller finished a turn | `{"text": "…", "end_call"?: true}` |
| `oneshot` | Background work (rolling summaries) | `{"text": ""}` is fine |

### `listen`

This asks what you know of the caller's language before they speak.
- `language`: their language on record, as an ISO 639-1 code.
- `region`: `"IN"` for an Indian number.

Both are optional, and an empty `{}` is a fine answer. The request has an
800ms timeout, and it's only a starting guess: Voxie corrects it from the
caller's first real sentence. Answer it without changing any call state.
A caller you don't know yet may be one you're about to claim.

### `greeting`

This asks for the opening line. It needs `pipeline.greeting_from_agent =
true`. It only comes if the caller stays silent. If they speak first
("Hello?"), you get a `chat` instead and should answer with your
introduction. Voxie never asks twice, but make a duplicate harmless
anyway: return `{"text": ""}` once you've greeted.

### `chat`

This is the caller's turn. Reply with what to say. With `"end_call": true`,
Voxie hangs up once that line has fully played. If the caller talks over
the goodbye, it's cancelled and you get their words as a new `chat`.

## Reply formats

By `Content-Type`:
- **`application/json`**: `{"text": "…", "end_call"?: bool}`. Buffered.
  Voxie still voices it one sentence at a time.
- **`text/event-stream`**: `data:` lines of text or `{"delta": "…"}`,
  spoken sentence by sentence as they arrive. Send `{"end_call": true}` as
  an event to end the call after the reply, and `[DONE]` to finish. Stream
  when your reply is ready in pieces, for example translated sentence by
  sentence: Voxie starts voicing the first while the rest is on its way.
- **`text/plain`**: chunked text, spoken as it arrives.

## Speaking the caller's language

- **Tag the language:** start each sentence with `[lang:xx]`, for example
  `[lang:mr] …`. Voxie voices a reply sentence by sentence, so an untagged
  later sentence would be guessed from its script. The voice router then picks the exact voice; Hindi and
  Marathi share a script, so text alone can't tell them apart. The tag
  never appears in transcripts.
- **Only answer in languages the voice can speak right now.** Read the
  voice router's `GET /health` → `languages`. If Sarvam is down, Tamil
  drops out of that list, and English is better than silence.
- **Numbers:** send amounts with the currency symbol ("₹4,999"). The
  voice router reads them in each language's words.

## A minimal agent

See [`quickstart/server.mjs`](../../quickstart/server.mjs):
about 60 lines handle all four types, with Groq replying in the caller's
language. It echoes if there's no key. Replace `think()` with your app.
