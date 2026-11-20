#!/usr/bin/env bash
# Pushes the local commits of BASE..HEAD to the branch BRANCH of the repository GITHUB_REPOSITORY,
# which must point at BASE, as commits that GitHub makes and signs:
#
#   push-signed.sh BRANCH BASE
#
# Each local commit becomes one createCommitOnBranch of GitHub's GraphQL API with its message and
# its changed files, made with the GitHub App token in GH_TOKEN. GitHub authors such a commit as
# the App's bot and signs it, so it shows as verified, which a commit that git pushes does not
# without a signing key. The API sets no file modes, so the script refuses a change to a file
# that is not a regular, non-executable file, and a merge. It fails unless the last commit's tree
# is HEAD's.
set -euo pipefail

branch=$1 base=$2
repo=${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY to owner/name}
if [ -n "$(git rev-list --merges "$base..HEAD")" ]; then
  echo "error: $base..HEAD holds a merge, which createCommitOnBranch cannot make" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# The GraphQL variable stays as it is.
# shellcheck disable=SC2016
query='mutation($input: CreateCommitOnBranchInput!) { createCommitOnBranch(input: $input) { commit { oid } } }'
head=$(git rev-parse "$base")
for commit in $(git rev-list --reverse "$base..HEAD"); do
  : >"$work/additions"
  : >"$work/deletions"
  while IFS= read -r -d '' status && IFS= read -r -d '' path; do
    if [ "$status" = D ]; then
      jq -n --arg path "$path" '{path: $path}' >>"$work/deletions"
      continue
    fi
    mode=$(git ls-tree "$commit" -- "$path" | cut -d ' ' -f 1)
    if [ "$mode" != 100644 ]; then
      echo "error: $commit changes $path, of mode $mode; createCommitOnBranch makes regular files alone" >&2
      exit 1
    fi
    git show "$commit:$path" | base64 -w 0 >"$work/contents"
    jq -n --arg path "$path" --rawfile contents "$work/contents" '{path: $path, contents: $contents}' >>"$work/additions"
  done < <(git diff-tree -z --no-commit-id --no-renames -r --name-status "$commit")
  jq -n --arg query "$query" --arg repo "$repo" --arg branch "$branch" --arg head "$head" \
    --arg headline "$(git show -s --format=%s "$commit")" --arg body "$(git show -s --format=%b "$commit")" \
    --slurpfile additions "$work/additions" --slurpfile deletions "$work/deletions" \
    '{query: $query, variables: {input: {
      branch: {repositoryNameWithOwner: $repo, branchName: $branch},
      expectedHeadOid: $head,
      message: {headline: $headline, body: $body},
      fileChanges: {additions: $additions, deletions: $deletions}
    }}}' >"$work/request.json"
  head=$(gh api graphql --input "$work/request.json" --jq '.data.createCommitOnBranch.commit.oid')
  if [ -z "$head" ]; then
    echo "error: createCommitOnBranch made no commit of $commit" >&2
    exit 1
  fi
  echo "$branch: $head from $(git show -s --format='%h %s' "$commit")"
done

tree=$(gh api "repos/$repo/git/commits/$head" --jq .tree.sha)
if [ "$tree" != "$(git rev-parse 'HEAD^{tree}')" ]; then
  echo "error: $branch's tree $tree differs from HEAD's" >&2
  exit 1
fi
