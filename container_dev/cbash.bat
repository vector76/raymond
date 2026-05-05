@echo off
setlocal enabledelayedexpansion

:: Load container config from .env.container
if not exist .env.container (
    echo .env.container file not found. Copy .env.container.example to .env.container and customize.
    exit /b 1
)
for /f "tokens=1,2 delims==" %%a in (.env.container) do (
    if "%%a"=="IMAGE_NAME" set IMAGE_NAME=%%b
    if "%%a"=="CONTAINER_NAME" set CONTAINER_NAME=%%b
)
if not defined IMAGE_NAME (
    echo IMAGE_NAME not set in .env.container.
    exit /b 1
)
if not defined CONTAINER_NAME (
    echo CONTAINER_NAME not set in .env.container.
    exit /b 1
)

:: Check if container exists
docker inspect %CONTAINER_NAME% >nul 2>&1
if errorlevel 1 (
    echo Container %CONTAINER_NAME% does not exist. Run rebuild.bat first.
    exit /b 1
)

:: Check if running
for /f %%i in ('docker inspect -f "{{.State.Running}}" %CONTAINER_NAME% 2^>nul') do set RUNNING=%%i
if not "%RUNNING%"=="true" (
    echo Starting container %CONTAINER_NAME%...
    docker start %CONTAINER_NAME%
)

:: Exec into interactive bash as devuser
echo Opening shell in %CONTAINER_NAME%...
docker exec -it -u devuser %CONTAINER_NAME% bash

:: After shell exit, count remaining bash processes (default to 0 if fails)
set COUNT=0
for /f %%a in ('docker top %CONTAINER_NAME% ^| findstr "bash" ^| find /c /v ""') do set COUNT=%%a

:: If no more bash processes, stop the container
if "%COUNT%"=="0" (
    echo No more active shells. Stopping container %CONTAINER_NAME%...
    docker stop %CONTAINER_NAME%
) else (
    echo %COUNT% active shell^(s^) remaining. Container stays running.
)

endlocal