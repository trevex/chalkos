#!/usr/bin/env bash
# retried.sh NAME LOG prints, as Markdown, what the first attempt of the CI job NAME failed to
# build, whose log is LOG, once its retry passed: the checks whose builds failed and the Go tests
# that failed in them. A check that fails and then passes is flaky, unless a download failed.
set -euo pipefail

name=$1
log=$2

echo "### $name passed on its retry"
echo
echo "The first attempt failed to build:"
echo
# Nix names a failed build "builder for '<drv>' failed" or "Cannot build '<drv>'".
grep -oE "(builder for|Cannot build) '/nix/store/[^']+\.drv'" "$log" |
  sed -E "s#.*'/nix/store/[^-]+-(.+)\.drv'#- \`\1\`#" | sort -u || echo "- nothing the log names"
echo
if failed=$(grep -E -- '--- FAIL: ' "$log" | sort -u | head -n 100) && [ -n "$failed" ]; then
  echo "Its failed tests:"
  echo
  echo '```text'
  echo "$failed"
  echo '```'
else
  echo "Its log names no failed test, as when a download failed."
fi
