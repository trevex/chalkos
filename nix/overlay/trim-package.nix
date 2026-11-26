# pkgs.chalkos.trimPackage: a copy of part of a package, for an image that carries no shell and only
# what it runs. keep lists the programs of from's bin to copy, by name, or other packages' programs
# by store path; null copies the whole of from. libs copies the libraries of from's lib, globs
# allowed. drop removes paths below the copy, such as bin/fsck.btrfs, and dropScripts every program
# of its bin that starts with #!, with the links to them. The run paths of the copy's programs and
# libraries name the copy's lib in place of from's, and the to of each { from; to; } of relink in
# place of its from. fix runs last, in the copy. The copy keeps from's version and meta, and its
# pname unless pname is given, with out as its only output, and must not reference from or a
# package relink replaces, so it never brings the original into the image. A pattern of libs that
# matches nothing fails the build, as does a program or library that needs a library its run path
# lacks.
{
  lib,
  runCommand,
  patchelf,
  removeReferencesTo,
}:
{
  from,
  pname ? lib.getName from,
  keep ? null,
  drop ? [ ],
  dropScripts ? false,
  libs ? [ ],
  relink ? [ ],
  fix ? "",
}:
runCommand "${pname}-${from.version}"
  {
    inherit pname;
    inherit (from) version;
    # The copy has out alone; the original's meta can name man too, for the system path.
    meta = (from.meta or { }) // {
      outputsToInstall = [ "out" ];
    };
    nativeBuildInputs = [
      patchelf
      removeReferencesTo
    ];
    disallowedReferences = [ from ] ++ map (r: r.from) relink;
    programs = keep;
    inherit libs drop;
  }
  (
    (
      if keep == null then
        ''
          cp -a ${from} $out
        ''
      else
        ''
          mkdir -p $out/bin
          for p in $programs; do
            case $p in
              /*) cp "$p" $out/bin/ ;;
              *) cp ${from}/bin/"$p" $out/bin/ ;;
            esac
          done
        ''
    )
    # stdenv sets nullglob, which drops a pattern that matches nothing; failglob fails on it, and
    # the patterns match in from's lib.
    + lib.optionalString (libs != [ ]) ''
      mkdir -p $out/lib
      (
        cd ${from}/lib
        shopt -s failglob
        cp -P $libs $out/lib/
      )
    ''
    + ''
      chmod -R u+w $out
      for d in $drop; do
        rm -r "$out/$d"
      done
    ''
    + lib.optionalString dropScripts ''
      for f in $out/bin/*; do
        if [ ! -L "$f" ] && [ "$(head -c 2 "$f")" = "#!" ]; then rm "$f"; fi
      done
      find $out/bin -xtype l -delete
    ''
    # A file that is no dynamically linked ELF, as a script or a static program, has no run path.
    # Each library a program or library needs lies on its run path or in its interpreter's
    # directory, glibc's, so the copy runs.
    + ''
      find $out -type f | while read -r f; do
        rpath=$(patchelf --print-rpath "$f" 2>/dev/null) || continue
        old=${from}/lib
        new=''${rpath//"$old"/$out/lib}
        ${lib.concatMapStrings (r: ''
          old=${r.from}/lib
          new=''${new//"$old"/${r.to}/lib}
        '') relink}
        [ "$new" = "$rpath" ] || patchelf --set-rpath "$new" "$f"
        interp=$(patchelf --print-interpreter "$f" 2>/dev/null) || interp=
        for n in $(patchelf --print-needed "$f"); do
          (
            IFS=:
            for d in $new ''${interp%/*}; do [ -e "$d/$n" ] && exit 0; done
            exit 1
          ) || {
            echo "error: $f needs $n, which is not on its run path $new" >&2
            exit 1
          }
        done
      done
    ''
    + fix
  )
