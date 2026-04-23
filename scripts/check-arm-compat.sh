#!/bin/sh
# scripts/check-arm-compat.sh
# Simple helper to check if container images expose an ARM64 manifest
# Usage: ./scripts/check-arm-compat.sh <image1> <image2> ...
# If no images are provided, the script prints usage and exits 0.

set -eu

if [ "$#" -eq 0 ]; then
  # If no images provided, attempt to read common images from ENV_FILE (VIPAS_IMAGE/VIPAS_VERSION)
   . ./deploy/versions.env 2>/dev/null || true
   ENV_FILE="${ENV_FILE:-/opt/vipas/.env}"
   . "$ENV_FILE" 2>/dev/null || true
  IMAGES_TO_CHECK="${VIPAS_IMAGE:-ghcr.io/victorgomez09/vipas:${VIPAS_VERSION:-latest}} postgres:18-alpine"
  echo "No images supplied — will check defaults: $IMAGES_TO_CHECK"
  set -- $IMAGES_TO_CHECK
fi

check_cmd=""
if command -v docker >/dev/null 2>&1 && docker manifest inspect --help >/dev/null 2>&1; then
  check_cmd="docker"
elif command -v skopeo >/dev/null 2>&1; then
  check_cmd="skopeo"
else
  echo "[warn] Neither 'docker manifest inspect' nor 'skopeo' found; cannot check manifests. Install skopeo or use a machine with Docker CLI supporting 'manifest inspect'." >&2
  exit 2
fi

missing=0
for img in "$@"; do
  echo "Checking image: $img"
  if [ "$check_cmd" = "docker" ]; then
    if docker manifest inspect "$img" >/tmp/manifest.json 2>/dev/null; then
      if grep -q '"architecture": "arm64"' /tmp/manifest.json || grep -qi 'arm64' /tmp/manifest.json; then
        echo "  -> OK: contains arm64 manifest"
      else
        echo "  -> MISSING: no arm64 manifest detected"
        missing=$((missing+1))
      fi
      rm -f /tmp/manifest.json
    else
      echo "  -> FAIL: could not fetch manifest (image may not exist or network error)"
      missing=$((missing+1))
    fi
  else
    # skopeo path
    if skopeo inspect --raw docker://"$img" >/tmp/manifest.json 2>/dev/null; then
      if grep -q '"arm64"' /tmp/manifest.json || grep -q '"architecture":"arm64"' /tmp/manifest.json; then
        echo "  -> OK: contains arm64 manifest"
      else
        echo "  -> MISSING: no arm64 manifest detected"
        missing=$((missing+1))
      fi
      rm -f /tmp/manifest.json
    else
      echo "  -> FAIL: could not fetch manifest via skopeo"
      missing=$((missing+1))
    fi
  fi
done

if [ $missing -eq 0 ]; then
  echo "All images checked appear to include arm64 manifests."
  exit 0
else
  echo "Some images are missing arm64 manifests or could not be inspected. Count: $missing" >&2
  exit 1
fi
