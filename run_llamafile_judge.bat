@echo off
REM Llamafile runner for Windows
REM Usage: run_llamafile_judge.bat

set PORT=1234
set MODEL_URL=https://huggingface.co/JMingo/gemma-4-E2B-it-ultra-uncensored-heretic-UD-GGUF/resolve/main/gemma-4-E2B-it-ultra-uncensored-heretic-UD.Q4_K_M.gguf
set MODEL_FILE=gemma-4-E2B-it-ultra-uncensored-heretic-UD.Q4_K_M.gguf
set LLAMAFILE_URL=https://github.com/Mozilla-Ocho/llamafile/releases/download/0.8.8/llamafile-0.8.8.exe
set LLAMAFILE_BIN=llamafile.exe

echo === Llamafile Windows Runner ===

if not exist "%LLAMAFILE_BIN%" (
    echo Downloading llamafile.exe...
    curl -L %LLAMAFILE_URL% -o %LLAMAFILE_BIN%
)

if not exist "%MODEL_FILE%" (
    echo Downloading Gemma 4 Heretic GGUF model...
    curl -L %MODEL_URL% -o %MODEL_FILE%
)

echo Starting llamafile on port %PORT%...
%LLAMAFILE_BIN% -m %MODEL_FILE% --host 127.0.0.1 --port %PORT% --nobrowser
