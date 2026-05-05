# Docker Workflow Quick Reference

This reference outlines the Docker workflow for a Linux dev environment container on Windows. The workflow uses batch scripts to automate container management, emphasizes declarative updates via Dockerfile, versioned images, persistent containers for day-to-day use, and periodic rebuilds for dependency freshness or major changes. The workflow separates container starting from shell attachment to prevent accidental shutdowns when closing shells—all interactions use `exec` for shells, and the container runs a keep-alive process (e.g., `tail -f /dev/null`) to stay active independently.

## Key Principles
- **Images**: Immutable, single image per configuration. Rebuild replaces the previous image.
- **Containers**: Persistent (no `--rm`); use `stop/start` for sessions. Create new ones only after image updates. Container runs detached with a keep-alive command to avoid shutdown on shell exits.
- **Persistence**: Work files are stored in a subdirectory (configurable via `WORK_FOLDER` in `.env.container`) that is mounted into the container, keeping Docker setup files separate from work.
- **Security**: Secrets (API keys, tokens) are loaded from `secrets.bat` (gitignored) or host environment variables, never committed to git. Container configuration (names, paths) is in `.env.container` (gitignored).
- **User Management**: Fixed UID (2000) ensures consistent file ownership across rebuilds, preventing git "dubious ownership" warnings while preserving security checks for legitimate cross-platform issues.
- **Shells**: Always attach via `exec -it -u devuser`; no "original" shell that can kill the container.
- **Commands**: Run from host terminal (PowerShell/Command Prompt) using batch scripts.

## 1. Initial Setup: Configuration

Create `.env.container` from `.env.container.example`:
```
IMAGE_NAME=my-dev-env
CONTAINER_NAME=my-dev-container
WORK_FOLDER=work
# Optional: Port to expose (e.g., 8080:8080 for web servers)
# EXPOSE_PORT=8080:8080
# Optional: Host folder to persist devuser home directory between rebuilds
# HOME_FOLDER=home
# Optional: Persist Claude Code auth and settings across rebuilds
# CLAUDE_PERSIST_FOLDER=claude_persist
```

**Note**: You can set `WORK_FOLDER=.` to use the current directory as the work folder. This is useful when you want the Docker setup files in a subdirectory.

**Note**: Setting `HOME_FOLDER` allows the devuser's home directory (including shell history, installed user packages, and tool configurations) to persist across container rebuilds.

Set secrets using `secrets.bat` file (recommended):
1. Copy `secrets.bat.example` to `secrets.bat`
2. Fill in your actual secret values in `secrets.bat`
3. Place `secrets.bat` in either:
   - The current folder (same directory as `rebuild.bat`)
   - The parent folder (useful when `WORK_FOLDER=.` to keep secrets outside the mounted volume)
4. The `rebuild.bat` script will automatically search both locations and load the file when building

Alternatively, set environment variables in your PowerShell session (or system-wide):
```powershell
$env:AMP_API_KEY = "sk-your-key"
$env:ANTHROPIC_API_KEY = "sk-ant-your-key"
$env:CURSOR_API_KEY = "your-key"
$env:GITHUB_TOKEN = "ghp-your-token"
$env:CLAUDE_CODE_OAUTH_TOKEN = "your-token"
$env:GIT_USER_NAME = "Your Name"
$env:GIT_USER_EMAIL = "your.email@example.com"
# Required if using GITHUB_TOKEN:
$env:GITHUB_USERNAME = "your-github-username"
$env:TZ = "America/New_York"  # Override auto-detected timezone
```

**Note**: The `secrets.bat` file is gitignored and will never be committed. It's the recommended way to manage secrets as it's less error-prone than setting environment variables manually each time.

## 2. Initial Setup: Build and Run

