#!/bin/sh
# run-suites.sh — run Complement suites side by side: one process per package.
#
# What this buys: `tests/csapi` (namespace "csapi") and the server-server package
# `tests` (namespace "fed") are two Go packages whose containers cannot collide, so
# running them together costs the slower of the two instead of their sum. What this
# deliberately is not: a sharder. Splitting ONE package across processes looks tempting
# — tests/csapi is fully serial (409 s of wall clock for 407 s of tests, measured
# 2026-11-20) and the machine has cores to spare — but it silently corrupts the result:
#
#   * `TestMain(m, "csapi")` fixes the package namespace, blueprint containers are
#     named `complement_<namespace>.<blueprint>.hsN` (internal/docker/builder.go:413),
#     so two processes of one package fight over the same names; and
#   * every process calls `builder.Cleanup()` at startup, which removes everything
#     labelled `complement_pkg=<namespace>` (internal/docker/builder.go:78-87) — i.e.
#     the other lanes' running homeservers.
#
# Six shards of tests/csapi on this machine measured **63 red instead of 22**, with a
# plausible-looking summary line. That is the whole trap: the tally is wrong, not the
# server. One process per package, no exceptions.
#
# Environment (all optional):
#   COMPLEMENT              Complement checkout                    (default $HOME/complement)
#   COMPLEMENT_BASE_IMAGE   image to deploy                        (default localhost/lattice-complement:latest)
#   COMPLEMENT_SUITES       suites to run in parallel              (default "csapi")
#                           names: csapi, fed, or any tests/<dir>  e.g. "csapi fed msc4306"
#   COMPLEMENT_RUN          -test.run regexp, all suites           (default .*)
#   COMPLEMENT_SKIP         -test.skip regexp, all suites          (default empty)
#   COMPLEMENT_SKIP_BLOCKED also skip the names in blocked-csapi.txt (default 0)
#   COMPLEMENT_TIMEOUT      per-suite test timeout                 (default 40m)
#   COMPLEMENT_OUT          where the logs and binaries go         (default a fresh temp dir)
#
# Results are read back as a serial `go test -v` prints them, so the tally means what
# `make complement` means.
set -u

COMPLEMENT="${COMPLEMENT:-$HOME/complement}"
IMAGE="${COMPLEMENT_BASE_IMAGE:-localhost/lattice-complement:latest}"
SUITES="${COMPLEMENT_SUITES:-csapi}"
RUN_RE="${COMPLEMENT_RUN:-.*}"
SKIP_RE="${COMPLEMENT_SKIP:-}"
SKIP_BLOCKED="${COMPLEMENT_SKIP_BLOCKED:-0}"
TIMEOUT="${COMPLEMENT_TIMEOUT:-40m}"
HERE=$(dirname "$0")
OUT="${COMPLEMENT_OUT:-$(mktemp -d "${TMPDIR:-/tmp}/lattice-complement-XXXXXX")}"

[ -d "$COMPLEMENT" ] || { echo "no Complement checkout at $COMPLEMENT" >&2; exit 2; }
mkdir -p "$OUT"

# The blocked list is a convenience copy of docs/TODO.md's red table; a missing file
# is not a reason to run the slow names.
if [ "$SKIP_BLOCKED" != "0" ] && [ -f "$HERE/blocked-csapi.txt" ]; then
  blocked=$(grep -E '^Test' "$HERE/blocked-csapi.txt" | paste -sd'|' -)
  SKIP_RE="${SKIP_RE:+$SKIP_RE|}$blocked"
fi

suite_pkg() {
  case "$1" in
    fed) echo "./tests" ;;
    *)   echo "./tests/$1" ;;
  esac
}

start=$(date +%s)
for suite in $SUITES; do
  pkg=$(suite_pkg "$suite")
  (
    cd "$COMPLEMENT" || exit 2
    [ -d "${pkg#./}" ] || { echo "no $pkg in $COMPLEMENT" > "$OUT/$suite.log"; exit 2; }
    # Compile once, then run the binary: the binary is what -test.list and -test.skip
    # speak, and it keeps the compile out of the measured time.
    COMPLEMENT_BASE_IMAGE="$IMAGE" go test -c -o "$OUT/$suite.test" "$pkg" >/dev/null 2>&1 || exit 3
    COMPLEMENT_BASE_IMAGE="$IMAGE" "$OUT/$suite.test" -test.run "^($RUN_RE)\$" -test.skip "^($SKIP_RE)\$" \
      -test.count=1 -test.timeout="$TIMEOUT" -test.v > "$OUT/$suite.log" 2>&1
    echo "$?" > "$OUT/$suite.exit"
  ) &
done
wait
wall=$(( $(date +%s) - start ))

for suite in $SUITES; do
  log="$OUT/$suite.log"
  if [ ! -s "$log" ]; then printf '%-8s no output (compile or launch failed)\n' "$suite"; continue; fi
  sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -E '^--- (PASS|FAIL|SKIP): Test' \
    | sed -E 's/^--- ([A-Z]+): (Test[A-Za-z0-9_]*).*/\1 \2/' | sort > "$OUT/$suite.results"
  green=$(grep -c '^PASS ' "$OUT/$suite.results")
  red=$(grep -c '^FAIL ' "$OUT/$suite.results")
  skipped=$(grep -c '^SKIP ' "$OUT/$suite.results")
  ran=$(wc -l < "$OUT/$suite.results" | tr -d ' ')
  printf '%-8s %s green, %s red, %s skipped (%s results, exit %s)\n' \
    "$suite" "$green" "$red" "$skipped" "$ran" "$(cat "$OUT/$suite.exit" 2>/dev/null || echo '?')"
  grep '^FAIL ' "$OUT/$suite.results" | awk '{ printf "         red: %s\n", $2 }'
done
echo "wall: ${wall}s"
