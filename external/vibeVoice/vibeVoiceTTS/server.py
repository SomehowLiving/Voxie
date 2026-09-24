#!/usr/bin/env python3
"""
VibeVoice TTS - HTTP text-to-speech server.

POST /synthesize  →  raw PCM audio (16kHz, 16-bit signed LE, mono)
GET  /health      →  {"status":"ok"}

Uses mlx-audio on Apple Silicon, falls back to PyTorch on other platforms.
"""

import argparse
import logging
import platform
import threading

import numpy as np
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel
import uvicorn

logger = logging.getLogger("vibevoice-tts")

TARGET_SAMPLE_RATE = 16000


def is_apple_silicon():
    return platform.system() == "Darwin" and platform.machine() == "arm64"


_model = None
_backend = None
_model_sample_rate = 24000  # VibeVoice-Realtime outputs 24 kHz


_voice_prefill_cache = {}  # voice name -> preloaded torch.load(voice.pt) dict

# One synthesis at a time. StreamCore cancels a response by dropping its HTTP
# request, but the handler's executor thread keeps generating; a second
# request arriving meanwhile (the caller spoke, a new reply is due) would run
# generate() concurrently on the same model and corrupt its state -- seen live
# as "unsupported operand type(s) for -: 'Tensor' and 'NoneType'".
_synthesis_lock = threading.Lock()
_voices_dir_arg = None  # set from --voices-dir before load_model() runs


def _load_streaming_pt(model_name: str, voices_dir: str):
    """Load microsoft/VibeVoice-Realtime-0.5B via the actual current
    upstream API. StreamCore's own fallback (a generic AutoModelForCausalLM
    load) doesn't work here: the real class is
    vibevoice.modular.modeling_vibevoice_streaming_inference
    .VibeVoiceStreamingForConditionalGenerationInference, not
    vibevoice.realtime.model.VibeVoiceRealtimeModel (which doesn't exist in
    the current microsoft/VibeVoice repo). Mirrors
    demo/realtime_model_inference_from_file.py.
    """
    import torch
    from vibevoice.modular.modeling_vibevoice_streaming_inference import (
        VibeVoiceStreamingForConditionalGenerationInference,
    )
    from vibevoice.processor.vibevoice_streaming_processor import (
        VibeVoiceStreamingProcessor,
    )

    device = "cuda" if torch.cuda.is_available() else "cpu"
    dtype = torch.bfloat16 if device == "cuda" else torch.float32
    attn_impl = "flash_attention_2" if device == "cuda" else "sdpa"

    processor = VibeVoiceStreamingProcessor.from_pretrained(model_name)
    try:
        model = VibeVoiceStreamingForConditionalGenerationInference.from_pretrained(
            model_name, torch_dtype=dtype, device_map=device, attn_implementation=attn_impl
        )
    except Exception:
        model = VibeVoiceStreamingForConditionalGenerationInference.from_pretrained(
            model_name, torch_dtype=dtype, device_map=device, attn_implementation="sdpa"
        )
    model.eval()
    model.set_ddpm_inference_steps(num_steps=5)

    return {"processor": processor, "model": model, "device": device, "voices_dir": voices_dir}


def _voice_prefill(voice: str, ctx: dict):
    """Load (and cache) a voice preset's pre-computed prompt tensor."""
    if voice in _voice_prefill_cache:
        return _voice_prefill_cache[voice]

    import os
    import torch
    from transformers.cache_utils import DynamicCache
    from transformers.modeling_outputs import BaseModelOutputWithPast

    path = os.path.join(ctx["voices_dir"], f"{voice}.pt")
    if not os.path.exists(path):
        # Fall back to any voice file whose name contains the requested one,
        # e.g. "en-Emma_woman" from a bare "Emma".
        candidates = [
            f for f in os.listdir(ctx["voices_dir"])
            if voice.lower() in f.lower()
        ]
        if not candidates:
            raise FileNotFoundError(f"No voice preset matching '{voice}' in {ctx['voices_dir']}")
        path = os.path.join(ctx["voices_dir"], candidates[0])

    with torch.serialization.safe_globals([BaseModelOutputWithPast, DynamicCache]):
        prefilled = torch.load(path, map_location=ctx["device"], weights_only=True)
    _voice_prefill_cache[voice] = prefilled
    return prefilled


