# Builds a role's kernel module tree from nixpkgs's prebuilt one, and checks that a tree holds the
# modules a role loads and those its modules depend on.
#
#   chalkos-kernel-modules filter SOURCE OUT DIRECTORIES NAMES [EXTRA...]
#     copies to OUT the modules of SOURCE below the directories the file DIRECTORIES lists
#     (relative to the tree's kernel/ directory), the modules the file NAMES lists and those the
#     out-of-tree modules of the EXTRA packages depend on, with what they depend on and their soft
#     dependencies, then runs depmod. A directory without modules, a name modprobe does not know
#     and a dependency of an out-of-tree module that neither SOURCE nor EXTRA has fail.
#   chalkos-kernel-modules check TREE NAMES
#     fails unless every module the file NAMES lists is in TREE or built into the kernel, and every
#     module of TREE finds the modules it depends on there.
{
  writeShellApplication,
  coreutils,
  findutils,
  gawk,
  gnused,
  kmod,
}:
writeShellApplication {
  name = "chalkos-kernel-modules";
  runtimeInputs = [
    coreutils
    findutils
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
      awk -v root="$tree/lib/modules/$version/" '$1 == "insmod" && index($2, root) == 1 { print substr($2, length(root) + 1) }' <<<"$out"
    }

    # The names of module files, read from stdin, as the kernel names the modules.
    moduleNames() {
      sed -E 's|.*/||; s|\.ko(\.[a-z]+)?$||; s|-|_|g' | sort -u
    }

    # For each module file read from stdin: "depends MODULE DEPENDENCY" for each module its
    # modinfo names as a dependency, and "softdep MODULE DEPENDENCY" for each soft dependency.
    dependencies() {
      xargs -r modinfo | awk '
        $1 == "filename:" { module = $2; sub(".*/", "", module); sub("[.]ko([.][a-z]+)?$", "", module); gsub("-", "_", module) }
        $1 == "depends:" && NF > 1 { n = split($2, deps, ","); for (i = 1; i <= n; i++) { d = deps[i]; gsub("-", "_", d); print "depends", module, d } }
        $1 == "softdep:" { for (i = 2; i <= NF; i++) if ($i != "pre:" && $i != "post:") print "softdep", module, $i }'
    }

    filter() {
      local source=$1 out=$2 directories=$3 names=$4 extras=("''${@:5}")
      local version root work failed=0 dir name before after kind module dependency extra
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

      # What the out-of-tree modules depend on, unless they bring it themselves: from the kernel's
      # tree, as modprobe would load it.
      for extra in "''${extras[@]}"; do
        if [[ -d $extra/lib/modules/$version ]]; then find -L "$extra/lib/modules/$version" -name '*.ko*'; fi
      done >"$work/extra"
      moduleNames <"$work/extra" >"$work/extraNames"
      dependencies <"$work/extra" >"$work/extraDependencies"
      while read -r kind module dependency; do
        if grep -qxF "$dependency" "$work/extraNames"; then continue; fi
        if ! resolve "$source" "$version" "$dependency" >>"$work/selected" && [[ $kind == depends ]]; then
          echo "error: the out-of-tree module $module depends on $dependency, which the kernel does not have" >&2
          failed=1
        fi
      done <"$work/extraDependencies"
      if [[ $failed != 0 ]]; then exit 1; fi

      # What the selected modules depend on, which modules.dep lists in full, and their soft
      # dependencies, until nothing is added.
      sort -u "$work/selected" -o "$work/selected"
      while :; do
        before=$(wc -l <"$work/selected")
        awk -F': *' 'NR == FNR { want[$0] = 1; next } ($1 in want) && $2 != "" { n = split($2, deps, " "); for (i = 1; i <= n; i++) print deps[i] }' \
          "$work/selected" "$root/modules.dep" >"$work/added"
        moduleNames <"$work/selected" >"$work/loaded"
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
      local tree=$1 names=$2 version root work name missing=() failed=0 module dependency
      version=$(version "$tree")
      root=$tree/lib/modules/$version
      while read -r name; do
        [[ -n $name ]] || continue
        resolve "$tree" "$version" "$name" >/dev/null || missing+=("$name")
      done <"$names"
      if [[ ''${#missing[@]} != 0 ]]; then
        echo "error: the image loads kernel modules its module tree does not hold: ''${missing[*]}" >&2
        echo "add them with chalkos.kernel.extraModules, or their group to chalkos.kernel.moduleGroups" >&2
        failed=1
      fi

      # depmod leaves a dependency it cannot find out of modules.dep, so modinfo tells.
      work=$(mktemp -d)
      find -L "$root" -name '*.ko*' >"$work/modules"
      {
        moduleNames <"$work/modules"
        if [[ -f $root/modules.builtin ]]; then moduleNames <"$root/modules.builtin"; fi
      } >"$work/names"
      dependencies <"$work/modules" | awk 'NR == FNR { have[$0] = 1; next } $1 == "depends" && !($3 in have) { print $2, $3 }' \
        "$work/names" - >"$work/missing"
      while read -r module dependency; do
        echo "error: the kernel module $module depends on $dependency, which the image's module tree does not hold" >&2
        failed=1
      done <"$work/missing"
      rm -r "$work"
      if [[ $failed != 0 ]]; then exit 1; fi
    }

    case ''${1:-} in
      filter) filter "''${@:2}" ;;
      check) check "''${@:2}" ;;
      *)
        echo "usage: chalkos-kernel-modules filter SOURCE OUT DIRECTORIES NAMES [EXTRA...] | check TREE NAMES" >&2
        exit 2
        ;;
    esac
  '';
}
