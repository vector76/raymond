@echo off
REM HYBRID_START.bat - First state in hybrid workflow
REM
REM This workflow demonstrates seamless transitions between
REM script states and markdown states:
REM   HYBRID_START.bat -> HYBRID_MIDDLE.md -> HYBRID_END.bat -> result

echo === Hybrid Workflow Started ===
echo This is a script state performing initial setup.
echo.
echo Gathering system information...
echo   Date: %DATE% %TIME%
echo   User: %USERNAME%
echo   PWD:  %CD%
echo.
echo Setup complete. Transitioning to markdown state for LLM processing.

echo ^<goto^>HYBRID_MIDDLE.md^</goto^>
