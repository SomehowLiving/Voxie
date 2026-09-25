"""Kokoro-82M text-to-speech sidecar for StreamCore.

Speaks the same HTTP contract as StreamCore's bundled VibeVoice TTS sidecar,
so StreamCore's existing `vibevoice` TTS provider can use it by pointing
`[vibevoice] tts_url` here -- no StreamCore code change:

    POST /synthesize  {"text": "...", "voice": "..."}
      -> 200, raw 16 kHz mono signed 16-bit little-endian PCM
    GET  /health      -> {"status": "ok", ...}

Why Kokoro: VibeVoice-Realtime-0.5B generated audio slower than real time on
this machine's GPU (~5s for ~3.6s of speech), and StreamCore can only play a
sentence once the whole sentence is back. Kokoro is an 82M-parameter model
that generates a sentence far faster than it takes to say it.

Languages: StreamCore sends only text, with no language, and the agent may
speak whatever language the caller is speaking. So each sentence's language is
detected here: by script (kana = Japanese, Han = Chinese, Devanagari =
Hindi), otherwise with a detector limited to the Latin-script languages
Kokoro speaks (English, Spanish, French, Italian, Portuguese). A sentence too
short or unclear to tell ("Ok.") reuses the language spoken just before.
Each language gets a matching Kokoro voice, male or female (--voice-gender).

English uses --default-voice, which may be any Kokoro voice -- including a
Hindi one ("hm_omega") for Indian-accented English. The request's "voice"
field (StreamCore's VibeVoice voice name) is ignored.

usage: python server.py [--port 8300] [--device cuda] [--default-voice af_heart] [--voice-gender female] [--spoken-names names.json]
"""

import argparse
import base64
import io
import json
import logging
import os
import re
import tempfile
import threading
import time
import urllib.error
import urllib.request
import wave

import numpy as np
import torch
import uvicorn
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel
from scipy.signal import resample_poly

REPO_ID = "hexgrad/Kokoro-82M"
MODEL_SAMPLE_RATE = 24000
TARGET_SAMPLE_RATE = 16000  # what StreamCore's vibevoice TTS client expects

logger = logging.getLogger("kokoro-tts")
app = FastAPI(title="Kokoro TTS sidecar")

_model = None
_pipelines: dict = {}  # lang code -> KPipeline, all sharing _model
_device = "cpu"
# One model, many callers: StreamCore synthesizes sentences concurrently, and
# a shared torch model isn't safe to run from several threads at once.
_lock = threading.Lock()

_VOICE_RE = re.compile(r"^[a-z][fm]_[a-z0-9]+$")

# Kokoro pipeline language code and voices (female, male) per language.
# Kokoro's pipeline (text-to-phoneme) code per language.
VOICES = {"es": "e", "fr": "f", "it": "i", "pt": "p", "ja": "j", "zh": "z", "hi": "h"}

# A persona is one consistent voice across every language: a Kokoro voice
# for English and each Kokoro language (Hindi only as Sarvam's fallback),
# and a Sarvam speaker for the Indian languages. Where Kokoro has a single
# voice of a gender (Spanish, French, Italian, Portuguese), both personas of
# that gender share it; Kokoro's "Santa" voices are a jolly character, not
# a second voice. Chosen per server (--voice) or per request (the request's
# "voice" field names a persona).
PERSONAS = {
    "female": {
        "gender": "female", "english": "af_heart", "sarvam": "priya",
        "kokoro": {"es": "ef_dora", "fr": "ff_siwis", "it": "if_sara", "pt": "pf_dora",
                   "ja": "jf_alpha", "zh": "zf_xiaobei", "hi": "hf_alpha"},
    },
    "male": {
        "gender": "male", "english": "am_michael", "sarvam": "rahul",
        # Kokoro has no male French voice.
        "kokoro": {"es": "em_alex", "fr": "ff_siwis", "it": "im_nicola", "pt": "pm_alex",
                   "ja": "jm_kumo", "zh": "zm_yunxi", "hi": "hm_omega"},
    },
    "female-2": {
        "gender": "female", "english": "af_bella", "sarvam": "neha",
        "kokoro": {"es": "ef_dora", "fr": "ff_siwis", "it": "if_sara", "pt": "pf_dora",
                   "ja": "jf_nezumi", "zh": "zf_xiaoni", "hi": "hf_beta"},
    },
    "male-2": {
        "gender": "male", "english": "am_fenrir", "sarvam": "aditya",
        "kokoro": {"es": "em_alex", "fr": "ff_siwis", "it": "im_nicola", "pt": "pm_alex",
                   "ja": "jm_kumo", "zh": "zm_yunjian", "hi": "hm_psi"},
    },
}
_persona = "female"
_english_override: str | None = None  # --default-voice: English voice for the server's persona

