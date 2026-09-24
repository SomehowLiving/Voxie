"""A scripted caller over real WebRTC, through the quickstart page server.
Speaks first, asks a question, says goodbye, and prints the conversation as
the agent saw it. Saves everything the agent said to audio/agent_<lang>.wav.

    pip install git+https://github.com/streamcoreai/python-sdk soundfile librosa
    python tools/callers/make_callers.py ta
    python tools/callers/call.py ta                  # a Tamil caller, nothing on record
    python tools/callers/call.py ta --language fr    # ...whose record says French
    python tools/callers/call.py hi --india --phone  # Indian number, phone-line audio

Flags:
  --language <code>  the caller's language on record (the listener's starting guess)
  --india            the caller has an Indian number
  --phone            degrade the caller's audio to a phone line (8kHz mu-law + noise)
  --page <url>       the quickstart page server (default http://127.0.0.1:8400)
"""
import argparse
import asyncio
import json
import os
import threading
import time
import urllib.request

import numpy as np
import soundfile as sf
import streamcore

HERE = os.path.dirname(os.path.abspath(__file__))
FRAME, SR = streamcore.FRAME_SIZE, streamcore.SAMPLE_RATE


def parse_args():
    p = argparse.ArgumentParser()
    p.add_argument("lang")
    p.add_argument("--language", default="")
    p.add_argument("--india", action="store_true")
    p.add_argument("--phone", action="store_true")
    p.add_argument("--page", default="http://127.0.0.1:8400")
    return p.parse_args()


ARGS = parse_args()
T0 = time.monotonic()


def log(msg: str) -> None:
    print(f"(+{time.monotonic() - T0:5.1f}s) {msg}", flush=True)


def load(name: str) -> np.ndarray:
    y, sr = sf.read(os.path.join(HERE, "audio", f"{name}_{ARGS.lang}.wav"), dtype="int16")
    if not ARGS.phone:
        return y
    import audioop  # stdlib up to Python 3.12

    import librosa

    f = librosa.resample(y.astype(np.float32) / 32768, orig_sr=sr, target_sr=8000)
    f = f + np.random.default_rng(0).normal(0, 0.004, len(f))
    pcm = audioop.ulaw2lin(audioop.lin2ulaw((np.clip(f, -1, 1) * 32767).astype(np.int16).tobytes(), 2), 2)
    f = librosa.resample(np.frombuffer(pcm, dtype=np.int16).astype(np.float32) / 32768, orig_sr=8000, target_sr=sr)
    return (np.clip(f, -1, 1) * 32767).astype(np.int16)


def follow_conversation() -> None:
    """Prints each turn as the page's live feed reports it."""
    try:
        with urllib.request.urlopen(f"{ARGS.page}/api/events", timeout=300) as feed:
            for raw in feed:
                line = raw.decode("utf-8", "replace").strip()
                if line.startswith("data: "):
                    ev = json.loads(line[6:])
                    who = {"caller": "caller", "agent": "agent "}.get(ev.get("kind"), ev.get("kind"))
                    if ev.get("language"):
                        who += f" [{ev['language']}]"
                    log(f"{who}: {ev.get('text', '')}")
    except Exception:
        pass


async def main() -> None:
    threading.Thread(target=follow_conversation, daemon=True).start()
    body = json.dumps({"name": "Sofía", "language": ARGS.language, "region": "IN" if ARGS.india else ""}).encode()
    urllib.request.urlopen(urllib.request.Request(f"{ARGS.page}/api/call", data=body, headers={"Content-Type": "application/json"}))

    agent_pcm, frames, ended = [], [], []
    client = streamcore.Client(
        config=streamcore.Config(whip_endpoint=f"{ARGS.page}/whip"),
        events=streamcore.EventHandler(
            on_status_change=lambda s: ended.append(1) if s.name in ("DISCONNECTED", "FAILED", "CLOSED") else None
        ),
    )
    await client.connect()
    log(f"connected: caller speaks {ARGS.lang}, on record {ARGS.language or '-'}, {'Indian number' if ARGS.india else 'elsewhere'}")

    queue: list[np.ndarray] = []

    async def sender():
        silence = np.zeros(FRAME, dtype=np.int16)
        while True:
            if queue:
                f, queue[0] = queue[0][:FRAME], queue[0][FRAME:]
                if len(queue[0]) == 0:
                    queue.pop(0)
                if len(f) < FRAME:
                    f = np.pad(f, (0, FRAME - len(f)))
            else:
                f = silence
            await client.send_pcm(f)
            await asyncio.sleep(FRAME / SR)

    async def receiver():
        while True:
            pcm = await client.recv_pcm()
            agent_pcm.append(pcm)
            frames.append((time.monotonic(), int(np.abs(pcm).max())))

    tasks = [asyncio.create_task(sender()), asyncio.create_task(receiver())]

    async def say(name: str) -> float:
        queue.append(load(name))
        while queue:
            await asyncio.sleep(0.02)
        return time.monotonic()

    async def agent_turn(after: float, timeout: float = 60) -> None:
        deadline = time.monotonic() + timeout
        started = None
        while time.monotonic() < deadline and not ended:
            loud = [t for t, a in frames if a > 500 and t > after]
            if loud and started is None:
                started = loud[0]
                log(f"agent starts speaking {started - after:.1f}s after the caller stopped")
            if loud and time.monotonic() - loud[-1] > 2.5:
                return
            await asyncio.sleep(0.2)

    await asyncio.sleep(0.8)
    for name in ("hello", "ask", "bye"):
        await agent_turn(await say(name))
        if ended:
            break
    for _ in range(40):
        if ended:
            break
        await asyncio.sleep(0.25)
    log(f"call ended by the agent: {bool(ended)}")
    for t in tasks:
        t.cancel()
    await asyncio.gather(*tasks, return_exceptions=True)
    await client.disconnect()
    if agent_pcm:
        sf.write(os.path.join(HERE, "audio", f"agent_{ARGS.lang}.wav"), np.concatenate(agent_pcm), SR, subtype="PCM_16")


asyncio.run(main())
