# Voice router: Kokoro (local GPU), Piper (local CPU) + Sarvam (Indian languages)

A text-to-speech server for Voxie. It speaks the same HTTP contract as the
bundled VibeVoice sidecar, so the server uses it through its existing
`vibevoice` TTS provider:

```toml
[tts]
provider = "vibevoice"

[vibevoice]
tts_url = "http://127.0.0.1:8300"   # this server
```

- `POST /synthesize {"text", "voice"}` returns raw 16 kHz mono 16-bit PCM.
- `GET /health` reports the model, device, voice persona (and the ones on offer), the languages it
  can voice **right now**, and Sarvam's state.

## Run it

**Docker** (needs the NVIDIA container runtime):

```bash
docker build -t voxie-voice-router voice-router
docker run --gpus all -p 8300:8300 -e SARVAM_API_KEY \
  -v hf-cache:/root/.cache/huggingface voxie-voice-router --voice female
```

**Locally:** use a venv of its own. Installing `kokoro` next to VibeVoice
upgrades torch and transformers and breaks VibeVoice.

```bash
uv venv --python 3.11 .venv
uv pip install --python .venv/bin/python torch==2.9.1 --index-url https://download.pytorch.org/whl/cu128
uv pip install --python .venv/bin/python -r requirements.txt
uv pip uninstall --python .venv/bin/python unidic   # see "Japanese" below
.venv/bin/python server.py --port 8300 --voice female
```

| Flag | Default | |
|---|---|---|
| `--voice` | `female` | Voice persona: `female`, `male`, `female-2` or `male-2` (below) |
| `--voice-gender` | `female` | Older shorthand for `--voice female` / `--voice male` |
| `--default-voice` | the persona's | Kokoro voice for English in the server's persona (requests for other personas keep theirs). `hm_omega` gives Indian-accented English |
| `--spoken-names` | `$VOXIE_SPOKEN_NAMES` | JSON file of names respelled per language (see below) |
| `--device` | `cuda` if available | |

## Languages and voices

A **voice persona** is one voice across every language. Pick it per
server with `--voice`, per request: `POST /synthesize {"voice": "male-2"}`,
or per line: a leading `[voice:male-2]` tag (stripped before speaking, and
before any `[lang:xx]` tag). The tag is how one server speaks in each
company's own voice: StreamCore sends the same fixed voice name on every
request, but passes the agent's text through. An unknown name falls back to
the server's persona. `/health` reports `"voice_tags": true`.

| Language | `female` | `male` | `female-2` | `male-2` |
|---|---|---|---|---|
| English | `af_heart` | `am_michael` | `af_bella` | `am_fenrir` |
| Spanish | `ef_dora` | `em_alex` | `ef_dora` | `em_alex` |
| French | `ff_siwis` | `ff_siwis` | `ff_siwis` | `ff_siwis` |
| Italian | `if_sara` | `im_nicola` | `if_sara` | `im_nicola` |
| Portuguese | `pf_dora` | `pm_alex` | `pf_dora` | `pm_alex` |
| Japanese | `jf_alpha` | `jm_kumo` | `jf_nezumi` | `jm_kumo` |
| Chinese | `zf_xiaobei` | `zm_yunxi` | `zf_xiaoni` | `zm_yunjian` |
| German (Piper) | `de_DE-kerstin-low` | `de_DE-thorsten-high` | `de_DE-kerstin-low` | `de_DE-thorsten-high` |
| Dutch (Piper) | `nl_BE-nathalie-medium` | `nl_BE-rdh-medium` | `nl_BE-nathalie-medium` | `nl_BE-rdh-medium` |
| Russian (Piper) | `ru_RU-dmitri-medium` | `ru_RU-dmitri-medium` | `ru_RU-dmitri-medium` | `ru_RU-dmitri-medium` |
| Polish (Piper) | `pl_PL-gosia-medium` | `pl_PL-darkman-medium` | `pl_PL-gosia-medium` | `pl_PL-darkman-medium` |
| Hindi (Kokoro fallback) | `hf_alpha` | `hm_omega` | `hf_beta` | `hm_psi` |
| Hindi, Bengali, Tamil, Telugu, Kannada, Malayalam, Marathi, Gujarati, Punjabi, Odia | Sarvam `bulbul:v3` `priya` | Sarvam `rahul` | Sarvam `neha` | Sarvam `aditya` |

