#!/usr/bin/env bash
# Checks the Developer Certificate of Origin sign-off of each commit in BASE..HEAD:
#
#   dco.sh BASE HEAD
#
# A commit passes when a Signed-off-by trailer names its author exactly, as "Name <email>". The
# commits of Renovate and of the release GitHub App's bot pass without one, and so do merge
# commits, which a squash merge leaves out of main. The check lists every commit that fails and
# how to sign them off.
set -euo pipefail

base=$1 head=$2
exempt=(
  "29139614+renovate[bot]@users.noreply.github.com"
  "359050260+chalkreleaser[bot]@users.noreply.github.com"
)

unsigned=()
while read -r commit; do
  email=$(git show -s --format=%ae "$commit")
  case " ${exempt[*]} " in
    *" $email "*) continue ;;
  esac
  author=$(git show -s --format='%an <%ae>' "$commit")
  signoffs=$(git show -s --format='%(trailers:key=Signed-off-by,valueonly,unfold)' "$commit")
  if ! grep -qxF -- "$author" <<<"$signoffs"; then
    unsigned+=("$(git show -s --format='%h %s (%an <%ae>)' "$commit")")
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ ${#unsigned[@]} = 0 ]; then
  echo "every commit of $base..$head is signed off by its author"
  exit 0
fi
if [ -n "${GITHUB_ACTIONS:-}" ]; then
  echo "::error title=Missing sign-off::${#unsigned[@]} commits lack a Signed-off-by trailer that names their author"
fi
{
  echo "These commits lack a Signed-off-by trailer that names their author:"
  printf '  %s\n' "${unsigned[@]}"
  echo
  echo "A sign-off certifies the Developer Certificate of Origin, https://developercertificate.org/."
  echo "Sign the commits off and push them again:"
  echo
  echo "  git rebase --signoff origin/main"
  echo "  git push --force-with-lease"
} >&2
exit 1
