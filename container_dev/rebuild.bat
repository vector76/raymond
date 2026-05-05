@echo off
setlocal enabledelayedexpansion

:: Load container config from .env.container
if not exist .env.container (
    echo .env.container file not found. Copy .env.container.example to .env.container and customize.
    exit /b 1
)
for /f "tokens=1,2 delims==" %%a in (.env.container) do (
    if "%%a"=="IMAGE_NAME"          set IMAGE_NAME=%%b
    if "%%a"=="CONTAINER_NAME"      set CONTAINER_NAME=%%b
    if "%%a"=="WORK_FOLDER"         set WORK_FOLDER=%%b
    if "%%a"=="RAYMOND_PORT"        set RAYMOND_PORT=%%b
    if "%%a"=="BEADS_PORT"          set BEADS_PORT=%%b
    if "%%a"=="ORIGIN_URL"          set ORIGIN_URL=%%b
    if "%%a"=="HOME_FOLDER"         set HOME_FOLDER=%%b
    if "%%a"=="CLAUDE_PERSIST_FOLDER" set CLAUDE_PERSIST_FOLDER=%%b
)
if not defined IMAGE_NAME (
    echo IMAGE_NAME not set in .env.container.
    exit /b 1
)
if not defined CONTAINER_NAME (
    echo CONTAINER_NAME not set in .env.container.
    exit /b 1
)
if not defined WORK_FOLDER (
    set WORK_FOLDER=work
)

:: Load secrets from secrets.bat (current folder or parent folder)
set SECRETS_LOADED=0
if exist secrets.bat (
    echo Loading secrets from secrets.bat ^(current folder^)...
    call secrets.bat
    set SECRETS_LOADED=1
) else if exist ..\secrets.bat (
    echo Loading secrets from secrets.bat ^(parent folder^)...
    call ..\secrets.bat
    set SECRETS_LOADED=1
)
if "!SECRETS_LOADED!"=="0" (
    echo Note: secrets.bat not found. Set environment variables manually or copy secrets.bat.example.
)

:: Build -e flags from environment (hard-coded list for security — add new vars here)
set ENV_VARS=
if defined ANTHROPIC_API_KEY (
    set ENV_VARS=!ENV_VARS! -e "ANTHROPIC_API_KEY=!ANTHROPIC_API_KEY!"
)
if defined AMP_API_KEY (
    set ENV_VARS=!ENV_VARS! -e "AMP_API_KEY=!AMP_API_KEY!"
)
if defined GITHUB_TOKEN (
    set ENV_VARS=!ENV_VARS! -e "GITHUB_TOKEN=!GITHUB_TOKEN!"
)
if defined CLAUDE_CODE_OAUTH_TOKEN (
    set ENV_VARS=!ENV_VARS! -e "CLAUDE_CODE_OAUTH_TOKEN=!CLAUDE_CODE_OAUTH_TOKEN!"
)
if defined GIT_USER_NAME (
    set ENV_VARS=!ENV_VARS! -e "GIT_USER_NAME=!GIT_USER_NAME!"
)
if defined GIT_USER_EMAIL (
    set ENV_VARS=!ENV_VARS! -e "GIT_USER_EMAIL=!GIT_USER_EMAIL!"
)
if defined GITHUB_USERNAME (
    set ENV_VARS=!ENV_VARS! -e "GITHUB_USERNAME=!GITHUB_USERNAME!"
)
if defined ORIGIN_URL (
    set ENV_VARS=!ENV_VARS! -e "ORIGIN_URL=!ORIGIN_URL!"
)
if defined BS_TOKEN (
    set ENV_VARS=!ENV_VARS! -e "BS_TOKEN=!BS_TOKEN!"
)
if defined RAYMOND_PORT (
    set ENV_VARS=!ENV_VARS! -e "RAYMOND_PORT=!RAYMOND_PORT!"
)
if defined BEADS_PORT (
    set ENV_VARS=!ENV_VARS! -e "BEADS_PORT=!BEADS_PORT!"
)

:: Auto-detect Windows timezone and convert to IANA format
if not defined TZ (
    for /f "delims=" %%t in ('powershell -NoProfile -Command "$tz = [TimeZoneInfo]::Local; $tzId = $tz.Id; $mapping = @{'Eastern Standard Time'='America/New_York'; 'Central Standard Time'='America/Chicago'; 'Mountain Standard Time'='America/Denver'; 'Pacific Standard Time'='America/Los_Angeles'; 'Alaska Standard Time'='America/Anchorage'; 'Hawaiian Standard Time'='Pacific/Honolulu'; 'Atlantic Standard Time'='America/Halifax'; 'Central European Standard Time'='Europe/Berlin'; 'GMT Standard Time'='Europe/London'; 'W. Europe Standard Time'='Europe/Amsterdam'; 'Tokyo Standard Time'='Asia/Tokyo'; 'China Standard Time'='Asia/Shanghai'; 'India Standard Time'='Asia/Kolkata'; 'AUS Eastern Standard Time'='Australia/Sydney'; 'New Zealand Standard Time'='Pacific/Auckland'}; if ($mapping.ContainsKey($tzId)) { $mapping[$tzId] } else { $tzId }"') do set TZ=%%t
)
if defined TZ (
    set ENV_VARS=!ENV_VARS! -e "TZ=!TZ!"
)
set ENV_VARS=!ENV_VARS! -e "WORK_FOLDER=!WORK_FOLDER!"

