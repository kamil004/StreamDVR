#!/bin/bash
# Stream DVR QPKG builder for QNAP (TS-464 / QTS 5, x86_64).
#
#   1. compiles the Go twitchdvr module for linux/amd64
#   2. packages payload/ + control/ into a QDK self-extracting .qpkg
#
# Usage: ./build.sh
set -e
cd "$(dirname "$0")"

echo "=== Building streamdvr-web (linux/amd64) ==="
pushd ../Sources >/dev/null
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../payload/twitchdvr/streamdvr-web .
popd >/dev/null
chmod +x payload/twitchdvr/streamdvr-web
echo "streamdvr-web: $(ls -1 payload/twitchdvr/streamdvr-web | xargs ls -l | awk '{print $5}') bytes"

echo "=== Assembling QPKG 3.0.0 ==="
python3 build_qpkg.py

echo ""
echo "Output: $(dirname "$0")/../build/StreamDVR_3.0.0_x86_64.qpkg"
echo "Install via App Center > Install from file. Web UI: http://<QNAP_IP>:8090"