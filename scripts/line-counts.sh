#!/bin/bash
# Count lines of code in key project directories

src_lines=$(find src -name "*.py" -type f -exec cat {} + 2>/dev/null | wc -l)
test_lines=$(find tests -name "*.py" -type f -exec cat {} + 2>/dev/null | wc -l)
wiki_lines=$(find wiki -name "*.md" -type f -exec cat {} + 2>/dev/null | wc -l)

printf "%-12s %s\n" "Location" "Lines"
printf "%-12s %s\n" "--------" "-----"
printf "%-12s %'d\n" "src/ (py)" "$src_lines"
printf "%-12s %'d\n" "tests/ (py)" "$test_lines"
printf "%-12s %'d\n" "wiki/ (md)" "$wiki_lines"
