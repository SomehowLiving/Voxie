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
3. For phone calls, put [sip-server](https://github.com/streamcoreai/sip-server)
   in front. It turns a SIP trunk (Twilio, Telnyx…) into WHIP sessions and
   passes the dialled number as `resource_id`.

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
| `internal/`, `main.go` | The voice server (StreamCore plus Voxie's changes; the new listener is `internal/stt/adaptive.go`) |
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

- Tested with synthetic voices, clean and at phone quality. It hasn't
  been tested with real people on real phone lines yet.
- A checked turn waits for language identification: 0.4–1s, usually on
  the first turn only.
- No voice yet for German, Dutch, Russian, Korean, Arabic or Turkish.
  They're understood, but should be answered in English.
- Free API tiers run out quickly under multilingual load. Plan on paid
  tiers for Groq and Sarvam.

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
