@echo off
REM SCRIPT_RESET.bat - Reset transition test
REM
REM This script demonstrates the reset transition by maintaining
REM a counter in a file. It runs 3 iterations (resetting twice),
REM then finishes with a result on the third iteration.
REM Reset clears the agent's conversation context but keeps workflow state.

setlocal enabledelayedexpansion

REM Determine counter file location
set "counter_file=%TEMP%\reset_counter.txt"

REM Initialize counter
set /a count=0

REM Read counter if file exists
if exist "%counter_file%" (
    for /f "usebackq tokens=*" %%a in ("%counter_file%") do set /a count=%%a
)

REM Increment counter
set /a count=count+1

REM Write counter to file (using set /p trick to avoid newline issues)
<nul set /p "=%count%" > "%counter_file%"

echo Reset iteration: %count% of 3

if %count% lss 3 (
    echo Resetting to run again...
    echo ^<reset^>SCRIPT_RESET.bat^</reset^>
) else (
    echo Counter reached limit. Cleaning up and finishing.
    del /f /q "%counter_file%" 2>nul
    echo ^<result^>Completed after 3 iterations ^(2 resets^)^</result^>
)

endlocal
