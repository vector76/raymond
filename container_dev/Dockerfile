# Raymond dev container
# Build context: container_dev/ (the directory containing this file)
# Run from container_dev/: docker build -t IMAGE_NAME .
#
# This container both runs raymond (as the orchestrator) and develops it (as the subject).
# For other projects, this same pattern applies with container_dev/ as the self-contained unit.
FROM ubuntu:26.04

# Base tools — golang-go from Ubuntu 26.04 default repos (ships with Go 1.26)
RUN apt-get update && \
    apt-get install -y \
        build-essential \
        git \
        curl \
        vim \
        tzdata \
        gosu \
        jq \
        golang-go \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI (gh) — for authenticated API calls and PR operations
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && \
    apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# Fixed UID 2000 for devuser — prevents git "dubious ownership" across rebuilds
RUN groupadd -g 2000 devuser 2>/dev/null || true && \
    useradd -m -s /bin/bash -u 2000 -g 2000 devuser

# Set Go env globally so docker-exec'd scripts find Go binaries without sourcing .bashrc
ENV GOPATH=/home/devuser/go
ENV PATH=/home/devuser/go/bin:/home/devuser/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

ARG WORK_FOLDER=work
WORKDIR /home/devuser/${WORK_FOLDER}
USER devuser

# Minimal .bashrc additions (interactive sessions only)
RUN echo 'export GOPATH="$HOME/go"' >> ~/.bashrc && \
    echo 'export PATH="$GOPATH/bin:$HOME/.local/bin:$PATH"' >> ~/.bashrc

# Set credential.helper so git uses the .git-credentials file written by entrypoint.sh
RUN git config --global credential.helper store

# Install beads_server CLI (bs) — built from source at image build time
RUN cd /tmp && \
    git clone https://github.com/vector76/beads_server.git && \
    cd beads_server && \
    go install ./cmd/bs && \
    cd /tmp && rm -rf beads_server

# Bootstrap ray binary (latest published release)
# Overridden by build-ray.sh once the source worktree is available.
# The || true allows the image to build even if no release tag exists yet.
RUN go install github.com/vector76/raymond/cmd/ray@latest 2>/dev/null || \
    echo "Bootstrap ray install skipped — run build-ray.sh after setup.sh"

# Conditionally install Claude Code (AI coding agent)
ARG INSTALL_CLAUDE=false
RUN if [ "$INSTALL_CLAUDE" = "true" ]; then curl -fsSL https://claude.ai/install.sh | bash; fi

# Conditionally install Amp CLI
ARG INSTALL_AMP=false
RUN if [ "$INSTALL_AMP" = "true" ]; then curl -fsSL https://ampcode.com/install.sh | bash; fi

USER root

# Persist gitconfig to skeleton so it survives HOME_FOLDER volume mounts
RUN cp /home/devuser/.gitconfig /etc/skel/.gitconfig 2>/dev/null || true
RUN cp /home/devuser/.bashrc    /etc/skel/.bashrc

# Container-side scripts — paths relative to container_dev/ build context
COPY entrypoint.sh  /entrypoint.sh
COPY setup.sh       /usr/local/bin/setup.sh
COPY build-ray.sh   /usr/local/bin/build-ray.sh
COPY start-ray.sh   /usr/local/bin/start-ray.sh
RUN chmod +x /entrypoint.sh \
             /usr/local/bin/setup.sh \
             /usr/local/bin/build-ray.sh \
             /usr/local/bin/start-ray.sh

# Development workflows — live in container_dev/workflows/, baked into image so
# an image rebuild refreshes them without touching the persistent volume.
# This directory is empty until Stage 6; the COPY is added then.
RUN mkdir -p /opt/raymond/workflows && chown -R 2000:2000 /opt/raymond

ENTRYPOINT ["/entrypoint.sh"]
CMD ["tail", "-f", "/dev/null"]