# Names the agent says in every language, spelled so each language's text-
# to-phoneme step reads them as words. Heard in live checks: Spanish, Italian
# and Portuguese spelled an all-caps name out letter by letter ("R X"), and
# Japanese/Chinese dropped or garbled Latin-script words entirely ("Pro" came
# out as "德尔"). Loaded from
# --spoken-names (JSON: {"ja": {"ACME": "アクメ"}, ...}); see
# spoken_names.example.json.
SPOKEN_NAMES: dict[str, dict[str, str]] = {}


def load_spoken_names(path: str | None) -> None:
    global SPOKEN_NAMES
    if not path:
        return
    with open(path, encoding="utf-8") as f:
        SPOKEN_NAMES = json.load(f)
    logger.info("spoken names loaded for %d languages from %s", len(SPOKEN_NAMES), path)


# The word said after an amount, per currency symbol and language
# ("₹4,999" -> "4999 rupias"). The G2P has no reading for the symbols, so
# without this an amount is said with no currency at all. ¥ is left alone:
# it's yen in Japanese and yuan in Chinese.
CURRENCY_WORDS = {
    "₹": {"en": "rupees", "es": "rupias", "fr": "roupies", "it": "rupie", "pt": "rúpias", "ja": "ルピー", "zh": "卢比", "hi": "रुपये"},
    "$": {"en": "dollars", "es": "dólares", "fr": "dollars", "it": "dollari", "pt": "dólares", "ja": "ドル", "zh": "美元", "hi": "डॉलर"},
    "€": {"en": "euros", "es": "euros", "fr": "euros", "it": "euro", "pt": "euros", "ja": "ユーロ", "zh": "欧元", "hi": "यूरो"},
    "£": {"en": "pounds", "es": "libras", "fr": "livres", "it": "sterline", "pt": "libras", "ja": "ポンド", "zh": "英镑", "hi": "पाउंड"},
}

_detector = None
_last_language = "en"
_last_language_at = 0.0
STICKY_SECONDS = 30.0


def _latin_detector():
    global _detector
    if _detector is None:
        from lingua import Language, LanguageDetectorBuilder

        _detector = LanguageDetectorBuilder.from_languages(
            Language.ENGLISH, Language.SPANISH, Language.FRENCH, Language.ITALIAN, Language.PORTUGUESE
        ).build()
    return _detector


_LINGUA_CODES = {"ENGLISH": "en", "SPANISH": "es", "FRENCH": "fr", "ITALIAN": "it", "PORTUGUESE": "pt"}


# Unicode blocks of the Indic scripts Sarvam speaks (Devanagari is handled
# above: it's shared by Hindi and Marathi, so only a tag can tell them apart).
_INDIC_SCRIPTS = [
    ("bn", "\u0980", "\u09ff"),  # Bengali
    ("pa", "\u0a00", "\u0a7f"),  # Gurmukhi
    ("gu", "\u0a80", "\u0aff"),  # Gujarati
    ("od", "\u0b00", "\u0b7f"),  # Odia
    ("ta", "\u0b80", "\u0bff"),  # Tamil
    ("te", "\u0c00", "\u0c7f"),  # Telugu
    ("kn", "\u0c80", "\u0cff"),  # Kannada
    ("ml", "\u0d00", "\u0d7f"),  # Malayalam
]


def _indic_script(text: str) -> str | None:
    for lang, lo, hi in _INDIC_SCRIPTS:
        if any(lo <= ch <= hi for ch in text):
            return lang
    return None


_LANG_TAG = re.compile(r"^\s*\[lang:([a-zA-Z-]{2,8})\]\s*")


def take_language_tag(text: str) -> tuple[str | None, str]:
    """The agent may prefix its lines with "[lang:ta] " so the language is exact
    rather than guessed (Hindi and Marathi share a script). Returns the
    language and the text without the tag."""
    m = _LANG_TAG.match(text)
    if not m:
        return None, text
    return m.group(1).lower()[:2], text[m.end():]


