#!/bin/bash
# Stop a running `ray serve` daemon by hitting POST /shutdown and streaming
# the response body (one line per shutdown phase, then a per-run summary).
# Honours RAYMOND_HOST / RAYMOND_PORT overrides; defaults match start-ray.sh.
set -eu

host="${RAYMOND_HOST:-localhost}"
port="${RAYMOND_PORT:-7100}"

curl --silent --show-error --no-buffer -X POST "http://${host}:${port}/shutdown"
