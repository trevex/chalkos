#!/usr/bin/env bash
# Checks that a pull request's title, which a squash merge makes the subject of its commit on
# main, is a Conventional Commit: a type, an optional scope in parentheses, an optional ! for a
# breaking change, a colon, a space and a summary.
#
#   pr-title.sh TITLE
set -euo pipefail

title=$1
types='feat|fix|docs|style|ci|build|test|refactor|perf|chore|revert'
if [[ $title =~ ^($types)(\([^()[:space:]]+\))?!?:\ [^[:space:]] ]]; then
  echo "the title is a Conventional Commit: $title"
  exit 0
fi
if [ -n "${GITHUB_ACTIONS:-}" ]; then
  echo "::error title=Not a Conventional Commit::the pull request's title becomes the subject of its commit on main"
fi
{
  echo "The title is not a Conventional Commit: $title"
  echo "Write it as <type>(<scope>)!: <summary>, where the scope and the ! are optional, such as"
  echo "  fix(chalkctl): check every node of an image's name before skipping it"
  echo "The type is one of: ${types//|/, }."
} >&2
exit 1
