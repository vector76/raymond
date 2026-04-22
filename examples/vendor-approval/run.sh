#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

case "${1:-run}" in
  run)
    exec raymond "$SCRIPT_DIR/states" \
      --on-await=pause \
      --budget "${BUDGET:-5.00}" \
      ${INPUT:+--input "$INPUT"}
    ;;
  resume)
    exec raymond \
      --resume "$RUN_ID" \
      --input "$INPUT"
    ;;
  *)
    echo "Usage: run.sh [run|resume]" >&2
    exit 1
    ;;
esac
