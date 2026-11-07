#!/bin/bash
# Stream DVR QPKG builder for QNAP (TS-464 / QTS 5, x86_64).
#
#   1. compiles the Go twitchdvr module for linux/amd64
#   2. packages payload/ + control/ into a QDK self-extracting .qpkg
#
# Usage: ./build.sh
set -e
cd "$(dirname "$0")"

# Read version from build_version.txt (shared with macOS build.sh)
VERSION_FILE="$(cd "$SCRIPT_DIR/../.." 2>/dev/null && pwd)/../build_version.txt"
if [ -z "$VERSION_FILE" ] || [ ! -f "$VERSION_FILE" ]; then
    VERSION_FILE="$(cd "$(dirname "$0")/../.." && pwd)/build_version.txt"
fi
if [ -f "$VERSION_FILE" ]; then
    VER=$(cat "$VERSION_FILE" | tr -d '[:space:]')
else
    VER="3.0.0"
fi

# Update qpkg.cfg with current version
sed -i '' "s/QPKG_VER=.*/QPKG_VER="$VER"/" control/qpkg.cfg

echo "=== Building streamdvr-web (linux/amd64) ==="
pushd ../Sources >/dev/null
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../QPKG/payload/twitchdvr/streamdvr-web .
popd >/dev/null
chmod +x payload/twitchdvr/streamdvr-web
echo "streamdvr-web: $(ls -1 payload/twitchdvr/streamdvr-web | xargs ls -l | awk '{print $5}') bytes"

# Copy build_version.txt into payload (so the running binary can find it)
cp "$(dirname "$0")/../build_version.txt" payload/build_version.txt 2>/dev/null || true

echo "=== Assembling QPKG $VER ==="
python3 build_qpkg.py

echo ""
echo "Output: $(dirname "$0")/../build/StreamDVR_${VER}_x86_64.qpkg"
echo "Install via App Center > Install from file. Web UI: http://<QNAP_IP>:8090"