Kokoro has one voice per gender for Spanish, Italian and Portuguese, and
no male French voice, so those are shared. (Its "Santa" voices are a jolly
character, not a second voice.) Piper has one licensed voice per gender
for German, Dutch and Polish, and no CC0 female Russian voice, so Russian
uses `dmitri` for every persona. Sarvam has 37 speakers; to use others, edit
`PERSONAS` in `server.py`.

Every language is warmed up at startup, and every persona's voices preloaded: each would otherwise take ~3s to
load on its first sentence, which a caller would hear as a pause.

**Which language a sentence is in:**
- A leading `[lang:xx]` tag decides, and is stripped before speaking. The
  server strips these tags from transcripts too. Agents should send tags:
  Hindi and Marathi share a script, so the text alone can't tell them apart.
- **Untagged text is detected:**
  - By script: kana → Japanese, Han → Chinese, Cyrillic → Russian, and
    each Indic script by its Unicode block. Devanagari → Hindi.
  - Otherwise by `lingua`, limited to English, Spanish, French, Italian,
    Portuguese, German, Dutch and Polish.
- A sentence too short to tell ("Ok.") reuses the language of the last 30s.

**Spoken forms:**
- **Amounts** in ₹, $, € and £ are read with the currency in each
  language's words ("₹4,999" → "4,999 rupees" / "4999 rupias" / "4999
  ルピー"; "4 999 €" → "4999 euros"). ¥ is left alone: it's yen in
  Japanese and yuan in Chinese.
- **Names** are respelled per language from `--spoken-names`. Without it,
  Spanish, Italian and Portuguese spell an all-caps name out letter by
  letter, and Japanese and Chinese drop or garble Latin-script words.
  `spoken_names.example.json` shows the format.

## Piper: German, Dutch, Russian and Polish

Languages Kokoro doesn't speak use [Piper](https://github.com/rhasspy/piper)
on the CPU, 0.1–0.35s a sentence (~13× real time). Its voices download
once at startup into the Hugging Face cache, from a pinned revision of
`rhasspy/piper-voices` (60–110 MB each, ~500 MB in all). A voice that can't load leaves its
language out of `/health`'s `languages`.

- **Licenses:** `piper-tts` is pinned to 1.2.0, the last MIT release (later
  ones are GPL-3.0). Each voice is trained on a CC0 dataset, checked in its
  model card. Piper's Turkish and Korean voices are non-commercial (CC
  BY-NC-SA) and its Arabic voice's data has no license, so those
  languages have no voice here.
- **Checked** by transcribing back with Whisper large-v3: a payment line
  matched at 0.94–1.00, and "€1.249,50" was read right in all four
  languages for both genders.

## Sarvam, and what happens when it fails

Indian languages use Sarvam `bulbul:v3` (cloud; set `SARVAM_API_KEY`), at
1.3–2s per sentence, with one retry for a transient error.
- **Hindi and Marathi** fall back to Kokoro's Hindi voice.
- **Other Indian languages** have no local voice. `/health` then leaves
  them out of `languages`, so an agent can answer those callers in
  English instead of silence.
- **Breaker:** after 2 consecutive failures, Sarvam is down for 60s.
- **Account problems:** a rejected key (401/403) disables Sarvam; an
  account out of credits (402) takes it out for 10 minutes, then retries.
  Both are also checked with one real request at startup.

## Known issues handled here

- **espeak-ng:** `espeakng_loader` 0.2.4 ships an espeak-ng that rejects
  its own data directory and exits the process. `_fix_espeak_data_path`
  works around it.
- **English G2P:** it downloads `en_core_web_sm` on first use and exits if
  that fails, so `requirements.txt` installs it up front.
- **Japanese:** `misaki[ja]` installs the full `unidic` without its data,
  and MeCab prefers it over `unidic-lite`, failing every sentence.
  Uninstall `unidic`; the Dockerfile does.

## Measured (RTX 5050 Laptop GPU, 8GB)

| | VibeVoice-Realtime-0.5B | Kokoro-82M |
|---|---|---|
| GPU memory | ~2.7GB | ~0.75GB |
| One sentence (~3.6s of speech) | ~5.2s | ~130ms |
| A call's six sentences (25.9s of speech) | – | 0.9s total, ~28× real time |

All eight Kokoro languages were checked by transcribing the output back:
each was detected as the right language (0.95–0.99) with the amount
correct. So was every Sarvam language.
