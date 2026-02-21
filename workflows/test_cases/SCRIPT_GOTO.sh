#!/bin/bash
# SCRIPT_GOTO.sh - Simple goto transition test
#
# This script demonstrates a basic script state that transitions
# to a markdown state using <goto>.

echo "Script state executing..."
echo "Environment variables:"
echo "  RAYMOND_WORKFLOW_ID=$RAYMOND_WORKFLOW_ID"
echo "  RAYMOND_AGENT_ID=$RAYMOND_AGENT_ID"

echo "<goto>SCRIPT_TARGET.md</goto>"
