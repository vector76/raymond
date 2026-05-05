#!/bin/bash
set -e

WORK="$HOME/${WORK_FOLDER:-work}"

if [ -z "$ORIGIN_URL" ]; then
    echo "Error: ORIGIN_URL is not set."
    echo "Add ORIGIN_URL=https://github.com/vector76/raymond.git to .env.container"
    exit 1
fi

# Bare clone — workers and build worktrees branch from this
if [ ! -d "$WORK/repo.git" ]; then
    echo "Cloning $ORIGIN_URL → $WORK/repo.git ..."
    git clone --bare "$ORIGIN_URL" "$WORK/repo.git"
    # Ensure the remote is configured (needed for git fetch inside worktrees)
    git --git-dir="$WORK/repo.git" remote set-url origin "$ORIGIN_URL"
    echo "Clone complete."
else
    echo "$WORK/repo.git already exists — skipping clone."
    echo "To update: git --git-dir=$WORK/repo.git fetch origin"
fi

# Persistent directory layout (created once; survives container rebuilds via volume)
mkdir -p "$WORK/worktrees"
mkdir -p "$WORK/raymond/state"
mkdir -p "$WORK/beads"

echo ""
echo "Layout:"
echo "  $WORK/repo.git       bare clone of origin"
echo "  $WORK/worktrees/     worker git worktrees (created on demand)"
echo "  $WORK/raymond/state/ raymond daemon state"
echo "  $WORK/beads/         beads_server data (beads.json written here on first run)"
echo ""
echo "Next steps (run inside the container):"
echo "  build-ray.sh   — compile ray from source"
echo "  start-ray.sh   — start beads_server + raymond"
