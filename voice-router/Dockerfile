# Voxie voice router: Kokoro on the GPU, Sarvam for Indian languages.
FROM python:3.11-slim

# build-essential and cmake: pyopenjtalk (Japanese, via misaki[ja]) builds
# from source.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsndfile1 build-essential cmake \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN pip install --no-cache-dir torch==2.9.1 --index-url https://download.pytorch.org/whl/cu128
COPY requirements.txt .
# misaki[ja] pulls the full unidic dictionary, which crashes MeCab without a
# separate 1GB download; unidic-lite is enough.
RUN pip install --no-cache-dir -r requirements.txt \
 && (pip uninstall -y unidic || true)
COPY server.py spoken_names.example.json ./

# Kokoro's weights download from Hugging Face on first start; mount a volume
# at /root/.cache/huggingface to keep them.
EXPOSE 8300
ENTRYPOINT ["python", "server.py", "--host", "0.0.0.0", "--port", "8300"]
