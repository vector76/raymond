#!/bin/bash
# Raymond-specific script: builds the ray binary from source.
# For non-raymond projects this script does not exist; ray is pre-installed in the image.
set -e

WORK="$HOME/${WORK_FOLDER:-work}"
REPO="$WORK/repo.git"
BUILD_TREE="$WORK/worktrees/build"

if [ ! -d "$REPO" ]; then
    echo "Error: $REPO not found. Run setup.sh first."
    exit 1
fi

# Add build worktree if not present (idempotent)
if [ ! -d "$BUILD_TREE" ]; then
    echo "Creating build worktree at $BUILD_TREE ..."
    cd "$REPO" && git worktree add "$BUILD_TREE" main
fi

echo "Fetching and updating build worktree ..."
cd "$BUILD_TREE"
git fetch origin
git reset --hard FETCH_HEAD

echo "Building ray ..."
go build -o "$GOPATH/bin/ray" ./cmd/ray/

echo ""
echo "Build complete: $GOPATH/bin/ray"
echo "Verify: ray --help"
