#!/bin/bash
# SCRIPT_RESULT.sh - Result with payload test
#
# This script demonstrates a script state that returns a result
# with a payload. This is useful for subroutine-style workflows
# where a script performs work and returns data to the caller.

echo "Performing some deterministic work..."

# Simulate gathering data
timestamp=$(date +%Y-%m-%d\ %H:%M:%S)
hostname_info=$(hostname)

# Create a result payload
payload="Script completed at $timestamp on $hostname_info"

echo "Work complete. Returning result."
echo "<result>$payload</result>"
