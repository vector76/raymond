@echo off
REM SCRIPT_RESULT.bat - Result with payload test
REM
REM This script demonstrates a script state that returns a result
REM with a payload. This is useful for subroutine-style workflows
REM where a script performs work and returns data to the caller.

echo Performing some deterministic work...

REM Gather data
set timestamp=%DATE% %TIME%
set hostname_info=%COMPUTERNAME%

REM Create a result payload
set payload=Script completed at %timestamp% on %hostname_info%

echo Work complete. Returning result.
echo ^<result^>%payload%^</result^>
