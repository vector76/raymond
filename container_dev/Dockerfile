# Use a recent Ubuntu base for Linux dev
FROM ubuntu:24.04

# Install base system tools and dev essentials (customize as needed)
# as of 2026-01-03, add-apt-repository needed for golang-go to have a newer version that 1.22.2
#   (and software-properties-common is needed for add-apt-repository)
# as of 2026-01-03, this installs version 1.25.5 of golang-go
RUN apt-get update && \
    apt-get install -y software-properties-common ca-certificates gnupg && \
    add-apt-repository ppa:longsleep/golang-backports && \
    apt-get update && \
    apt-get install -y \
    build-essential \
    git \
    python3 \
    python3-pip \
    curl \
    vim \
    tzdata \
    gosu \
    tmux \
    golang-go \
    jq \
	pandoc texlive-latex-recommended texlive-fonts-recommended \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 20.x LTS from NodeSource (required for Convex)
# As of 2026-01-04, Node.js is v20.19.6 and npm is 10.8.2
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Enable pnpm via corepack (ships with Node.js; version matches CI)
RUN corepack enable && corepack prepare pnpm@9 --activate

# Install GitHub CLI (gh)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && \
    apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# Install Tauri Linux system dependencies and OCCT for local Rust/C++ builds
# Mirrors the apt-get block in .github/workflows/ci.yml so local builds match CI
RUN apt-get update && \
    apt-get install -y \
        pkg-config \
        cmake \
        libssl-dev \
        libgtk-3-dev \
        libwebkit2gtk-4.1-dev \
        libayatana-appindicator3-dev \
        librsvg2-dev \
        llvm \
        clang \
        libclang-dev \
        libocct-foundation-dev \
        libocct-modeling-data-dev \
        libocct-modeling-algorithms-dev \
        libocct-data-exchange-dev \
        libocct-ocaf-dev \
    && rm -rf /var/lib/apt/lists/*

# Environment variables required by the Rust build (bindgen + OCCT linking)
ENV OCCT_INCLUDE_DIR=/usr/include/opencascade \
    OCCT_LIB_DIR=/usr/lib/x86_64-linux-gnu
# Detect LLVM lib dir dynamically (Ubuntu 24.04 ships LLVM 18+)
RUN echo "LIBCLANG_PATH=$(llvm-config --libdir)" >> /etc/environment && \
    echo "export LIBCLANG_PATH=$(llvm-config --libdir)" >> /etc/bash.bashrc

# Create a non-root user for security with fixed UID/GID
# Using UID 2000 to avoid conflicts (1000 is taken by ubuntu user in base image)
RUN groupadd -g 2000 devuser 2>/dev/null || true && \
    useradd -m -s /bin/bash -u 2000 -g 2000 devuser
ARG WORK_FOLDER=work
WORKDIR /home/devuser/$WORK_FOLDER
USER devuser

# Install Amp CLI (AI coding agent) only if AMP_API_KEY is provided
# This runs the install script non-interactively; first run may prompt for login if no key is set
ARG INSTALL_AMP=false
RUN if [ "$INSTALL_AMP" = "true" ]; then curl -fsSL https://ampcode.com/install.sh | bash; fi

# Install Claude Code only if CLAUDE_CODE_OAUTH_TOKEN is provided
ARG INSTALL_CLAUDE=false
RUN if [ "$INSTALL_CLAUDE" = "true" ]; then curl -fsSL https://claude.ai/install.sh | bash; fi

# Install Cursor agent only if CURSOR_API_KEY is provided
ARG INSTALL_CURSOR=false
RUN if [ "$INSTALL_CURSOR" = "true" ]; then curl https://cursor.com/install -fsS | bash; fi

# Switch to root temporarily to allow global install
USER root
ARG INSTALL_COPILOT=false
RUN if [ "$INSTALL_COPILOT" = "true" ]; then npm install -g @github/copilot; fi
USER devuser

# Install Rust via rustup (stable toolchain)
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable
RUN echo 'export PATH="$HOME/.cargo/bin:$PATH"' >> ~/.bashrc

# Add path for various tools (including bd and others) in bashrc
RUN echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc

RUN echo 'export GOPATH="$HOME/go"' >> ~/.bashrc
RUN echo 'export PATH="$PATH:$GOPATH/bin"' >> ~/.bashrc

# install bd (beads):
#RUN go install github.com/steveyegge/beads/cmd/bd@latest

# install gt (gas town):
#RUN go install github.com/steveyegge/gastown/cmd/gt@latest

# install bv (beads viewer):
#RUN go install github.com/Dicklesworthstone/beads_viewer/cmd/bv@latest

# Set timezone from TZ environment variable if provided
RUN echo 'if [ -n "$TZ" ]; then export TZ; fi' >> ~/.bashrc

# Configure git from environment variables if set
RUN echo 'if [ -n "$GIT_USER_NAME" ]; then git config --global user.name "$GIT_USER_NAME"; fi' >> ~/.bashrc
RUN echo 'if [ -n "$GIT_USER_EMAIL" ]; then git config --global user.email "$GIT_USER_EMAIL"; fi' >> ~/.bashrc

# Set up GitHub token authentication if GITHUB_TOKEN is available
# Uses "git" as username (standard for GitHub PATs) or GITHUB_USERNAME if set
RUN echo 'if [ -n "$GITHUB_TOKEN" ]; then' >> ~/.bashrc && \
    echo '  if [ -z "$GITHUB_USERNAME" ]; then' >> ~/.bashrc && \
    echo '    echo "GITHUB_USERNAME is required for GitHub authentication" >&2' >> ~/.bashrc && \
    echo '  else' >> ~/.bashrc && \
    echo '    git config --global credential.helper store' >> ~/.bashrc && \
    echo '    echo "https://${GITHUB_USERNAME}:${GITHUB_TOKEN}@github.com" > ~/.git-credentials' >> ~/.bashrc && \
    echo '    chmod 600 ~/.git-credentials' >> ~/.bashrc && \
    echo '  fi' >> ~/.bashrc && \
    echo 'fi' >> ~/.bashrc

# Switch to root temporarily to create entrypoint that can fix ownership
USER root

# Copy default bashrc to skeleton for home folder mounting bootstrap
RUN cp /home/devuser/.bashrc /etc/skel/.bashrc

# Create entrypoint script that fixes Windows mount ownership and bootstraps bashrc
# The work directory itself is often owned by root due to Windows->Linux mount translation
RUN echo '#!/bin/bash' > /entrypoint.sh && \
    echo 'set -e' >> /entrypoint.sh && \
    echo 'WORK_DIR="/home/devuser/${WORK_FOLDER:-work}"' >> /entrypoint.sh && \
    echo '# Fix work directory ownership if owned by root (Windows mount artifact)' >> /entrypoint.sh && \
    echo '# Only fix root ownership - preserve warnings for legitimate cross-platform issues' >> /entrypoint.sh && \
    echo 'if [ -d "$WORK_DIR" ] && [ "$(stat -c %u "$WORK_DIR" 2>/dev/null)" = "0" ]; then' >> /entrypoint.sh && \
    echo '  chown 2000:2000 "$WORK_DIR" 2>/dev/null || true' >> /entrypoint.sh && \
    echo 'fi' >> /entrypoint.sh && \
    echo '# Bootstrap home folder from skeleton if mounted empty' >> /entrypoint.sh && \
    echo 'if [ ! -f /home/devuser/.bashrc ]; then' >> /entrypoint.sh && \
    echo '  cp -a /etc/skel/. /home/devuser/' >> /entrypoint.sh && \
    echo '  chown -R 2000:2000 /home/devuser' >> /entrypoint.sh && \
    echo 'fi' >> /entrypoint.sh && \
    echo '# Switch to devuser for command execution' >> /entrypoint.sh && \
    echo 'exec gosu devuser "$@"' >> /entrypoint.sh && \
    chmod +x /entrypoint.sh

# Optional: Install additional Python packages or tools here via pip
# RUN pip3 install --user requests numpy  # Example

# Use entrypoint to handle ownership fix and bashrc bootstrap, then run the keep-alive command
ENTRYPOINT ["/entrypoint.sh"]
CMD ["tail", "-f", "/dev/null"]
