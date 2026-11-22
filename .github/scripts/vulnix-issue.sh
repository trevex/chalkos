#!/usr/bin/env bash
# Turns the nightly run's vulnix reports into the body of the issue that lists the high-score
# matches: each match of a CVE with a CVSS v3 score of 7 or more, one row per image, package and
# CVE. vulnix leaves the matches of its whitelist out of affected_by. Writes the rows that
# PREVIOUS, the body of the last such issue, does not list to NEW, and the body for every row to
# standard output. NEW stays empty when no row is new, and the issue is then left as it is.
#
#   vulnix-issue.sh REPORTS PREVIOUS NEW
#
# REPORTS holds a vulnix JSON report per image, named after the image.
set -euo pipefail

reports=$1 previous=$2 new=$3

rows=$(for report in "$reports"/*.json; do
  jq -r --arg image "$(basename "$report" .json)" '.[] | (.pname // .name) as $package
    | .cvssv3_basescore as $scores
    | .affected_by[] | select(($scores[.] // 0) >= 7)
    | "| \($image) | \($package) | \(.) | \($scores[.]) |"' "$report"
done | sort -u)

: >"$new"
while IFS= read -r row; do
  [ -n "$row" ] || continue
  # A row is known by its image, package and CVE; a changed score does not make it new.
  key=${row% | *}
  if ! grep -qF -- "$key |" "$previous"; then
    echo "$row" >>"$new"
  fi
done <<<"$rows"

cat <<EOF
vulnix matches these CVEs with a CVSS v3 score of 7 or more to the packages of the release images,
and \`.github/vulnix-whitelist.toml\` does not hold them. Update nixpkgs to fix a real match, or add
one that does not apply to the whitelist with its reason. The nightly run adds a comment for each
new match; closing the issue acknowledges the matches it lists.

| Image | Package | CVE | CVSS v3 |
| --- | --- | --- | ---: |
$rows
EOF
