#!/bin/bash
# Count lines of Go code and Markdown documentation in the project.

set -u

count_lines() {
	# $1: root directory, $2: name pattern, $3+: optional extra `find` predicates
	local root=$1
	local pattern=$2
	shift 2
	if [ ! -d "$root" ]; then
		echo 0
		return
	fi
	find "$root" -type f -name "$pattern" "$@" -exec cat {} + 2>/dev/null | wc -l
}

cmd_lines=$(count_lines cmd '*.go' ! -name '*_test.go')
internal_lines=$(count_lines internal '*.go' ! -name '*_test.go')
cmd_test_lines=$(count_lines cmd '*_test.go')
internal_test_lines=$(count_lines internal '*_test.go')
unit_test_lines=$((cmd_test_lines + internal_test_lines))
integration_test_lines=$(count_lines tests '*.go')
docs_lines=$(count_lines docs '*.md')
readme_lines=$(find . -maxdepth 1 -name '*.md' -type f -exec cat {} + 2>/dev/null | wc -l)

go_total=$((cmd_lines + internal_lines + unit_test_lines + integration_test_lines))
doc_total=$((docs_lines + readme_lines))

printf "%-28s %s\n" "Location" "Lines"
printf "%-28s %s\n" "----------------------------" "-----"
printf "%-28s %'d\n" "cmd/ (go, non-test)"          "$cmd_lines"
printf "%-28s %'d\n" "internal/ (go, non-test)"     "$internal_lines"
printf "%-28s %'d\n" "unit tests (*_test.go)"       "$unit_test_lines"
printf "%-28s %'d\n" "tests/ (integration go)"      "$integration_test_lines"
printf "%-28s %'d\n" "  Go subtotal"                "$go_total"
printf "%-28s %s\n"  "" ""
printf "%-28s %'d\n" "docs/ (md)"                   "$docs_lines"
printf "%-28s %'d\n" "root *.md (README etc.)"      "$readme_lines"
printf "%-28s %'d\n" "  Docs subtotal"              "$doc_total"
