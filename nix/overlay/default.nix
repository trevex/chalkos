# Adds pkgs.chalkos: the Go toolchain go.mod names, buildGoProgram, and a package per file of
# nix/packages. Updating Go is a change to go.mod's go line. mkImage applies the overlay to the
# images' nixpkgs, so images of another nixpkgs build chalkos's programs with this Go too.
{ go-overlay }:
final: prev: {
  chalkos = final.lib.makeScope final.newScope (
    self:
    final.lib.packagesFromDirectoryRecursive {
      inherit (self) callPackage;
      directory = ../packages;
    }
    // {
      go = (go-overlay.lib.mkGoBin final).fromGoModStrict ../../go.mod;
      buildGoProgram = self.callPackage ./build-go-program.nix { };
      # The whole tree's source paths, which chalkctl, chalklab, the e2e tests and the go-unit and
      # go-lint checks build from; a program's passthru.goPaths lists its own paths instead.
      treeGoPaths = import ./go-paths.nix { inherit (final) lib; };
      # kea.nix and frr.nix wrap nixpkgs's packages, whose names inside the scope are their own,
      # so they are passed nixpkgs's explicitly. The images' pkgs.frr is chalkos's, so frr.nix
      # takes the one before this overlay.
      kea = self.callPackage ../packages/kea.nix { inherit (final) kea; };
      frr = self.callPackage ../packages/frr.nix { inherit (prev) frr; };
    }
  );
}