def remember_language(language: str) -> None:
    global _last_language, _last_language_at
    _last_language, _last_language_at = language, time.monotonic()


def detect_language(text: str) -> str:
    """The language of one sentence, or the recent one if it can't tell."""
    global _last_language, _last_language_at
    now = time.monotonic()
    recent = _last_language if now - _last_language_at < STICKY_SECONDS else "en"

    if re.search(r"[\u3040-\u30ff]", text):
        found = "ja"
    elif re.search(r"[\u4e00-\u9fff]", text):
        found = "zh"
    elif (indic := _indic_script(text)) is not None:
        found = indic
    # Devanagari letters -- not the danda "।" (U+0964/5), which lives in the
    # Devanagari block but ends sentences in Bengali, Punjabi and Odia too.
    elif re.search(r"[\u0900-\u0963\u0966-\u097f]", text):
        found = "hi"  # Hindi unless a [lang:mr] tag says Marathi
    else:
        letters = re.sub(r"[^A-Za-zÀ-ÿ]", "", text)
        found = None
        if len(letters) >= 12:
            ranked = _latin_detector().compute_language_confidence_values(text)
            if ranked and ranked[0].value >= 0.6:
                found = _LINGUA_CODES.get(ranked[0].language.name)
        found = found or recent

    _last_language, _last_language_at = found, now
    return found


def _fix_espeak_data_path() -> None:
    """Make the espeak-ng bundled by espeakng_loader find its own data.

    Kokoro's text-to-phoneme step falls back to espeak-ng for words it doesn't
    know (and uses it outright for Hindi). The bundled library (espeakng_loader
    0.2.4, espeak-ng 1.52) rejects the data directory misaki hands it, falls
    back to the path compiled in on the package's CI machine
    ("/home/runner/work/..."), and then exit()s the whole process -- no Python
    exception -- with "phontab: No such file or directory". Found by trial: it
    only accepts a directory that holds the data files *and* has an
    "espeak-ng-data" entry inside it. So build one: symlinks to every data file
    plus "espeak-ng-data" -> ".", and hand that to phonemizer instead.
    """
    try:
        import espeakng_loader
        import misaki.espeak  # noqa: F401 -- runs misaki's (broken) set_data_path once, first
        from phonemizer.backend.espeak.wrapper import EspeakWrapper
    except ImportError:
        return
    source = espeakng_loader.get_data_path()
    shim = os.path.join(tempfile.gettempdir(), f"kokoro-tts-espeak-{os.getuid()}")
    os.makedirs(shim, exist_ok=True)
    for name in os.listdir(source):
        link = os.path.join(shim, name)
        if not os.path.lexists(link):
            os.symlink(os.path.join(source, name), link)
    if not os.path.lexists(os.path.join(shim, "espeak-ng-data")):
        os.symlink(".", os.path.join(shim, "espeak-ng-data"))
    EspeakWrapper.set_data_path(shim)


def persona_for(requested: str) -> dict:
    """The request's persona if it names one, else the server's."""
    return PERSONAS.get((requested or "").strip().lower(), PERSONAS[_persona])


def english_voice(persona: dict) -> str:
    if _english_override and persona is PERSONAS[_persona]:
        return _english_override
    return persona["english"]


def voice_for(language: str, persona: dict) -> tuple[str, str]:
    """(Kokoro pipeline code, voice) for a language."""
    if language in VOICES:
        return VOICES[language], persona["kokoro"][language]
    # English (and anything unexpected): the persona's voice, or
    # --default-voice for the server's own persona (a request for another
    # persona keeps that persona's, so its gender stays right). Read with the
    # British G2P for British voices and American for everything else --
    # including a Hindi voice reading English.
    english = english_voice(persona)
    return ("b" if english.startswith("b") else "a"), english


def _pipeline(lang: str):
    from kokoro import KPipeline

    if lang not in _pipelines:
        _pipelines[lang] = KPipeline(lang_code=lang, repo_id=REPO_ID, model=_model)
    return _pipelines[lang]


# Indian grouping first ("1,00,000"), then thousands groups, then plain.
_LAKH = r"\d{1,2}(?:,\d{2})+,\d{3}(?:\.\d+)?"
_NUMBER = _LAKH + r"|\d{1,3}(?:[.,\s\u00a0\u202f]\d{3})*(?:[.,]\d+)?|\d+(?:[.,]\d+)?"


