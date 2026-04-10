#!/bin/sh
# One-time setup for Alpine VPS: installs ffmpeg and yt-dlp.
# Run this once before the first deploy: sh setup.sh

set -e

echo "Installing ffmpeg..."
apk add --no-cache ffmpeg

echo "Installing yt-dlp..."
apk add --no-cache yt-dlp

echo "Done. Verify with:"
echo "  ffmpeg -version"
echo "  yt-dlp --version"
