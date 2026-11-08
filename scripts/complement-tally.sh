#!/usr/bin/env bash
# Turn a Complement results table into the tallies the documents quote, instead of retyping them.
#
# Why: three documents quoted the state of one server and disagreed with each other (130/89/1 in
# README.md, "remains 121/98/1" in BLOCKERS.md, 141/78/1 in a run summary) because each number had
# been copied by hand from a different run of a different tree. A number that is generated cannot
# drift from the table it is generated from, and the table comes out of the same run as the log.
#
# Usage:
#   scripts/complement-tally.sh                       # the baseline table
#   scripts/complement-tally.sh .agent-work/next/complement-results-<label>.tsv
#   scripts/complement-tally.sh <table.tsv> --by-package
#   scripts/complement-tally.sh <table.tsv> --grep 'Knock|Invite'
#
# The input is the qualified table scripts/complement.sh writes:  <pass|fail|skip>\t<package>\t<TestName>.
set -euo pipefail

table="${1:-$(dirname "${BASH_SOURCE[0]}")/../.agent-work/next/complement-baseline.tsv}"
shift || true
by_package=0
pattern=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--by-package) by_package=1 ;;
	--grep)
		shift
		pattern="${1:-}"
		;;
	*)
		echo "unknown argument: $1" >&2
		exit 2
		;;
	esac
	shift
done

if [[ ! -f "$table" ]]; then
	echo "no results table at $table — run scripts/complement.sh first" >&2
	exit 1
fi

echo "table: $table"
if [[ -n "$pattern" ]]; then
	echo "filter: $pattern"
fi

awk -F'\t' -v bypkg="$by_package" -v pat="$pattern" '
	pat != "" && $3 !~ pat { next }
	{
		total++
		state[$1]++
		pkg = $2; sub(/^tests\//, "", pkg)
		key = pkg "\t" $1
		pkgcount[key]++
		pkgs[pkg] = 1
	}
	END {
		printf "tally: %d passing, %d failing, %d skipped, %d top-level results\n",
			state["pass"], state["fail"], state["skip"], total
		if (bypkg) {
			for (p in pkgs) {
				printf "  %-28s %d pass, %d fail, %d skip\n", p,
					pkgcount[p "\t" "pass"], pkgcount[p "\t" "fail"], pkgcount[p "\t" "skip"]
			}
		}
	}' "$table"

if [[ -n "$pattern" ]]; then
	echo "--- matching results ---"
	awk -F'\t' -v pat="$pattern" '$3 ~ pat { printf "  %-6s %-32s %s\n", $1, $2, $3 }' "$table" | sort
fi
