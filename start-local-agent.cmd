@echo off
setlocal
cd /d "%~dp0"
if not exist "bin\ai-studio-bridge\webcodex-agent.exe" (
    echo Local agent EXE is missing. See ai-studio-extension\BUILD.ru.md.
    pause
    exit /b 1
)
"bin\ai-studio-bridge\webcodex-agent.exe" --local
