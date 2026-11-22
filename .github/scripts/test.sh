#!/usr/bin/env bash
# Tests dco.sh, pr-title.sh, vulnix-issue.sh and retried.sh: dco.sh on the commits of a repository
# made for the test, pr-title.sh on titles that pass and titles that fail, vulnix-issue.sh on
# reports made for the test, and retried.sh on build logs made for the test. The workflows check runs it.
set -euo pipefail

scripts=$(cd "$(dirname "$0")" && pwd)
failures=0

# expect pass|fail DESCRIPTION COMMAND...: runs the command and counts a failure when its outcome
# is not the one expected.
expect() {
  local want=$1 what=$2 got
  shift 2
  if "$@" >/dev/null 2>&1; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "ok: $what"
  else
    echo "FAIL: $what: wanted $want, got $got" >&2
    failures=$((failures + 1))
  fi
}

repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q -b main
git config user.name "Ada Lovelace"
git config user.email "ada@example.com"
git config commit.gpgsign false
git commit -q --allow-empty -m "chore: start"
base=$(git rev-parse HEAD)

# commit AUTHOR MESSAGE: an empty commit by AUTHOR, "Name <email>", with MESSAGE.
commit() {
  git commit -q --allow-empty --author="$1" -m "$2"
  git rev-parse HEAD
}
# dco BASE HEAD: dco.sh on the commits of BASE..HEAD.
dco() {
  bash "$scripts/dco.sh" "$1" "$2"
}

ada="Ada Lovelace <ada@example.com>"
renovate="renovate[bot] <29139614+renovate[bot]@users.noreply.github.com>"
bot="chalkreleaser[bot] <359050260+chalkreleaser[bot]@users.noreply.github.com>"

signed=$(commit "$ada" "$(printf 'feat: a\n\nSigned-off-by: %s' "$ada")")
expect pass "a commit signed off by its author" dco "$base" "$signed"
expect pass "an empty range" dco "$signed" "$signed"

git reset -q --hard "$base"
unsigned=$(commit "$ada" "fix: b")
expect fail "a commit without a sign-off" dco "$base" "$unsigned"

git reset -q --hard "$base"
other=$(commit "$ada" "$(printf 'fix: c\n\nSigned-off-by: Charles Babbage <charles@example.com>')")
expect fail "a commit signed off by someone else" dco "$base" "$other"

git reset -q --hard "$base"
email=$(commit "$ada" "$(printf 'fix: d\n\nSigned-off-by: Ada Lovelace <ada@example.org>')")
expect fail "a sign-off with another email" dco "$base" "$email"

git reset -q --hard "$base"
name=$(commit "$ada" "$(printf 'fix: e\n\nSigned-off-by: A. Lovelace <ada@example.com>')")
expect fail "a sign-off with another name" dco "$base" "$name"

git reset -q --hard "$base"
body=$(commit "$ada" "$(printf 'fix: f\n\nSigned-off-by: %s\n\nA paragraph after it.' "$ada")")
expect fail "a sign-off in the body, not among the trailers" dco "$base" "$body"

git reset -q --hard "$base"
several=$(commit "$ada" "$(printf 'fix: g\n\nSigned-off-by: Charles Babbage <charles@example.com>\nSigned-off-by: %s' "$ada")")
expect pass "the author's sign-off among others" dco "$base" "$several"

git reset -q --hard "$base"
commit "$renovate" "build(deps): update a module" >/dev/null
fixup=$(commit "$bot" "build(deps): update the vendor hash")
expect pass "Renovate's and the App bot's commits without a sign-off" dco "$base" "$fixup"

git reset -q --hard "$base"
first=$(commit "$ada" "$(printf 'fix: h\n\nSigned-off-by: %s' "$ada")")
mixed=$(commit "$ada" "fix: i")
commit "$ada" "$(printf 'fix: j\n\nSigned-off-by: %s' "$ada")" >/dev/null
expect fail "one unsigned commit among signed ones" dco "$base" HEAD
expect pass "the signed commits before it" dco "$base" "$first"
expect fail "the unsigned commit alone" dco "$first" "$mixed"

git reset -q --hard "$base"
main=$(commit "$ada" "$(printf 'fix: k\n\nSigned-off-by: %s' "$ada")")
git switch -q -c topic "$base"
commit "$ada" "$(printf 'fix: l\n\nSigned-off-by: %s' "$ada")" >/dev/null
git merge -q --no-ff -m "Merge branch 'main' into topic" "$main"
expect pass "a merge commit without a sign-off" dco "$base" HEAD

git rebase -q --signoff --onto "$base" "$base" "$unsigned"
expect pass "a commit after git rebase --signoff" dco "$base" HEAD

for title in \
  "feat: add a kind" \
  "fix(chalkctl): check every node" \
  "chore(main): release 0.2.2" \
  "build(deps): update the flake inputs" \
  "refactor(chalkctl,chalkd)!: rename the flag" \
  "feat!: drop the old API" \
  "ci(release): rebuild the release pull request" \
  "style(chalkd): wrap a doc comment" \
  "deps: update module golang.org/x/net to v0.62.0" \
  "revert: feat: add a kind"; do
  expect pass "the title '$title'" bash "$scripts/pr-title.sh" "$title"
