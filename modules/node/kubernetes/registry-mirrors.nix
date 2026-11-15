# Writes containerd's hosts.toml files from the output of mirrorsCommand, chalkd kubernetes
# containerd-hosts: for each registry a line "=== <directory>" and then its hosts.toml. The files
# go to a new directory that is swapped in for /run/containerd/certs.d in one rename, so
# containerd never reads a mix of two configs nor finds none; a failing command leaves it as it was.
{
  coreutils,
  mirrorsCommand,
  writeShellApplication,
}:
writeShellApplication {
  name = "chalkos-registry-mirrors";
  runtimeInputs = [ coreutils ];
  text = ''
    dir=/run/containerd/certs.d
    parent=$(dirname "$dir")
    mkdir -p "$parent"
    tmp=$(mktemp -d "$parent/.certs.d.XXXXXX")
    # After the swap, $tmp holds the old files.
    trap 'rm -rf "$tmp"' EXIT
    ${mirrorsCommand} >"$tmp/.documents"
    out=""
    while IFS= read -r line; do
      if [[ "$line" == "=== "* ]]; then
        registry=''${line#=== }
        mkdir "$tmp/$registry"
        out="$tmp/$registry/hosts.toml"
        : >"$out"
      elif [ -n "$out" ]; then
        printf '%s\n' "$line" >>"$out"
      fi
    done <"$tmp/.documents"
    rm "$tmp/.documents"
    chmod 0755 "$tmp"
    if [ -e "$dir" ]; then
      mv -T --exchange "$tmp" "$dir"
    else
      mv -T "$tmp" "$dir"
    fi
  '';
}