- **Build and Create Container**:
```
rebuild.bat
```
  - Removes old image if it exists
  - Builds a new image (no version tags)
  - Creates (but doesn't start) the container
  - Automatically detects Windows timezone and passes it to container
  - Passes environment variables (API keys, git config, etc.) to container
  - Creates work folder subdirectory if it doesn't exist
  - Mounts work folder to `/home/devuser/work` in container

## 3. Day-to-Day Use: Opening Shells

- **Open Shell**:
```
cbash.bat
```
  - Starts container if not running
  - Opens interactive bash shell as `devuser` (UID 2000)
  - Automatically stops container when last shell exits
  - Multiple shells can be open simultaneously (container stays running)

## 4. Container Features

### Installed Tools

**Always installed:**
- **Git**: Configured from environment variables
- **Python 3**: With pip
- **Go**: golang-go (via PPA for latest version)
- **Node.js**: v20.x LTS with npm
- **Build tools**: build-essential, curl, vim, tmux
- **Document tools**: pandoc, texlive-latex-recommended, texlive-fonts-recommended

**Conditionally installed** (only if corresponding API key is set during build):
- **Amp CLI**: AI coding agent - installed if `AMP_API_KEY` is set
- **Claude Code**: AI coding agent - installed if `CLAUDE_CODE_OAUTH_TOKEN` is set
- **Cursor agent**: AI coding agent - installed if `CURSOR_API_KEY` is set

### Automatic Configuration
- **Git user**: Configured from `GIT_USER_NAME` and `GIT_USER_EMAIL` environment variables
- **GitHub authentication**: Uses `GITHUB_TOKEN` and `GITHUB_USERNAME` for authenticated git operations
- **Timezone**: Auto-detected from Windows, can be overridden with `TZ` environment variable

### Ownership Management
- **Fixed UID**: User `devuser` always has UID 2000 for consistent ownership
- **Windows mount fix**: Entrypoint automatically fixes work directory ownership when owned by root (Windows mount artifact)
- **Git security**: Preserves git "dubious ownership" warnings for legitimate cross-platform issues while preventing false positives

### Claude Code Persistence
Set `CLAUDE_PERSIST_FOLDER` in `.env.container` to preserve Claude Code login and settings across rebuilds. This mounts a lightweight host folder for just `~/.claude/` (settings, history) and `~/.claude.json` (auth token) — much faster than persisting the entire home directory.

**Migrating from an existing container** (one-time):
1. In your **current** container, copy Claude data to the work folder:
   ```bash
   mkdir ~/work/.claude_migrate
   cp -a ~/.claude ~/work/.claude_migrate/
   cp ~/.claude.json ~/work/.claude_migrate/
   ```
2. Add to `.env.container`:
   ```
   CLAUDE_PERSIST_FOLDER=claude_persist
   ```
3. Run `rebuild.bat` (creates the empty persist folder automatically).
4. Open a shell with `cbash.bat` and copy the data into the mounted paths:
   ```bash
   cp -a ~/work/.claude_migrate/.claude/* ~/.claude/
   cp -a ~/work/.claude_migrate/.claude/.[!.]* ~/.claude/ 2>/dev/null
   cp ~/work/.claude_migrate/.claude.json ~/.claude.json
   rm -rf ~/work/.claude_migrate
   ```

## 5. Updating the Image (for Dependencies or Changes)

- **When to Update**: Periodically (e.g., to refresh pip packages) or for major additions (e.g., new tools in Dockerfile).
- **Steps**:
  1. Edit `Dockerfile` if adding/changing installs.
  2. Rebuild:
```
rebuild.bat
```
  - Automatically stops and removes old container
  - Removes old image
  - Builds new image
  - Creates new container from updated image
  - Work folder is preserved (not removed)

- **Optional Cleanup** (if needed):
```
docker system prune  # Prune dangling items (confirm prompt)
```

## 6. Troubleshooting Tips

- List containers: `docker ps -a`
- List images: `docker images`
- Logs: `docker logs my-dev-container`
- Check container status: `docker inspect my-dev-container`
- If runtime changes need saving: Update Dockerfile instead of committing (avoid `docker commit`).
- Git "dubious ownership" warning: Should not occur with fixed UID. If it does, check ownership with `ls -ld /home/devuser/work` and `id`.
- Timezone issues: Check `TZ` environment variable or verify auto-detection in `rebuild.bat`.