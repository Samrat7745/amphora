#!/usr/bin/env bash
# Fails when the total statement coverage of a Go cover profile is below a floor.
#
#   go test -race -coverprofile=cover.out -coverpkg=./... $(go list ./... | grep -v e2e)
#   scripts/check-coverage.sh cover.out [floor-percent]
#
# Binaries and test tooling (cmd/, tools/, internal/testkit, internal/e2e) are excluded from the total.
# The floor defaults to DEFAULT_FLOOR below (override with COVERAGE_FLOOR or the 2nd argument); it is the measured coverage minus 2 points. Raise it when coverage grows.
set -euo pipefail

DEFAULT_FLOOR=88.5
profile="${1:?usage: check-coverage.sh <cover.out> [floor-percent]}"
floor="${2:-${COVERAGE_FLOOR:-$DEFAULT_FLOOR}}"

filtered="$(mktemp)"
trap 'rm -f "$filtered"' EXIT
grep -Ev '/(cmd|tools)/|/internal/(testkit|e2e)/' "$profile" > "$filtered"

total="$(go tool cover -func="$filtered" | awk '/^total:/ {gsub("%","",$NF); print $NF}')"
if [ -z "$total" ]; then
  echo "could not determine total coverage from $profile" >&2
  exit 2
fi
echo "total statement coverage: ${total}% (floor ${floor}%)"
if awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t + 0 < f + 0) }'; then
  echo "coverage ${total}% is below the floor of ${floor}%" >&2
  exit 1
fi
