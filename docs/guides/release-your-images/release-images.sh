#!/usr/bin/env bash
# Releases the cluster's images from a checkout of the tag v<version>: builds each package
# <image>-oci of the flake as an OCI layout, signs it for Secure Boot when DB_KEY_FILE and
# DB_CERT_FILE name the db key and certificate, pushes it to $REGISTRY/<image>:<tag>, signs the
# pushed digest with cosign when COSIGN_KEY names a key or COSIGN_KEYLESS=1, and writes each
# image's reference with its digest to images.txt. Run it in the flake's directory, logged in to
# the registry with docker login.
set -euo pipefail

: "${REGISTRY:?set REGISTRY to the registry and path to push to, such as registry.example.com/cluster}"

tag=$(git describe --tags --exact-match)
version=$(nix eval --raw --file version.nix)
if [ "$tag" != "v$version" ]; then
  echo "error: the commit is tagged $tag, but version.nix names $version" >&2
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "error: the checkout has changes; the images would record a dirty revision" >&2
  exit 1
fi

images=$(nix eval --json --no-update-lock-file .#packages.x86_64-linux --apply builtins.attrNames |
  jq -r '.[] | select(endswith("-oci")) | rtrimstr("-oci")')
if [ -z "$images" ]; then
  echo "error: the flake has no package named <image>-oci" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
nix build --inputs-from . -o "$work/chalkbuild" chalkos#chalkbuild
chalkbuild=$work/chalkbuild/bin/chalkbuild

: >images.txt
for name in $images; do
  repository=$REGISTRY/$name
  layout=$work/$name
  nix build --print-build-logs -o "$layout" ".#$name-oci"
  if [ -n "${DB_KEY_FILE:-}" ]; then
    "$chalkbuild" sign --key "$DB_KEY_FILE" --cert "$DB_CERT_FILE" "$layout" "$work/$name-signed"
    layout=$work/$name-signed
  fi
  pushed=$("$chalkbuild" push "$layout" "$repository:$tag")
  digest=${pushed#"$repository@"}
  if [ -n "${COSIGN_KEY:-}" ]; then
    cosign sign --yes --key "$COSIGN_KEY" "$pushed"
  elif [ "${COSIGN_KEYLESS:-}" = 1 ]; then
    cosign sign --yes "$pushed"
  fi
  echo "$repository:$tag@$digest" | tee -a images.txt
done