def normalize(text: str, language: str = "en") -> str:
    for name, spoken in SPOKEN_NAMES.get(language, {}).items():
        text = re.sub(rf"(?<![A-Za-z]){name}(?![A-Za-z])", spoken, text)

    def amount(digits: str, word: str) -> str:
        # Outside English, "4,999" or "4.999" is read as a decimal; plain
        # digits are read as the whole number in every language.
        if re.fullmatch(_LAKH, digits):
            digits = digits.replace(",", "")  # "1,00,000" -> "100000", read right everywhere
        elif language != "en":
            digits = re.sub(r"(\d)[,.\s\u00a0\u202f](?=\d{3}\b)", r"\1", digits)
        # "$12.50" is "12 dollars 50", not "twelve point five dollars".
        cents = re.fullmatch(r"(.+?)[.,](\d{2})", digits)
        if cents:
            return f"{cents.group(1)} {word} {cents.group(2)}"
        return f"{digits} {word}"

    for symbol, words in CURRENCY_WORDS.items():
        word = words.get(language, words["en"])
        sym = re.escape(symbol)
        # "₹4,999" (symbol first) and "4.999 €" (symbol after, as many
        # languages write it) both become "<number> <word>".
        text = re.sub(rf"{sym}\s?({_NUMBER})", lambda m: amount(m.group(1), word), text)
        text = re.sub(rf"({_NUMBER})\s?{sym}", lambda m: amount(m.group(1), word), text)
        text = text.replace(symbol, f" {word} ")
    return re.sub(r"  +", " ", text).strip()


# ---------------------------------------------------------------------------
# Sarvam: the voice for Indian languages. Cloud, ~1.3-2s a sentence, and
# intelligible in every Indian language tested (checked by transcribing its
# audio back). Kokoro stays the voice for everything else, and the fallback
# for Hindi and Marathi when Sarvam is down.
# ---------------------------------------------------------------------------

SARVAM_LANGUAGES = {"hi", "bn", "ta", "te", "kn", "ml", "mr", "gu", "pa", "od"}
SARVAM_TTS_URL = "https://api.sarvam.ai/text-to-speech"
SARVAM_MODEL = "bulbul:v3"
SARVAM_TIMEOUT_S = 6.0
# Kokoro can read Devanagari: Hindi natively, Marathi passably.
KOKORO_FALLBACK = {"hi": "hi", "mr": "hi"}


class SarvamBreaker:
    """Stops calling Sarvam for a while after repeated failures, so a
    Sarvam outage costs one quick failure per sentence at most a few times,
    not a timeout on every line -- and /health can tell the agent to stop choosing
    languages only Sarvam can speak."""

    def __init__(self, threshold: int = 2, cooldown_s: float = 60.0):
        self.threshold, self.cooldown_s = threshold, cooldown_s
        self.failures, self.open_until = 0, 0.0
        self.lock = threading.Lock()

    def available(self) -> bool:
        return time.monotonic() >= self.open_until

    def disable(self, reason: str) -> None:
        """Down for good: a rejected key won't start working on its own."""
        with self.lock:
            self.open_until = float("inf")
        logger.error("Sarvam voice disabled: %s", reason)

    def suspend(self, reason: str, seconds: float) -> None:
        """Down for a long while: the account can't be used right now (no
        credits) but may be topped up, so it's retried later."""
        with self.lock:
            self.open_until = max(self.open_until, time.monotonic() + seconds)
        logger.error("Sarvam voice down for %.0f min: %s", seconds / 60, reason)

    def success(self) -> None:
        with self.lock:
            self.failures, self.open_until = 0, 0.0

    def failure(self) -> None:
        with self.lock:
            self.failures += 1
            if self.failures >= self.threshold:
                self.open_until = time.monotonic() + self.cooldown_s
                logger.warning("Sarvam voice marked down for %.0fs after %d failures", self.cooldown_s, self.failures)


_sarvam_key = os.environ.get("SARVAM_API_KEY", "")
_sarvam_breaker = SarvamBreaker()


def sarvam_available() -> bool:
    return bool(_sarvam_key) and _sarvam_breaker.available()


