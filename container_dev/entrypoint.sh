#!/bin/bash
set -e

WORK_DIR="/home/devuser/${WORK_FOLDER:-work}"

# Fix work directory ownership if root-owned (Windows→Linux mount artifact)
if [ -d "$WORK_DIR" ] && [ "$(stat -c %u "$WORK_DIR" 2>/dev/null)" = "0" ]; then
    chown 2000:2000 "$WORK_DIR" 2>/dev/null || true
fi

# Bootstrap home from skeleton if volume-mounted empty
if [ ! -f /home/devuser/.bashrc ]; then
    cp -a /etc/skel/. /home/devuser/
    chown -R 2000:2000 /home/devuser
fi

# Set timezone
if [ -n "$TZ" ]; then
    echo "$TZ" > /etc/timezone
    ln -snf "/usr/share/zoneinfo/$TZ" /etc/localtime 2>/dev/null || true
fi

# Persist git identity (written each start so env vars always win)
if [ -n "$GIT_USER_NAME" ]; then
    gosu devuser git config --global user.name "$GIT_USER_NAME"
fi
if [ -n "$GIT_USER_EMAIL" ]; then
    gosu devuser git config --global user.email "$GIT_USER_EMAIL"
fi

# Write GitHub credentials so all processes (not just interactive shells) can push
if [ -n "$GITHUB_TOKEN" ] && [ -n "$GITHUB_USERNAME" ]; then
    echo "https://${GITHUB_USERNAME}:${GITHUB_TOKEN}@github.com" > /home/devuser/.git-credentials
    chown 2000:2000 /home/devuser/.git-credentials
    chmod 600 /home/devuser/.git-credentials
else
    if [ -n "$GITHUB_TOKEN" ] || [ -n "$GITHUB_USERNAME" ]; then
        echo "Warning: both GITHUB_TOKEN and GITHUB_USERNAME are required for git authentication." >&2
    fi
fi

exec gosu devuser "$@"