def load_model(model_name: str):
    """Load TTS model. Auto-selects MLX on Apple Silicon, PyTorch otherwise."""
    global _model, _backend, _model_sample_rate

    if is_apple_silicon():
        try:
            from mlx_audio.tts.utils import load_model as mlx_load

            logger.info("Using MLX backend")
            logger.info(f"Loading model: {model_name}")
            _model = mlx_load(model_name)
            _backend = "mlx"
            logger.info("TTS model loaded successfully")
            return
        except ImportError:
            logger.warning("mlx-audio not installed, falling back to PyTorch")

    # PyTorch fallback
    try:
        logger.info("Using PyTorch backend")
        logger.info(f"Loading model: {model_name}")

        # VibeVoice-Realtime-0.5B uses the vibevoice package from
        # https://github.com/microsoft/VibeVoice
        # Install: pip install -e .[streamingtts]  (from cloned repo)
        #
        # NOTE: vibevoice.realtime.model.VibeVoiceRealtimeModel does not
        # exist in the current microsoft/VibeVoice repo (verified: only
        # vibevoice.modular / .processor / .schedule / .configs / .scripts
        # exist). This import always fails and falls through to
        # _load_streaming_pt below, which uses the real current API.
        from vibevoice.realtime.model import VibeVoiceRealtimeModel

        _model = VibeVoiceRealtimeModel.from_pretrained(model_name)
        _backend = "pytorch"
        logger.info("TTS model loaded successfully")
    except ImportError:
        try:
            _model = _load_streaming_pt(model_name, _voices_dir_arg)
            _backend = "streaming_pt"
            logger.info("TTS model loaded via vibevoice.modular streaming API")
            return
        except Exception as e:
            logger.warning(f"streaming_pt backend failed ({e}), trying generic transformers")
        # Lighter fallback: try loading through transformers directly
        try:
            import torch
            from transformers import AutoModelForCausalLM, AutoProcessor

            processor = AutoProcessor.from_pretrained(
                model_name, trust_remote_code=True
            )
            model = AutoModelForCausalLM.from_pretrained(
                model_name,
                trust_remote_code=True,
                torch_dtype=(
                    torch.float16 if torch.cuda.is_available() else torch.float32
                ),
                device_map="auto" if torch.cuda.is_available() else None,
            )
            _model = {"processor": processor, "model": model}
            _backend = "pytorch_transformers"
            logger.info("TTS model loaded via transformers")
        except Exception as e:
            raise RuntimeError(
                f"Failed to load TTS model: {e}\n"
                "Install mlx-audio (Apple Silicon): pip install mlx-audio\n"
                "Install PyTorch: pip install vibevoice  OR  pip install torch transformers"
            )


def resample(audio: np.ndarray, orig_sr: int, target_sr: int) -> np.ndarray:
    """Resample audio using linear interpolation (fast, good enough for speech)."""
    if orig_sr == target_sr:
        return audio
    ratio = target_sr / orig_sr
    n_out = int(len(audio) * ratio)
    indices = np.arange(n_out) / ratio
    indices_floor = np.floor(indices).astype(int)
    indices_ceil = np.minimum(indices_floor + 1, len(audio) - 1)
    frac = indices - indices_floor
    return audio[indices_floor] * (1 - frac) + audio[indices_ceil] * frac


def synthesize_speech(text: str, voice: str = "en-Emma_woman") -> bytes:
    """Generate speech from text and return raw PCM bytes (16 kHz, s16le, mono)."""
    with _synthesis_lock:
        return _synthesize_speech_locked(text, voice)