:: Create work folder if it doesn't exist
if not exist "!WORK_FOLDER!" (
    echo Creating work folder: !WORK_FOLDER!
    mkdir "!WORK_FOLDER!"
)

:: Volume mount (host path relative to container_dev/)
set HOST_DIR=%CD%
set WORK_PATH=!HOST_DIR!\!WORK_FOLDER!
set VOLUME_MOUNT=!WORK_PATH:\=/!:/home/devuser/!WORK_FOLDER!

set WORKDIR=/home/devuser/!WORK_FOLDER!

:: Stop and remove existing container
echo Stopping and removing container %CONTAINER_NAME% if exists...
docker stop %CONTAINER_NAME% 2>nul
docker rm %CONTAINER_NAME% 2>nul

:: Remove old image
echo Removing old image %IMAGE_NAME% if exists...
docker rmi %IMAGE_NAME% 2>nul

:: Build image (context is project root ..; Dockerfile is in container_dev/)
echo Building image %IMAGE_NAME%...
set BUILD_ARGS=--build-arg WORK_FOLDER=!WORK_FOLDER!
if defined AMP_API_KEY (
    set BUILD_ARGS=!BUILD_ARGS! --build-arg INSTALL_AMP=true
)
if defined CLAUDE_CODE_OAUTH_TOKEN (
    set BUILD_ARGS=!BUILD_ARGS! --build-arg INSTALL_CLAUDE=true
) else if defined CLAUDE_PERSIST_FOLDER (
    set BUILD_ARGS=!BUILD_ARGS! --build-arg INSTALL_CLAUDE=true
)

docker build !BUILD_ARGS! -t %IMAGE_NAME% .
if errorlevel 1 (
    echo Build failed.
    pause
    exit /b 1
)

:: Port flags
set PORT_FLAGS=
if defined RAYMOND_PORT (
    set PORT_FLAGS=!PORT_FLAGS! -p !RAYMOND_PORT!:!RAYMOND_PORT!
)
if defined BEADS_PORT (
    set PORT_FLAGS=!PORT_FLAGS! -p !BEADS_PORT!:!BEADS_PORT!
)

:: Home folder volume mount (optional)
set HOME_MOUNT=
if defined HOME_FOLDER (
    if not exist "!HOME_FOLDER!" (
        echo Creating home folder: !HOME_FOLDER!
        mkdir "!HOME_FOLDER!"
    )
    set HOME_PATH=!HOST_DIR!\!HOME_FOLDER!
    set HOME_MOUNT=-v "!HOME_PATH:\=/!:/home/devuser"
)

:: Claude persist mounts (optional)
set CLAUDE_MOUNT=
if defined CLAUDE_PERSIST_FOLDER (
    if not exist "!CLAUDE_PERSIST_FOLDER!\claude" (
        echo Creating Claude persist folder: !CLAUDE_PERSIST_FOLDER!\claude
        mkdir "!CLAUDE_PERSIST_FOLDER!\claude"
    )
    set CLAUDE_JSON_NEEDS_INIT=0
    if not exist "!CLAUDE_PERSIST_FOLDER!\claude.json" (
        set CLAUDE_JSON_NEEDS_INIT=1
    ) else (
        for %%I in ("!CLAUDE_PERSIST_FOLDER!\claude.json") do if %%~zI==0 set CLAUDE_JSON_NEEDS_INIT=1
    )
    if "!CLAUDE_JSON_NEEDS_INIT!"=="1" (
        echo Initializing claude.json in !CLAUDE_PERSIST_FOLDER!
        echo {}>"!CLAUDE_PERSIST_FOLDER!\claude.json"
    )
    set CLAUDE_PATH=!HOST_DIR!\!CLAUDE_PERSIST_FOLDER!
    set CLAUDE_MOUNT=-v "!CLAUDE_PATH:\=/!/claude:/home/devuser/.claude" -v "!CLAUDE_PATH:\=/!/claude.json:/home/devuser/.claude.json"
)

:: Create container (does not start it)
echo Creating container %CONTAINER_NAME%...
docker create --init --name %CONTAINER_NAME% %ENV_VARS% %PORT_FLAGS% %HOME_MOUNT% %CLAUDE_MOUNT% -v "%VOLUME_MOUNT%" --workdir %WORKDIR% %IMAGE_NAME%
if errorlevel 1 (
    echo Create failed.
    pause
    exit /b 1
)

echo.
echo Rebuild complete.
echo   cbash.bat   - open a shell
echo   serve.bat   - start raymond + beads_server

endlocal
