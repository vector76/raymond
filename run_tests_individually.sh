#!/bin/bash
# Run each test individually, logging output per-test.
# This isolates failures so we can identify which test kills the system.

LOGDIR="/home/devuser/work/raymond-edit/test-logs"
SUMMARY="$LOGDIR/summary.txt"
PROJ="/home/devuser/work/raymond-edit"

rm -rf "$LOGDIR"
mkdir -p "$LOGDIR"

# Collect all test node IDs
TESTS=$(python3 -m pytest --collect-only -q "$PROJ/tests/" 2>/dev/null | grep '::')

TOTAL=$(echo "$TESTS" | wc -l)
START_FROM=${1:-1}
PASSED=0
FAILED=0
SKIPPED=0
ERRORS=0
COUNT=0

echo "Running $TOTAL tests individually..."
echo "Logs in: $LOGDIR"
echo "======================================" > "$SUMMARY"
echo "Test run started: $(date)" >> "$SUMMARY"
echo "Total tests collected: $TOTAL" >> "$SUMMARY"
echo "======================================" >> "$SUMMARY"

while IFS= read -r test_id; do
    COUNT=$((COUNT + 1))
    if [ $COUNT -lt $START_FROM ]; then
        continue
    fi
    # Create a safe filename from the test ID
    safe_name=$(echo "$test_id" | sed 's|/|_|g; s|::|__|g')
    logfile="$LOGDIR/${safe_name}.log"

    echo "[$COUNT/$TOTAL] $test_id"

    # Run single test with a 30-second timeout to prevent hangs
    timeout 30 python3 -m pytest -x --tb=short --no-header -q "$test_id" > "$logfile" 2>&1
    rc=$?

    if [ $rc -eq 0 ]; then
        status="PASS"
        PASSED=$((PASSED + 1))
    elif [ $rc -eq 5 ]; then
        # pytest exit code 5 = no tests collected (e.g. skipped)
        status="SKIP"
        SKIPPED=$((SKIPPED + 1))
    elif [ $rc -eq 124 ]; then
        status="TIMEOUT"
        ERRORS=$((ERRORS + 1))
    else
        status="FAIL"
        FAILED=$((FAILED + 1))
    fi

    echo "  -> $status"
    echo "$status  $test_id" >> "$SUMMARY"

done <<< "$TESTS"

echo "======================================" >> "$SUMMARY"
echo "Finished: $(date)" >> "$SUMMARY"
echo "Total: $TOTAL  Passed: $PASSED  Failed: $FAILED  Skipped: $SKIPPED  Errors/Timeout: $ERRORS" >> "$SUMMARY"

echo ""
echo "======================================"
echo "Total: $TOTAL  Passed: $PASSED  Failed: $FAILED  Skipped: $SKIPPED  Errors/Timeout: $ERRORS"
echo "Summary: $SUMMARY"
echo "======================================"
