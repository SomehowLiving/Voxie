#!/usr/bin/env bash
# Builds Voxie's two images, checks that each one starts, and with --push
# publishes them.
#
#   tools/release/build-images.sh v0.2.0           # build and check only
#   tools/release/build-images.sh v0.2.0 --push    # ...then publish
#
# Images: $IMAGE_REPO/voxie (the server) and $IMAGE_REPO/voxie-voice-router,
# tagged <version>, <short commit> and latest. No other tag is ever written,
# so tags others pin (e.g. a commit tag a deployment runs on) stay as they are.
# Each image carries LICENSE and NOTICE in /licenses and OCI labels.
set -euo pipefail

VERSION=${1:?usage: build-images.sh <version> [--push]}
PUSH=${2:-}
REPO=${IMAGE_REPO:-ghcr.io/somehowliving}
SHA=$(git rev-parse --short HEAD)
cd "$(git rev-parse --show-toplevel)"

LABELS=(--label org.opencontainers.image.source=https://github.com/SomehowLiving/Voxie
        --label org.opencontainers.image.licenses=Apache-2.0
        --label "org.opencontainers.image.version=$VERSION"
        --label "org.opencontainers.image.revision=$(git rev-parse HEAD)"
        --label "org.opencontainers.image.description=Voxie: real-time voice agents that speak the caller's language")

build() { # name, context
  local base="voxie-build/$1:$SHA"
  docker build --platform linux/amd64 -t "$base" "$2"
  # LICENSE and NOTICE live at the repo root, outside the router's context.
  printf 'FROM %s\nCOPY LICENSE NOTICE /licenses/\n' "$base" |
    docker build --platform linux/amd64 "${LABELS[@]}" -t "$REPO/$1:$VERSION" -f - .
}

build voxie .
build voxie-voice-router voice-router

echo "== checking the images"
for name in voxie voxie-voice-router; do
  docker run --rm --entrypoint sh "$REPO/$name:$VERSION" -c 'test -s /licenses/LICENSE && test -s /licenses/NOTICE' ||
    { echo "$name: /licenses is missing LICENSE or NOTICE"; exit 1; }
done
# The server starts and listens with no config.
cid=$(docker run -d --rm "$REPO/voxie:$VERSION")
trap 'docker stop "$cid" >/dev/null 2>&1 || true' EXIT
for _ in $(seq 1 30); do
  docker logs "$cid" 2>&1 | grep -q "listening on" && break
  sleep 1
done
docker logs "$cid" 2>&1 | grep -q "listening on" || { docker logs "$cid"; echo "voxie: the server didn't start"; exit 1; }
docker stop "$cid" >/dev/null; trap - EXIT
# The router loads everything it needs before it can speak. Starting it for
# real downloads ~1.5 GB of voices, so this loads the libraries and the
# language models instead: what broke the 664c63e image (spaCy's English
# model) fails here.
docker run --rm --entrypoint python "$REPO/voxie-voice-router:$VERSION" -c '
import en_core_web_sm; en_core_web_sm.load()
import kokoro, piper, misaki, lingua, fastapi, uvicorn, soundfile, scipy
import ast; ast.parse(open("server.py").read())
print("voice router: libraries and models load")'

if [ "$PUSH" != "--push" ]; then
  echo "Built and checked $VERSION ($SHA). Not pushed: add --push."
  exit 0
fi
for name in voxie voxie-voice-router; do
  for tag in "$SHA" latest; do
    docker tag "$REPO/$name:$VERSION" "$REPO/$name:$tag"
  done
  for tag in "$VERSION" "$SHA" latest; do
    docker push "$REPO/$name:$tag"
  done
done
echo "Pushed $VERSION ($SHA) and latest."