done
for title in \
  "Add a kind" \
  "feature: add a kind" \
  "wip: add a kind" \
  "feat:add a kind" \
  "feat: " \
  "feat(): add a kind" \
  "feat(a b): add a kind" \
  "Feat: add a kind" \
  " feat: add a kind" \
  "feat!(chalkctl): add a kind"; do
  expect fail "the title '$title'" bash "$scripts/pr-title.sh" "$title"
done

# vulnix-issue.sh on reports made for the test: a high-score match is new until the previous body
# lists it, and a low score, a match without a score or a whitelisted CVE never counts.
vulnix=$(mktemp -d)
mkdir "$vulnix/reports"
cat >"$vulnix/reports/node-kvm.json" <<'JSON'
[
 {"name": "systemd-259.1", "pname": "systemd", "version": "259.1",
  "affected_by": ["CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"], "whitelisted": ["CVE-2026-0009"],
  "cvssv3_basescore": {"CVE-2026-0001": 9.8, "CVE-2026-0002": 5.3, "CVE-2026-0009": 9.1}}
]
JSON
# vulnix_issue PREVIOUS: vulnix-issue.sh on the reports with PREVIOUS as the last body, which
# writes the body to body.md and the new rows to new.md.
vulnix_issue() {
  bash "$scripts/vulnix-issue.sh" "$vulnix/reports" "$1" "$vulnix/new.md" >"$vulnix/body.md"
}
row='| node-kvm | systemd | CVE-2026-0001 | 9.8 |'
: >"$vulnix/empty.md"
vulnix_issue "$vulnix/empty.md"
expect pass "a high-score match is new without a previous issue" grep -qxF -- "$row" "$vulnix/new.md"
expect pass "the new match is the only one" test "$(wc -l <"$vulnix/new.md")" = 1
expect pass "the body lists the match" grep -qxF -- "$row" "$vulnix/body.md"
expect fail "the body leaves out a low score" grep -qF CVE-2026-0002 "$vulnix/body.md"
expect fail "the body leaves out a match without a score" grep -qF CVE-2026-0003 "$vulnix/body.md"
expect fail "the body leaves out a whitelisted CVE" grep -qF CVE-2026-0009 "$vulnix/body.md"
cp "$vulnix/body.md" "$vulnix/previous.md"
vulnix_issue "$vulnix/previous.md"
expect pass "a match the previous issue lists is not new" test ! -s "$vulnix/new.md"
sed 's/| 9.8 |/| 9.9 |/' "$vulnix/previous.md" >"$vulnix/rescored.md"
vulnix_issue "$vulnix/rescored.md"
expect pass "a changed score does not make a match new" test ! -s "$vulnix/new.md"
cp "$vulnix/reports/node-kvm.json" "$vulnix/reports/router-kvm.json"
vulnix_issue "$vulnix/previous.md"
expect pass "the same match in another image is new" grep -qxF -- '| router-kvm | systemd | CVE-2026-0001 | 9.8 |' "$vulnix/new.md"
expect pass "the match the previous issue lists stays old" test "$(wc -l <"$vulnix/new.md")" = 1
rm -rf "$vulnix"

# retried.sh on first attempts made for the test: it names the checks whose builds failed, in
# either of Nix's forms, and the tests that failed in them, and says when the log names none.
retried=$(mktemp -d)
cat >"$retried/first.log" <<'LOG'
chalkos-go-unit> --- FAIL: TestTrialRollsBackAtItsDeadline (0.10s)
chalkos-go-unit> FAIL
error: Cannot build '/nix/store/jrc222f7m6a2205ayvvw8bivncq7hknr-chalkos-go-unit.drv'.
error: builder for '/nix/store/abc222f7m6a2205ayvvw8bivncq7hknr-chalkos-eval-tests.drv' failed with exit code 1
LOG
bash "$scripts/retried.sh" fast "$retried/first.log" >"$retried/summary.md"
expect pass "the summary names the job" grep -qxF "### fast passed on its retry" "$retried/summary.md"
expect pass "the summary names a build Nix cannot build" grep -qxF -- "- \`chalkos-go-unit\`" "$retried/summary.md"
expect pass "the summary names a failed builder" grep -qxF -- "- \`chalkos-eval-tests\`" "$retried/summary.md"
expect pass "the summary names the failed test" grep -qxF -- "chalkos-go-unit> --- FAIL: TestTrialRollsBackAtItsDeadline (0.10s)" "$retried/summary.md"
echo "error: unable to download 'https://proxy.golang.org/x'" >"$retried/download.log"
bash "$scripts/retried.sh" fast "$retried/download.log" >"$retried/summary.md"
expect pass "a log without a failed test says so" grep -qF "names no failed test" "$retried/summary.md"
rm -rf "$retried"

if [ "$failures" != 0 ]; then
  echo "$failures cases failed" >&2
  exit 1
fi
