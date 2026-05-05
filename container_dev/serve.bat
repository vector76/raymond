@echo off
setlocal enabledelayedexpansion

:: Load container config from .env.container
if not exist .env.container (
    echo .env.container not found.
    exit /b 1
)
for /f "tokens=1,2 delims==" %%a in (.env.container) do (
    if "%%a"=="CONTAINER_NAME" set CONTAINER_NAME=%%b
)
if not defined CONTAINER_NAME (
    echo CONTAINER_NAME not set in .env.container.
    exit /b 1
)

:: Ensure container exists
docker inspect %CONTAINER_NAME% >nul 2>&1
if errorlevel 1 (
    echo Container %CONTAINER_NAME% does not exist. Run rebuild.bat first.
    exit /b 1
)

:: Start container if not already running
for /f %%i in ('docker inspect -f "{{.State.Running}}" %CONTAINER_NAME% 2^>nul') do set RUNNING=%%i
if not "%RUNNING%"=="true" (
    echo Starting container %CONTAINER_NAME%...
    docker start %CONTAINER_NAME%
    :: Brief pause to let the entrypoint finish
    timeout /t 2 /nobreak >nul
)

:: Launch raymond and beads_server inside the container
echo Launching services in %CONTAINER_NAME%...
docker exec -u devuser %CONTAINER_NAME% start-ray.sh

endlocal
