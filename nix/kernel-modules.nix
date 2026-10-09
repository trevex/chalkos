# Builds a role's kernel module tree from nixpkgs's prebuilt one, and checks that a tree holds the
# modules a role loads.
#
#   chalkos-kernel-modules filter SOURCE OUT DIRECTORIES NAMES
#     copies to OUT the modules of SOURCE below the directories the file DIRECTORIES lists
#     (relative to the tree's kernel/ directory) and the modules the file NAMES lists, with what
#     they depend on and their soft dependencies, then runs depmod. A directory without modules
#     or a name modprobe does not know fails.
#   chalkos-kernel-modules check TREE NAMES
#     fails unless every module the file NAMES lists is in TREE or built into the kernel.
{
  writeShellApplication,
  coreutils,
  gawk,
  gnused,
  kmod,
}:
writeShellApplication {
  name = "chalkos-kernel-modules";
  runtimeInputs = [
    coreutils
    gawk
    gnused
    kmod
  ];
  text = ''
    # The one kernel version below TREE/lib/modules.
    version() {
      local versions
      versions=$(ls "$1/lib/modules")
      if [[ $(wc -w <<<"$versions") != 1 ]]; then
        echo "error: $1 holds modules of kernels '$versions', want one" >&2
        exit 1
      fi
      echo "$versions"
    }

    # The module files, relative to the tree's version directory, that loading the module named
    # takes, itself last; nothing for a built-in module. modprobe resolves aliases and dashes.
    resolve() {
      local tree=$1 version=$2 name=$3 out
      out=$(modprobe --config no-config -d "$tree" -S "$version" --ignore-install --show-depends "$name" 2>/dev/null) || return 1
      awk -v root="$tree/lib/modules/$version/" '$1 == "insmod" { sub("^" root, "", $2); print $2 }' <<<"$out"
    }

    filter() {
      local source=$1 out=$2 directories=$3 names=$4
      local version root work failed=0 dir name before after
      version=$(version "$source")
      root=$source/lib/modules/$version
      work=$(mktemp -d)
      : >"$work/selected"

      while read -r dir; do
        [[ -n $dir ]] || continue
        if ! awk -F: -v prefix="kernel/$dir/" 'index($1, prefix) == 1 { print $1; found = 1 } END { exit !found }' \
          "$root/modules.dep" >>"$work/selected"; then
          echo "error: no kernel module below $dir" >&2
          failed=1
        fi
      done <"$directories"
      while read -r name; do
        [[ -n $name ]] || continue
        if ! resolve "$source" "$version" "$name" >>"$work/selected"; then
          echo "error: unknown kernel module $name" >&2
          failed=1
        fi
      done <"$names"
      if [[ $failed != 0 ]]; then exit 1; fi

      # What the selected modules depend on, which modules.dep lists in full, and their soft
      # dependencies, until nothing is added.
      sort -u "$work/selected" -o "$work/selected"
      while :; do
        before=$(wc -l <"$work/selected")
        awk -F': *' 'NR == FNR { want[$0] = 1; next } ($1 in want) && $2 != "" { n = split($2, deps, " "); for (i = 1; i <= n; i++) print deps[i] }' \
          "$work/selected" "$root/modules.dep" >"$work/added"
        sed -E 's|.*/||; s|\.ko(\.[a-z]+)?$||; s|-|_|g' "$work/selected" | sort -u >"$work/loaded"
        awk 'NR == FNR { want[$0] = 1; next } $1 == "softdep" { m = $2; gsub("-", "_", m); if (m in want) for (i = 3; i <= NF; i++) if ($i != "pre:" && $i != "post:") print $i }' \
          "$work/loaded" "$root/modules.softdep" | sort -u | while read -r name; do
          # A soft dependency the tree does not have is skipped, as modprobe skips it.
          resolve "$source" "$version" "$name" || true
        done >>"$work/added"
        sort -u "$work/selected" "$work/added" -o "$work/selected"
        after=$(wc -l <"$work/selected")
        if [[ $before == "$after" ]]; then break; fi
      done

      mkdir -p "$out/lib/modules/$version"
      (cd "$root" && xargs -r -a "$work/selected" cp --parents --no-preserve=mode -t "$out/lib/modules/$version")
      cp --no-preserve=mode "$root"/modules.builtin "$root"/modules.builtin.modinfo "$root"/modules.order "$out/lib/modules/$version/"
      depmod -b "$out" -C no-config "$version"
      echo "kept $(wc -l <"$work/selected") of $(wc -l <"$root/modules.dep") kernel modules"
      rm -r "$work"
    }

    check() {
      local tree=$1 names=$2 version name missing=()
      version=$(version "$tree")
      while read -r name; do
        [[ -n $name ]] || continue
        resolve "$tree" "$version" "$name" >/dev/null || missing+=("$name")
      done <"$names"
      if [[ ''${#missing[@]} != 0 ]]; then
        echo "error: the image loads kernel modules its module tree does not hold: ''${missing[*]}" >&2
        echo "add them with chalkos.kernel.extraModules, or their group to chalkos.kernel.moduleGroups" >&2
        exit 1
      fi
    }

    case ''${1:-} in
      filter) filter "''${@:2}" ;;
      check) check "''${@:2}" ;;
      *)
        echo "usage: chalkos-kernel-modules filter SOURCE OUT DIRECTORIES NAMES | check TREE NAMES" >&2
        exit 2
        ;;
    esac
  '';
}
