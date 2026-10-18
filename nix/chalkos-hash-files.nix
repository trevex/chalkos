# chalkos-hash-files PATH: a command fact that prints, as a JSON string, the hex SHA-256 of the
# file at PATH, or of the names and contents of the files in the directory at PATH, in the order
# of their names; null when PATH is missing. Rules compare files with it without seeing them, as
# the networkd units chalkd writes with the values of their secretRefs.
{ writeShellScript, coreutils }:
writeShellScript "chalkos-hash-files" ''
  set -eu
  export PATH=${coreutils}/bin LC_ALL=C
  # A fact's command reads the config on standard input, which this one has no use for.
  cat >/dev/null
  path=$1
  if [ -d "$path" ]; then
    sum=$(
      cd "$path"
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
