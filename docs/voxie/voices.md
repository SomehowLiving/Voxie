# Voices: what we measured

Voxie can speak through its own voice router (Kokoro on a GPU, Piper on the
CPU, Sarvam for Indian languages) or through a cloud voice provider. These
are measurements, not recommendations: listen and choose. Measured on
2026-10-21 from one machine in India (network included), unless noted.
The test scripts aren't in this repository yet.

## Speed: time until speech is heard

Warm, median of 3, a short sentence; first audio plus the silence at its
start. The voice router returns a whole sentence before it plays; the cloud
APIs stream.

| Engine | Language | Heard after |
|---|---|---|
| Piper (self-hosted) | German | 0.28 s |
| Cartesia Sonic 3.6 | English · Hindi · Tamil | 0.37 · 0.35 · 0.36 s |
| ElevenLabs Flash v2.5 | English | 0.44 s |
| Kokoro (self-hosted) | English | 0.49 s |
| ElevenLabs Multilingual v2 | English | 1.02 s |
| Deepgram Aura-2 | English | 1.16 s |
| Sarvam Bulbul v3 | Hindi · Tamil | 1.26 · 1.32 s |
| OpenAI gpt-4o-mini-tts | English | 1.8–2.4 s, one run 25 s |

US-hosted APIs include the round trip from India.

## Accuracy: the voice transcribed back

The same payment sentence in each of Voxie's 29 spoken languages, read by
the voice router and by Cartesia Sonic 3.6, transcribed back by Whisper
large-v3 (1.00 = identical). Whisper is weak on Malayalam and Odia, so both
score low there; scored by Sarvam's speech-to-text instead, the ten Indian
languages are within ±0.04 for both.

| | en | es | fr | it | pt | ja | zh | de | nl | ru |
|---|---|---|---|---|---|---|---|---|---|---|
| Voice router | 1.00 | 0.99 | 0.98 | 1.00 | 0.93 | 0.91 | 0.76 | 0.95 | 0.98 | 0.94 |
| Cartesia | 1.00 | 0.97 | 0.98 | 0.98 | 0.93 | 0.91 | 0.83 | 0.98 | 0.97 | 0.97 |

| | pl | uk | cs | hu | sv | ro | fi | da | vi |
|---|---|---|---|---|---|---|---|---|---|
| Voice router | 1.00 | 0.95 | 0.99 | 0.95 | 0.99 | 0.99 | 0.96 | 0.96 | 0.94 |
| Cartesia | 0.98 | 1.00 | 0.98 | 0.97 | 0.98 | 0.99 | 1.00 | 0.98 | 0.94 |

| | hi | bn | ta | te | kn | ml | mr | gu | pa | od |
|---|---|---|---|---|---|---|---|---|---|---|
| Voice router | 0.90 | 0.82 | 0.93 | 0.76 | 0.81 | 0.38 | 0.82 | 0.81 | 0.40 | 0.36 |
| Cartesia | 0.91 | 0.82 | 0.93 | 0.77 | 0.93 | 0.36 | 0.86 | 0.88 | 0.79 | 0.36 |

**Numbers and codes** (what Whisper heard):

| Text | Kokoro | Cartesia |
|---|---|---|
| `₹4,999`, `₹12,500` (English and Hindi) | right | right |
| `+91 98123 45678`, OTP `482913`, "GST" | "plus 90-198-123…", "480-2913", "GSD" | right |
| `12/10/2026` in English | "12-10-2026" | "December 10" (US order) |

## Price per minute of speech

About 900 characters a minute; list prices on 2026-10-05.

| Engine | $ per minute of speech |
|---|---|
| Kokoro, Piper (self-hosted) | none per minute; you run the GPU/CPU |
| OpenAI gpt-4o-mini-tts | ~0.014 |
| Deepgram Aura-2 | 0.024–0.027 |
| Sarvam Bulbul v3 (V4 Flash) | ~0.031 (~0.020) |
| Cartesia Sonic 3.6 | 0.034–0.045 by plan |
| ElevenLabs Flash v2.5 | 0.036 |
| ElevenLabs Multilingual v2 | 0.072 |

## Not measured

Naturalness (a listening judgment; for an independent view see the
Artificial Analysis speech arena), live calls through Voxie with each cloud
voice, and behaviour under many simultaneous calls.
