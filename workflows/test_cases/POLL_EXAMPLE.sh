#!/bin/bash
# POLL_EXAMPLE.sh - Polling workflow with sleep
#
# This script demonstrates a polling pattern where the script:
# 1. Checks for a condition (simulated by a counter file)
# 2. If not met, sleeps briefly and resets
# 3. If met, transitions to a processing state
#
# This is a key use case for script states - polling without
# consuming LLM tokens or risking session timeouts.

poll_counter="/tmp/poll_counter.txt"
poll_target=3  # Number of polls before "finding" work

# Initialize or read counter
if [ -f "$poll_counter" ]; then
    count=$(cat "$poll_counter")
else
    count=0
fi

count=$((count + 1))
echo $count > "$poll_counter"

echo "=== Poll Iteration $count ==="
echo "Checking for work... (simulated condition: poll $poll_target times)"

if [ $count -lt $poll_target ]; then
    echo "No work found. Sleeping for 1 second before next poll..."
    sleep 1
    echo "Resuming poll."
    echo "<reset>POLL_EXAMPLE.sh</reset>"
else
    echo "Work found! Cleaning up poll counter and processing."
    rm -f "$poll_counter"
    echo "<goto>POLL_PROCESS.md</goto>"
fi
