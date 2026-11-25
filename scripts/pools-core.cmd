@rem Path: scripts/pools-core.cmd | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
@rem What/why: double-click entry point for the pools-core launcher (WO-T5, WWPP-R41.1).
@rem   No arguments  -> opens the PowerShell 7 TUI menu (one screen, numbered items).
@rem   With arguments -> passes them straight to pools-core.ps1, e.g.
@rem       pools-core.cmd start | stop | status | restart | logs | tail | config | store | diagnostics | test
@echo off
setlocal EnableDelayedExpansion
set "PS_SCRIPT=%~dp0pools-core.ps1"

where pwsh >nul 2>&1
if errorlevel 1 (
    echo pools-core.cmd: PowerShell 7 "pwsh" was not found on PATH.
    echo Install or add PowerShell 7 to PATH, then run this file again.
    pause
    exit /b 9009
)

if "%~1"=="" goto :menu

pwsh -NoProfile -File "%PS_SCRIPT%" %*
exit /b %ERRORLEVEL%

:menu
pwsh -NoProfile -File "%PS_SCRIPT%" -Action menu
set "RC=%ERRORLEVEL%"
if not "%RC%"=="0" (
    echo.
    echo pools-core.cmd: the launcher exited with code %RC%.
)
echo.
pause
exit /b %RC%
