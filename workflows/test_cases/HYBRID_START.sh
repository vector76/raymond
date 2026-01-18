#!/bin/bash
# HYBRID_START.sh - First state in hybrid workflow
#
# This workflow demonstrates seamless transitions between
# script states and markdown states:
#   HYBRID_START.sh -> HYBRID_MIDDLE.md -> HYBRID_END.sh -> result

echo "=== Hybrid Workflow Started ==="
echo "This is a script state performing initial setup."
echo ""
echo "Gathering system information..."
echo "  Date: $(date)"
echo "  User: $(whoami)"
echo "  PWD:  $(pwd)"
echo ""
echo "Setup complete. Transitioning to markdown state for LLM processing."

echo "<goto>HYBRID_MIDDLE.md</goto>"
