#!/bin/bash
set -e

WORK="$HOME/${WORK_FOLDER:-work}"
RAYMOND_PORT="${RAYMOND_PORT:-7100}"
BEADS_PORT="${BEADS_PORT:-7101}"
NUM_WORKERS="${NUM_WORKERS:-1}"
WORKFLOWS_DIR="/opt/raymond/workflows"

# ---------------------------------------------------------------------------
# beads_server
# ---------------------------------------------------------------------------
if [ -z "$BS_TOKEN" ]; then
    echo "ERROR: BS_TOKEN is not set. Add it to secrets.bat and rebuild."
    exit 1
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
        echo "ERROR: beads_server failed to start."
        echo "Check: $WORK/beads/server.log"
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# raymond
# ---------------------------------------------------------------------------
if curl -fs "http://localhost:$RAYMOND_PORT/workflows" > /dev/null 2>&1; then
    echo "raymond already running (port $RAYMOND_PORT)."
else
    if ! command -v ray > /dev/null 2>&1; then
        echo "ERROR: ray not found on PATH. Run build-ray.sh first."
        exit 1
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
            echo "Check: $WORK/raymond/state/ray-serve.log"
            exit 1
        fi
        echo -n "."
    done
    echo " ready."

    # Launch workers — skip any already alive via run recovery
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
