@echo off
REM POLL_EXAMPLE.bat - Polling workflow with sleep
REM
REM This script demonstrates a polling pattern where the script:
REM 1. Checks for a condition (simulated by a counter file)
REM 2. If not met, sleeps briefly and resets
REM 3. If met, transitions to a processing state
REM
REM This is a key use case for script states - polling without
REM consuming LLM tokens or risking session timeouts.

setlocal enabledelayedexpansion

REM Determine counter file location
if defined RAYMOND_STATE_DIR (
    set "poll_counter=%RAYMOND_STATE_DIR%\poll_counter.txt"
) else (
    set "poll_counter=%TEMP%\poll_counter.txt"
)

set /a poll_target=3

REM Initialize counter
set /a count=0

REM Read counter if file exists
if exist "%poll_counter%" (
    for /f "usebackq tokens=*" %%a in ("%poll_counter%") do set /a count=%%a
)

REM Increment counter
set /a count=count+1

REM Write counter to file (using set /p trick to avoid newline issues)
<nul set /p "=%count%" > "%poll_counter%"

echo === Poll Iteration %count% ===
echo Checking for work... (simulated condition: poll %poll_target% times)

if %count% lss %poll_target% (
    echo No work found. Sleeping for 1 second before next poll...
    timeout /t 1 /nobreak >nul
    echo Resuming poll.
    echo ^<reset^>POLL_EXAMPLE.bat^</reset^>
) else (
    echo Work found^^! Cleaning up poll counter and processing.
    del /f /q "%poll_counter%" 2>nul
    echo ^<goto^>POLL_PROCESS.md^</goto^>
)

endlocal