def _synthesize_speech_locked(text: str, voice: str) -> bytes:
    if _backend == "streaming_pt":
        import copy
        import torch

        processor = _model["processor"]
        model = _model["model"]
        device = _model["device"]
        prefilled = _voice_prefill(voice, _model)

        inputs = processor.process_input_with_cached_prompt(
            text=text,
            cached_prompt=prefilled,
            padding=True,
            return_tensors="pt",
            return_attention_mask=True,
        )
        for k, v in inputs.items():
            if torch.is_tensor(v):
                inputs[k] = v.to(device)

        with torch.no_grad():
            outputs = model.generate(
                **inputs,
                max_new_tokens=None,
                cfg_scale=1.5,
                tokenizer=processor.tokenizer,
                generation_config={"do_sample": False},
                verbose=False,
                all_prefilled_outputs=copy.deepcopy(prefilled),
            )

        if not outputs.speech_outputs or outputs.speech_outputs[0] is None:
            return b""

        audio = outputs.speech_outputs[0]
        if torch.is_tensor(audio):
            audio = audio.float().cpu().numpy()
        audio = np.asarray(audio, dtype=np.float32).reshape(-1)

        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    elif _backend == "mlx":
        audio_chunks = []
        for result in _model.generate(text, voice=voice):
            chunk = np.array(result.audio, dtype=np.float32)
            audio_chunks.append(chunk)

        if not audio_chunks:
            return b""

        audio = np.concatenate(audio_chunks)

        # Resample to 16 kHz
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)

        # Float → int16 PCM
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    elif _backend == "pytorch":
        # vibevoice package path
        audio = _model.synthesize(
            text, speaker_name=voice.split("-")[-1] if "-" in voice else voice
        )
        if isinstance(audio, np.ndarray):
            pass
        else:
            import torch

            audio = audio.cpu().numpy()

        audio = audio.astype(np.float32)
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    elif _backend == "pytorch_transformers":
        import torch

        processor = _model["processor"]
        model = _model["model"]

        inputs = processor(text=text, return_tensors="pt", trust_remote_code=True)
        if torch.cuda.is_available():
            inputs = {k: v.cuda() for k, v in inputs.items()}

        with torch.no_grad():
            output = model.generate(**inputs, max_new_tokens=4096)

        # Extract audio from model output (implementation depends on model)
        audio = output.cpu().numpy().astype(np.float32)
        audio = resample(audio, _model_sample_rate, TARGET_SAMPLE_RATE)
        audio = np.clip(audio * 32767, -32768, 32767).astype(np.int16)
        return audio.tobytes()

    return b""


# ── FastAPI app ──────────────────────────────────────────────────────────────

app = FastAPI(title="VibeVoice TTS")


class SynthesizeRequest(BaseModel):
    text: str
    voice: str = "en-Emma_woman"


@app.post("/synthesize")
async def synthesize_endpoint(req: SynthesizeRequest):
    if not req.text.strip():
        raise HTTPException(status_code=400, detail="text must not be empty")
    try:
        import asyncio

        pcm_bytes = await asyncio.get_event_loop().run_in_executor(
            None, synthesize_speech, req.text, req.voice
        )
        logger.info(f"Synthesized {len(pcm_bytes)} bytes for {len(req.text)} chars")
        return Response(content=pcm_bytes, media_type="audio/pcm")
    except Exception as e:
        logger.error(f"Synthesis error: {e}", exc_info=True)
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/health")
async def health():
    return {"status": "ok"}


# ── Entrypoint ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="VibeVoice TTS HTTP Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8300, help="Bind port")
    parser.add_argument("--model", default=None, help="Model name or path")
    parser.add_argument(
        "--voices-dir",
        default=None,
        help="Directory of streaming-model voice preset .pt files "
        "(VibeVoice repo's demo/voices/streaming_model)",
    )
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper()),
        format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    )

    if args.model is None:
        args.model = (
            "mlx-community/VibeVoice-Realtime-0.5B-6bit"
            if is_apple_silicon()
            else "microsoft/VibeVoice-Realtime-0.5B"
        )

    _voices_dir_arg = args.voices_dir
    load_model(args.model)

    uvicorn.run(app, host=args.host, port=args.port, log_level=args.log_level.lower())
