#!/bin/bash
# SCRIPT_RESET.sh - Reset transition test
#
# This script demonstrates the reset transition by maintaining
# a counter in a file. It runs 3 iterations (resetting twice),
# then finishes with a result on the third iteration.
# Reset clears the agent's conversation context but keeps workflow state.

counter_file="${RAYMOND_STATE_DIR:-/tmp}/reset_counter.txt"

# Initialize or read counter
if [ -f "$counter_file" ]; then
    count=$(cat "$counter_file")
else
    count=0
fi

# Increment counter
count=$((count + 1))
echo $count > "$counter_file"

echo "Reset iteration: $count of 3"

if [ $count -lt 3 ]; then
    echo "Resetting to run again..."
    echo "<reset>SCRIPT_RESET.sh</reset>"
else
    echo "Counter reached limit. Cleaning up and finishing."
    rm -f "$counter_file"
    echo "<result>Completed after 3 iterations (2 resets)</result>"
fi
