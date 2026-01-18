@echo off
REM SCRIPT_GOTO.bat - Simple goto transition test
REM
REM This script demonstrates a basic script state that transitions
REM to a markdown state using <goto>.

echo Script state executing...
echo Environment variables:
echo   RAYMOND_WORKFLOW_ID=%RAYMOND_WORKFLOW_ID%
echo   RAYMOND_AGENT_ID=%RAYMOND_AGENT_ID%
echo   RAYMOND_STATE_DIR=%RAYMOND_STATE_DIR%

echo ^<goto^>SCRIPT_TARGET.md^</goto^>
