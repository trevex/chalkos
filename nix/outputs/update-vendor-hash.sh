# update-vendor-hash: after a change of go.mod or go.sum, sets the vendorHash that the Go programs
# of the chalkos repository in the working directory share, in nix/overlay/build-go-program.nix.
# It builds their module download with lib.fakeHash, so that a download of the old hash in the
# store cannot pass for the new one, and writes back the hash that the build reports. A second run
# changes nothing. It needs network access to the Go module proxy, and runs nix of the caller's
# PATH.

file=nix/overlay/build-go-program.nix
if [[ ! -f $file || ! -f go.mod ]]; then
  echo "error: update-vendor-hash runs in the root of the chalkos repository" >&2
  exit 1
fi
pattern='^(\s*vendorHash = ")(sha256-[A-Za-z0-9+/=]+)(";)$'
old=$(grep -P "$pattern" "$file" | sed -E "s|$pattern|\2|")
if [[ -z $old ]]; then
  echo "error: $file sets no vendorHash" >&2
  exit 1
fi
setHash() {
  sed -i -E "s|$pattern|\1$1\3|" "$file"
}
work=$(mktemp -d)
# The file gets the old hash back when the build fails, and the new one once the build reports it.
hash=$old
trap 'setHash "$hash"; rm -rf "$work"' EXIT
setHash sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=

if nix build --no-link .#chalkctl.goModules 2>"$work/log"; then
  echo "error: the module download built with the fake hash" >&2
  exit 1
fi
new=$(sed -nE 's/^\s*got:\s*(sha256-[A-Za-z0-9+/=]+)$/\1/p' "$work/log" | tail -n 1)
if [[ -z $new ]]; then
  cat "$work/log" >&2
  echo "error: the module download failed without a hash mismatch" >&2
  exit 1
fi
hash=$new
if [[ $new == "$old" ]]; then
  echo "vendorHash: $new, unchanged"
else
  echo "vendorHash: $new"
fi
