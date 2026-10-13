@echo off
setlocal
cd /d "%~dp0"
chcp 65001 > nul

echo ========================================================
echo   WebCodex: Sbor komponentov (Agent + Gate)
echo ========================================================
echo.

where go >nul 2>nul
if %errorlevel% neq 0 (
    if exist "C:\Program Files\Go\bin\go.exe" (
        set "PATH=C:\Program Files\Go\bin;%PATH%"
    ) else if exist "C:\Go\bin\go.exe" (
        set "PATH=C:\Go\bin;%PATH%"
    ) else (
        echo [ERROR] Go compiler not found!
        echo.
        echo To install Go automatically via winget, run:
        echo   winget install GoLang.Go
        echo.
        echo Or download installer from:
        echo   https://go.dev/dl/
        echo.
        echo Restart terminal or this script after installation.
        echo.
        if /i not "%~1"=="--no-pause" pause
        exit /b 1
    )
)

echo [OK] Found Go:
go version
echo.

echo --------------------------------------------------------
echo [1/2] Building webcodex-agent.exe ...
go build -ldflags="-s -w" -o webcodex-agent.exe ./cmd/agent
if %errorlevel% neq 0 (
    echo [ERROR] Failed to compile webcodex-agent.exe
    if /i not "%~1"=="--no-pause" pause
    exit /b 1
)
echo [SUCCESS] Created webcodex-agent.exe

echo.
echo --------------------------------------------------------
echo [2/2] Building bin/webcodex-gate.exe ...
if not exist "bin" mkdir "bin"
go build -ldflags="-s -w" -o bin\webcodex-gate.exe ./cmd/gate
if %errorlevel% neq 0 (
    echo [ERROR] Failed to compile bin\webcodex-gate.exe
    if /i not "%~1"=="--no-pause" pause
    exit /b 1
)
echo [SUCCESS] Created bin\webcodex-gate.exe

powershell -NoProfile -NonInteractive -Command "Compress-Archive -LiteralPath 'browser-extension\manifest.json','browser-extension\background.js','browser-extension\popup.html','browser-extension\popup.js' -DestinationPath 'bin\webcodex-browser-extension.zip' -Force"
if %errorlevel% neq 0 (
    echo [ERROR] Failed to package browser extension
    exit /b 1
)
echo [SUCCESS] Created bin\webcodex-browser-extension.zip

echo.
echo ========================================================
echo   Build finished successfully!
echo ========================================================
echo.
echo  - Worker agent: webcodex-agent.exe
echo  - Gate server:  bin\webcodex-gate.exe
echo.
echo Next steps:
echo  1. Get agent token and start script from /admin
echo  2. Run start-agent-*.bat alongside webcodex-agent.exe
echo  3. Connect parameters in ChatGPT settings / custom GPT
echo ========================================================
echo.
if /i not "%~1"=="--no-pause" pause
exit /b 0

