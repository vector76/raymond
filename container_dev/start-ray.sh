#!/bin/bash
set -e

WORK="$HOME/${WORK_FOLDER:-work}"
RAYMOND_PORT="${RAYMOND_PORT:-7100}"
BEADS_PORT="${BEADS_PORT:-7101}"
NUM_WORKERS="${NUM_WORKERS:-1}"
WORKFLOWS_DIR="/opt/raymond/workflows"

# `--foreground` (passed by the Dockerfile CMD) means we are the container's
# foreground process: install a SIGTERM trap that runs stop-ray.sh and block
# on the backgrounded children. Without the flag (e.g. `docker exec
# start-ray.sh` from serve.bat) the script remains fire-and-forget as before.
FOREGROUND=0
if [ "${1:-}" = "--foreground" ]; then
    FOREGROUND=1
    shift
fi

# Resolve sibling stop-ray.sh — installed in /usr/local/bin by the Dockerfile.
# `$0` is the resolved script path (binfmt_script rewrites it to the absolute
# path even when invoked via PATH lookup), so dirname is reliable here.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
STOP_RAY="$SCRIPT_DIR/stop-ray.sh"

# Trap is installed unconditionally — harmless if SIGTERM never arrives. With
# `--foreground` it fires under `docker stop`, POSTs /shutdown, and the
# coordinator drives the tier sequence to completion before this shell exits.
trap '"$STOP_RAY" || true' TERM

# Replacement for `exit 1` on prerequisite failures: in `--foreground` mode
# (Dockerfile CMD), terminating the script also terminates the container,
# breaking `cbash.bat`-based recovery. Hand off to `sleep infinity` instead
# so the user can shell in to diagnose. Nothing was gracefully started on
# these paths, so losing the trap via `exec` is OK.
fail() {
    echo "$1" >&2
    if [ "$FOREGROUND" = "1" ]; then
        echo "[start-ray] container staying alive — fix via cbash.bat, then docker restart." >&2
        exec sleep infinity
    fi
    exit 1
}

# ---------------------------------------------------------------------------
# beads_server
# ---------------------------------------------------------------------------
if [ -z "$BS_TOKEN" ]; then
    fail "ERROR: BS_TOKEN is not set. Add it to secrets.bat and rebuild."
fi

export BS_URL="http://localhost:$BEADS_PORT"

if pgrep -f "bs serve" > /dev/null 2>&1; then
    echo "beads_server already running (port $BEADS_PORT)."
else
    echo "Starting beads_server on port $BEADS_PORT ..."
    mkdir -p "$WORK/beads"
    bs serve \
        --data-file "$WORK/beads/beads.json" \
        --port "$BEADS_PORT" \
        --token "$BS_TOKEN" \
        >> "$WORK/beads/server.log" 2>&1 &
    sleep 2
    if pgrep -f "bs serve" > /dev/null 2>&1; then
        echo "beads_server started."
    else
        fail "ERROR: beads_server failed to start. Check: $WORK/beads/server.log"
    fi
fi

# ---------------------------------------------------------------------------
# raymond
# ---------------------------------------------------------------------------
if curl -fs "http://localhost:$RAYMOND_PORT/workflows" > /dev/null 2>&1; then
    echo "raymond already running (port $RAYMOND_PORT)."
else
    if ! command -v ray > /dev/null 2>&1; then
        fail "ERROR: ray not found on PATH. Run build-ray.sh first."
    fi

    echo "Starting raymond on port $RAYMOND_PORT ..."
    mkdir -p "$WORK/raymond/state"
    ray serve --root "$WORKFLOWS_DIR" --port "$RAYMOND_PORT" \
        >> "$WORK/raymond/state/ray-serve.log" 2>&1 &

    echo -n "Waiting for raymond ..."
    TRIES=0
    until curl -fs "http://localhost:$RAYMOND_PORT/workflows" > /dev/null 2>&1; do
        sleep 1
        TRIES=$((TRIES + 1))
        if [ $TRIES -ge 30 ]; then
            echo " timed out."
            fail "Check: $WORK/raymond/state/ray-serve.log"
        fi
        echo -n "."
    done
    echo " ready."

    # ----------------------------------------------------------------------
    # TODO (Stage 11): remove this autostart loop.
    #
    # The v1 design (workflows/approach.md "Manual launch and self-bootstrapping")
    # launches workers manually from the raymond UI — one work.yaml run per
    # worker, with the worker's name as input. Workers self-bootstrap their own
    # folder, .env, and clone in their START state. No autostart, no curl POST.
    #
    # The loop below predates that decision. As long as work.yaml does not yet
    # exist in $WORKFLOWS_DIR, this code is harmless (hits the else branch).
    # Once Stage 6 produces work.yaml, this loop will conflict with manual
    # launch — strip it then (Stage 11 in PLAN.md).
    # ----------------------------------------------------------------------
    if [ -f "$WORKFLOWS_DIR/work.yaml" ]; then
        echo "Checking for active worker runs ..."
        EXISTING=$(curl -s "http://localhost:$RAYMOND_PORT/runs" 2>/dev/null || echo "[]")
        for i in $(seq 1 "$NUM_WORKERS"); do
            WORKER="worker-$i"
            if echo "$EXISTING" | grep -q "\"$WORKER\""; then
                echo "  $WORKER: already active (recovered)"
            else
                echo "  $WORKER: launching ..."
                curl -s -X POST "http://localhost:$RAYMOND_PORT/runs" \
                     -H "Content-Type: application/json" \
                     -d "{\"workflow_id\":\"work.yaml\",\"input\":\"$WORKER\"}" > /dev/null \
                     && echo "  $WORKER: launched." \
                     || echo "  $WORKER: launch failed (check raymond logs)"
            fi
        done
    else
        echo "No work.yaml in $WORKFLOWS_DIR — skipping worker launch."
    fi
fi

echo ""
echo "  raymond UI:  http://localhost:$RAYMOND_PORT"
echo "  beads UI:    http://localhost:$BEADS_PORT"

# In `--foreground` mode (container CMD), block so the SIGTERM trap can fire
# on `docker stop`. Bash's `wait` returns once a trap on the interrupting
# signal completes (see bash(1) — "wait builtin will return immediately with
# an exit status greater than 128"), so the script exits promptly after
# stop-ray.sh has driven `ray serve` through the graceful tier sequence;
# `beads_server` is reaped by the kernel when the container terminates.
if [ "$FOREGROUND" = "1" ]; then
    wait
fi
