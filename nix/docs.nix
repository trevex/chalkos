# The documentation site, built with Zensical from zensical.toml and docs/. The flake's source
# holds tracked files alone, so docs/superpowers, which git ignores, never reaches the site.
{ pkgs }:
let
  inherit (pkgs) lib;
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../zensical.toml
      ../docs
    ];
  };
in
{
  # The site, built strictly, so a broken link fails the build. Zensical skips a page the
  # navigation names but that is missing, so the navigation is checked first.
  site =
    pkgs.runCommand "chalkos-docs"
      {
        nativeBuildInputs = [
          pkgs.zensical
          pkgs.python3
        ];
      }
      ''
        export HOME=$TMPDIR
        cp -r ${src} work
        chmod -R u+w work
        cd work
        python3 ${./docs-nav.py} zensical.toml
        zensical build --strict --clean
        cp -r site $out
      '';

  # Serves the working tree's docs while they are edited. Zensical has no way to exclude a
  # directory, so it serves a copy without docs/superpowers, which is kept in sync every second.
  serve = pkgs.writeShellApplication {
    name = "chalkos-docs-serve";
    runtimeInputs = [
      pkgs.zensical
      pkgs.rsync
    ];
    text = ''
      if [ ! -f zensical.toml ] || [ ! -d docs ]; then
        echo "chalkos-docs-serve: run it in the repository's root, where zensical.toml is" >&2
        exit 1
      fi
      work=$(mktemp -d)
      trap 'kill "$syncer" 2>/dev/null; rm -rf "$work"' EXIT
      copy() {
        rsync -a --delete --exclude /superpowers/ docs/ "$work/docs/"
        rsync -a zensical.toml "$work/zensical.toml"
      }
      copy
      while sleep 1; do copy; done &
      syncer=$!
      zensical serve -f "$work/zensical.toml" "$@"
    '';
  };
}
