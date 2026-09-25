# Voxie

**Real-time voice agents that speak the caller's language.** Voxie runs the
call: it listens first, handles interruptions, hears 21 languages, speaks
17, and switches speech-to-text mid-call when the caller's language needs
it. Your app only decides what to say.

Built on [StreamCore](https://github.com/streamcoreai/streamcore-server)
(Apache 2.0). See [NOTICE](NOTICE) for what Voxie changes.

## What you get

- **Listen first.** The agent waits for the caller to speak before
  greeting. If they say "Hello?", it answers them instead of talking over
  them.
- **Real barge-in.** Talk over the agent and it drops its volume at once,
  then stops if you really mean it. "Yeah, okay" doesn't stop it, in six
  languages.
- **A listener per call** ([how](docs/voxie/how-it-works.md#choosing-the-listener-the-adaptive-listener)).
  - Deepgram hears most callers. A turn it may have misheard is
    identified by language and moved to the listener that hears it:
    Sarvam for Tamil, Telugu, Bengali…, and Deepgram fixed for Chinese,
    Korean, Arabic, Turkish.
  - The switch happens on the caller's first real sentence, and that turn
    isn't lost.
- **A voice for every language it speaks.** Kokoro on your GPU for
  English, Spanish, French, Italian, Portuguese, Japanese and Chinese
  (~130ms a sentence). Sarvam for ten Indian languages.
- **Failover at every layer.** Each listener is a failover chain. The
  voice has a breaker and a health endpoint. A failed language check
  keeps the turn as heard. A provider going down degrades the call; it
  doesn't end it.
- **One small contract for your app.** Four request types over HTTP
  ([agent contract](docs/voxie/agent-contract.md)). Every turn arrives
  with the language it was heard in, so your app can answer in it
  without its own detection.

## Quick start

You need keys for Deepgram, Groq and Sarvam (AssemblyAI is optional), and
an NVIDIA GPU for the voice router.

```bash
cp .env.example .env          # add your keys
```

**Linux (Docker Engine):**

```bash
docker compose up --build     # server :8080, voice router :8300, quickstart :8400
```

**Docker Desktop (Windows, WSL2, macOS).** Docker's "host network" is its
own VM, not your machine. So run the voice router in Docker, and the
server (Go 1.25+) and quickstart (Node 20+) natively:

```bash
docker compose up -d --build voice-router
go build -o voxie . && cp configs/voxie.toml config.toml
set -a && . ./.env && set +a && ./voxie &
node examples/quickstart/server.mjs
```

Then open **http://localhost:8400**, click **Start call**, and speak any
language. The quickstart's demo agent replies in your language with Groq,
and echoes you if `GROQ_API_KEY` is unset. The voice router can also run
without Docker: see [voice-router/README.md](voice-router/README.md).

## Use it in your project

1. Point `[agent] url` in [`configs/voxie.toml`](configs/voxie.toml) at
   your app.
2. Answer the [four request types](docs/voxie/agent-contract.md): `listen`,
   `greeting`, `chat` and `oneshot`. The demo agent in
   [`examples/quickstart/server.mjs`](examples/quickstart/server.mjs)
   shows all four in ~60 lines.
3. For phone calls, see [Phone calls](#phone-calls) below.

## Phone calls

**Twilio, built in** (Twilio Media Streams). Twilio dials, or answers, the
call and streams its audio to Voxie's `/twilio/media` WebSocket. Voxie runs
it on the same pipeline as a browser call, with the same barge-in and the
same voices. All you need is a Twilio number and a public URL for Voxie
(`ngrok http 8080` is enough); no SIP trunk and no public IP.

1. Set `TWILIO_AUTH_TOKEN` for the Voxie server. This turns the endpoint on,
   and it then accepts only streams Twilio has signed. Behind a proxy that
   rewrites the Host header, also set `VOXIE_TWILIO_STREAM_URL` to the public
   `wss://…/twilio/media` URL.
2. Place a call with this TwiML (Twilio's REST API `Twiml` parameter):
   ```xml
   <Response><Connect><Stream url="wss://<voxie-host>/twilio/media">
     <Parameter name="resource_id" value="+919812345678"/>
   </Stream></Connect></Response>
   ```
   `resource_id` reaches your agent as the call's `resource_id`.
   `direction` defaults to `outbound`.
3. When your agent ends the call (`end_call`), Voxie closes the stream once
   the goodbye has played, and Twilio hangs up. If the caller hangs up
   first, the call ends at Voxie too.

Phone audio (8 kHz μ-law) is converted to 16 kHz and back, with a low-pass
filter on the way out. When the caller interrupts, Voxie also clears the
audio Twilio has buffered, so the agent stops at once.

**Any SIP trunk:** put [sip-server](https://github.com/streamcoreai/sip-server)
in front. It turns SIP calls into WHIP sessions and passes the dialled number
as `resource_id`.

## Test it in many languages

```bash
pip install git+https://github.com/streamcoreai/python-sdk soundfile librosa gTTS
python tools/callers/make_callers.py              # 22 test callers
python tools/callers/call.py ta                   # a Tamil caller
python tools/callers/call.py ko --language fr     # a Korean caller whose record says French
python tools/callers/call.py hi --india --phone   # phone-line audio from an Indian number
```

What was measured, per language: [docs/voxie/languages.md](docs/voxie/languages.md).

## Layout

| Path | |
|---|---|
| `internal/`, `main.go` | The voice server (StreamCore plus Voxie's changes; the new listener is `internal/stt/adaptive.go`, phone calls are `internal/twilio/`) |
| `voice-router/` | Kokoro + Sarvam text-to-speech |
| `configs/voxie.toml` | One config for every caller |
| `examples/quickstart/` | Call page, WHIP proxy, demo agent |
| `tools/callers/` | Scripted multilingual callers over WebRTC |
| `docs/voxie/` | How it works, the agent contract, language measurements |
| `docs/`, `README.streamcore.md` | Upstream StreamCore's docs, still accurate for everything Voxie didn't change |

## Staying current with upstream

Voxie is a real fork, so StreamCore's history is kept and its fixes merge
in normally:

```bash
git fetch upstream && git merge upstream/main
go test ./...
```

## Limits

- Tested mostly with synthetic voices, clean and at phone quality, plus a
  real phone call through Twilio to an Indian mobile.
- A turn the listener may have misheard waits for language
  identification (0.4–1s), usually only the caller's first real turn.
  Confident Hindi is checked in the background, without waiting.
- No voice yet for German, Dutch, Russian, Korean, Arabic or Turkish.
  They're understood, but should be answered in English.
- Free API tiers run out quickly under multilingual load. Plan on paid
  tiers for Groq and Sarvam.

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
