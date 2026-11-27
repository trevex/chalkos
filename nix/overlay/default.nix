# Adds pkgs.chalkos: the Go toolchain go.mod names, buildGoProgram, trimPackage, containerDisk and
# a package per file of nix/packages. Updating Go is a change to go.mod's go line. mkImage applies
# the overlay to the images' nixpkgs, so images of another nixpkgs build chalkos's programs with
# this Go too.
{ go-overlay }:
final: _: {
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
      # A copy of part of a package, as the shell-free images carry it; see trim-package.nix.
      trimPackage = import ./trim-package.nix {
        inherit (final)
          lib
          runCommand
          patchelf
          removeReferencesTo
          ;
      };
      # A KubeVirt containerDisk of an image directory; see container-disk.nix.
      containerDisk = import ./container-disk.nix {
        inherit (final)
          gnutar
          jq
          lib
          qemu-utils
          runCommand
          stdenv
          ;
      };
      # kea.nix and frr.nix wrap nixpkgs's packages, whose names inside the scope are their own,
      # so they are passed nixpkgs's explicitly.
      kea = self.callPackage ../packages/kea.nix { inherit (final) kea; };
      frr = self.callPackage ../packages/frr.nix { inherit (final) frr; };
    }
  );
}
