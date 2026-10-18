# chalkos-hash-files PATH: a command fact that prints, as a JSON string, the hex SHA-256 of the
# file at PATH, or of the names and contents of the files in the directory at PATH, in the order
# of their names; null when PATH is missing. Rules compare files with it without seeing them, as
# the networkd units chalkd writes with the values of their secretRefs. A file or directory that
# cannot be read fails it, rather than hash as something it is not.
{ writeShellScript, coreutils }:
writeShellScript "chalkos-hash-files" ''
  set -euo pipefail
  # A command substitution keeps -e, so a file that cannot be read fails the hash.
  shopt -s inherit_errexit
  export PATH=${coreutils}/bin LC_ALL=C
  unset CDPATH
  # A fact's command reads the config on standard input, which this one has no use for.
  cat >/dev/null
  path=$1
  if [ -d "$path" ]; then
    # Out here a failed cd ends the script, rather than leave the names below those of another
    # directory.
    cd -- "$path"
    # A directory that cannot be listed would hash as an empty one.
    if [ ! -r . ]; then
      echo "chalkos-hash-files: cannot list $path" >&2
      exit 1
    fi
    sum=$(
      for f in *; do
        [ -f "$f" ] || continue
        printf '%s\n' "$f"
        sha256sum <"$f"
      done | sha256sum
    )
  elif [ -e "$path" ]; then
    sum=$(sha256sum <"$path")
  else
    echo null
    exit 0
  fi
  printf '"%s"\n' "''${sum%% *}"
''
