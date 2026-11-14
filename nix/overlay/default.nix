# The overlay that adds pkgs.chalkos, the scope of chalkos's packages: the Go toolchain, go-overlay's
# build of the Go release that go.mod's go directive names; buildGoProgram, which builds a Go
# program of this repository with it; and chalkos's programs and tools, a file each in
# nix/packages, named as the package. Updating Go is a change to go.mod's go line. mkImages applies
# the overlay to the images' own nixpkgs, so images built from another nixpkgs still build
# chalkos's programs with this Go. A package of the scope that takes an argument named as another
# of its packages gets that package.
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
      # kea.nix wraps nixpkgs's kea, whose name inside the scope is kea.nix's own package, so it is
      # passed nixpkgs's explicitly.
      kea = self.callPackage ../packages/kea.nix { inherit (final) kea; };
    }
  );
}
