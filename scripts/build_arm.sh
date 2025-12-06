#!/usr/bin/env bash
set -euo pipefail

# Build a Linux/ARM and Linux/ARM64 binary of the client
# Usage: scripts/build_arm.sh

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
OUTPUT_DIR="$PROJECT_ROOT/build"
mkdir -p "$OUTPUT_DIR"

echo "Building linux/arm (GOARM=7)"
GOOS=linux GOARCH=arm GOARM=7 go build -o "$OUTPUT_DIR/client-linux-arm" ./cmd/client

echo "Building linux/arm64"
GOOS=linux GOARCH=arm64 go build -o "$OUTPUT_DIR/client-linux-arm64" ./cmd/client

ls -lh "$OUTPUT_DIR"

echo "Done. Binaries are in $OUTPUT_DIR"