def _sarvam_once(text: str, language: str, speaker: str | None = None) -> bytes:
    body = json.dumps({
        "text": text,
        "target_language_code": f"{language}-IN",
        "speaker": speaker or PERSONAS[_persona]["sarvam"],
        "model": SARVAM_MODEL,
        "speech_sample_rate": TARGET_SAMPLE_RATE,
    }).encode()
    req = urllib.request.Request(SARVAM_TTS_URL, data=body, method="POST",
                                 headers={"api-subscription-key": _sarvam_key, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=SARVAM_TIMEOUT_S) as res:
        payload = json.loads(res.read())
    audio = base64.b64decode(payload["audios"][0])
    with wave.open(io.BytesIO(audio)) as w:
        if w.getframerate() != TARGET_SAMPLE_RATE or w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise RuntimeError(f"unexpected Sarvam audio: {w.getframerate()} Hz, {w.getnchannels()} ch")
        return w.readframes(w.getnframes())


def synthesize_sarvam(text: str, language: str, speaker: str | None = None) -> bytes:
    """One retry for a transient error; a request Sarvam rejects (4xx) isn't
    retried."""
    last: Exception | None = None
    for attempt in range(2):
        try:
            pcm = _sarvam_once(text, language, speaker)
            _sarvam_breaker.success()
            return pcm
        except urllib.error.HTTPError as exc:
            last = exc
            if account_problem(exc):
                raise RuntimeError(f"Sarvam voice failed: {exc}") from exc
            if 400 <= exc.code < 500 and exc.code != 429:
                break
        except Exception as exc:  # timeout, connection, bad payload
            last = exc
    _sarvam_breaker.failure()
    raise RuntimeError(f"Sarvam voice failed: {last}")


def account_problem(exc: urllib.error.HTTPError) -> bool:
    """A rejected key (401/403) or an account out of credits (402) fails
    every request until someone fixes the account, so Sarvam is taken out at
    once -- and /health stops offering its languages -- rather than after
    failures on a caller's lines. Live: credits ran out mid-test, and Tamil
    replies went silent while /health still listed Tamil."""
    if exc.code in (401, 403):
        _sarvam_breaker.disable(f"key rejected (HTTP {exc.code})")
        return True
    if exc.code == 402:
        _sarvam_breaker.suspend("account out of credits (HTTP 402)", 600)
        return True
    return False


def speakable_languages() -> list[str]:
    """What this server can voice right now; an agent should only answer in these."""
    languages = ["en", *VOICES]
    if sarvam_available():
        languages += sorted(SARVAM_LANGUAGES - set(languages))
    return languages


def synthesize(text: str, voice: str = "") -> bytes:
    persona = persona_for(voice)
    tagged, text = take_language_tag(text)
    language = tagged or detect_language(text)
    remember_language(language)
    if language in SARVAM_LANGUAGES and sarvam_available():
        try:
            pcm = synthesize_sarvam(text, language, persona["sarvam"])
            logger.info("language %s -> Sarvam %s", language, persona["sarvam"])
            return pcm
        except Exception as exc:
            if language not in KOKORO_FALLBACK:
                raise
            logger.warning("%s; falling back to Kokoro for %s", exc, language)
    if language in SARVAM_LANGUAGES:
        if language not in KOKORO_FALLBACK:
            raise RuntimeError(f"no voice available for {language}: Sarvam is down or not configured")
        language = KOKORO_FALLBACK[language]
    lang, kokoro_voice = voice_for(language, persona)
    logger.info("language %s -> voice %s", language, kokoro_voice)
    text = normalize(text, language)
    with _lock:
        parts = [
            r.audio.detach().float().cpu().numpy()
            for r in _pipeline(lang)(text, voice=kokoro_voice, speed=1.0)
            if r.audio is not None
        ]
    if not parts:
        return b""
    audio = np.concatenate(parts)
    audio = resample_poly(audio, TARGET_SAMPLE_RATE // 8000, MODEL_SAMPLE_RATE // 8000)
    return (np.clip(audio, -1.0, 1.0) * 32767).astype("<i2").tobytes()


class SynthesizeRequest(BaseModel):
    text: str
    voice: str = ""


@app.post("/synthesize")
def synthesize_endpoint(req: SynthesizeRequest):
    if not req.text.strip():
        raise HTTPException(status_code=400, detail="text must not be empty")
    start = time.perf_counter()
    try:
        pcm = synthesize(req.text, req.voice)
    except Exception as exc:  # surface the reason to StreamCore's log
        logger.exception("synthesis failed")
        raise HTTPException(status_code=500, detail=str(exc)) from exc
    took = time.perf_counter() - start
    seconds = len(pcm) / 2 / TARGET_SAMPLE_RATE
    logger.info(
        "synthesized %.2fs of audio in %.3fs (%.1fx real time) for %d chars",
        seconds, took, seconds / took if took else 0, len(req.text),
    )
    return Response(content=pcm, media_type="application/octet-stream")


@app.get("/health")
def health():
    return {
        "status": "ok",
        "model": REPO_ID,
        "device": _device,
        "voice": _persona,
        "voice_gender": PERSONAS[_persona]["gender"],
        "voices": {name: {"gender": p["gender"], "english": english_voice(p), "sarvam": p["sarvam"]}
                   for name, p in PERSONAS.items()},
        "languages": speakable_languages(),
        "sarvam": "not configured" if not _sarvam_key else ("up" if _sarvam_breaker.available() else "down"),
    }


def main():
    global _model, _device, _persona, _english_override
    parser = argparse.ArgumentParser(description="Kokoro TTS sidecar for StreamCore")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8300)
    parser.add_argument("--device", default="cuda" if torch.cuda.is_available() else "cpu")
    parser.add_argument("--voice", choices=sorted(PERSONAS), help="voice persona for every language (default: --voice-gender)")
    parser.add_argument("--voice-gender", choices=["female", "male"], default="female", help="shorthand for --voice female / male")
    parser.add_argument("--default-voice", default=None, help="Kokoro voice for English, overriding the persona's (hm_omega: Indian-accented English)")
    parser.add_argument("--spoken-names", default=os.environ.get("VOXIE_SPOKEN_NAMES"), help="JSON file: how to spell names per language")
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(name)s] %(levelname)s: %(message)s")
    load_spoken_names(args.spoken_names)

    _fix_espeak_data_path()
    from kokoro import KModel

    _device = args.device
    _persona = args.voice or args.voice_gender
    _english_override = args.default_voice if args.default_voice and _VOICE_RE.match(args.default_voice) else None
    logger.info("loading %s on %s", REPO_ID, _device)
    _model = KModel(repo_id=REPO_ID).to(_device).eval()
    # Warm up so the first real request doesn't pay for the voice download,
    # CUDA kernel setup and the G2P's first load.
    # Check the Sarvam key once, up front. A rejected key would otherwise
    # surface only on a caller's first lines -- as silence, for languages
    # no local voice can speak -- before the breaker learned it.
    if _sarvam_key:
        try:
            _sarvam_once("नमस्ते", "hi")
            logger.info("Sarvam voice key accepted")
        except urllib.error.HTTPError as exc:
            if not account_problem(exc):
                _sarvam_breaker.failure()
        except Exception as exc:
            logger.warning("Sarvam voice check failed at startup: %s", exc)
            _sarvam_breaker.failure()
    synthesize("Hello, this is a warm up.")
    # Every other language too: each loads its own text-to-phoneme pipeline
    # and voice on first use (~3s), which a caller would otherwise hear as a
    # pause before the agent's first line in that language.
    for sample in ("Hola, esto es una prueba.", "Bonjour, ceci est un test.", "Ciao, questa è una prova.",
                   "Olá, isto é um teste.", "こんにちは、テストです。", "你好，这是测试。", "नमस्ते, यह एक परीक्षण है।"):
        synthesize(sample)
    # The other personas share those pipelines; only their voice packs are
    # still to load, which is quick and needs no synthesis.
    for persona in PERSONAS.values():
        for language in ("en", *VOICES):
            code, kokoro_voice = voice_for(language, persona)
            try:
                with _lock:
                    _pipeline(code).load_voice(kokoro_voice)
            except Exception as exc:
                logger.warning("couldn't preload voice %s: %s", kokoro_voice, exc)
    logger.info(
        "ready (voice %s, English %s; Sarvam %s)",
        _persona,
        english_voice(PERSONAS[_persona]),
        ("on for " + ", ".join(sorted(SARVAM_LANGUAGES)) if sarvam_available() else "DOWN -- Indian languages other than Hindi can't be spoken")
        if _sarvam_key
        else "off: SARVAM_API_KEY not set",
    )
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
