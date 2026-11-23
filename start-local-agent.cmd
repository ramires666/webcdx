@echo off
setlocal DisableDelayedExpansion
cd /d "%~dp0"
if not exist "bin\aix\webcodex-agent.exe" (
    echo Local agent EXE is missing. See ai-studio-extension\BUILD.ru.md.
    pause
    exit /b 1
)

if not defined WEBCODEX_OPENCODE_PATH if exist "%APPDATA%\npm\node_modules\@opencode\cli\bin\opencode.exe" (
    set "WEBCODEX_OPENCODE_PATH=%APPDATA%\npm\node_modules\@opencode\cli\bin\opencode.exe"
)
if not defined WEBCODEX_OPENCODE_PATH goto start_agent
if defined WEBCODEX_TASK_KEY_FILE goto start_agent
if defined WEBCODEX_TASK_OPENMCP_ENV goto start_agent
if defined WEBCODEX_TASK_OPENCODE_AUTH goto start_agent
if defined WEBCODEX_TASK_OPENCODE_DB goto start_agent
for /f "delims=" %%D in ('""%WEBCODEX_OPENCODE_PATH%" debug paths db"') do (
    if not defined WEBCODEX_TASK_OPENCODE_DB if exist "%%D" set "WEBCODEX_TASK_OPENCODE_DB=%%D"
)

:start_agent
"bin\aix\webcodex-agent.exe" --local
