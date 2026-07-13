#!/bin/bash
# Headless llamafile runner for Linux / Hetzner
# Usage: ./run_llamafile_judge.sh

PORT=1234
MODEL_URL="https://huggingface.co/JMingo/gemma-4-E2B-it-ultra-uncensored-heretic-UD-GGUF/resolve/main/gemma-4-E2B-it-ultra-uncensored-heretic-Q4_K_M.gguf"
MODEL_FILE="gemma-4-E2B-it-ultra-uncensored-heretic-Q4_K_M.gguf"
LLAMAFILE_URL="https://github.com/Mozilla-Ocho/llamafile/releases/download/0.8.8/llamafile-0.8.8"
LLAMAFILE_BIN="llamafile"

echo "=== Llamafile Headless Runner ==="

# Download llamafile if missing
if [ ! -f "$LLAMAFILE_BIN" ]; then
    echo "Downloading llamafile binary..."
    curl -L "$LLAMAFILE_URL" -o "$LLAMAFILE_BIN"
    chmod +x "$LLAMAFILE_BIN"
fi

# Download GGUF model if missing
if [ ! -f "$MODEL_FILE" ]; then
    echo "Downloading Gemma 4 Heretic GGUF model..."
    curl -L "$MODEL_URL" -o "$MODEL_FILE"
fi

echo "Starting llamafile on port $PORT (headless)..."
./$LLAMAFILE_BIN -m "$MODEL_FILE" --host 127.0.0.1 --port "$PORT" --nobrowser
