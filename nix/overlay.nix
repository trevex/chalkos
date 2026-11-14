# The overlay that adds pkgs.chalkos, the scope of chalkos's packages: the Go toolchain, go-overlay's
# build of the Go release that go.mod's go directive names; buildGoProgram, which builds a Go
# program of this repository with it; and chalkos's programs and tools. Updating Go is a change to
# go.mod's go line. mkImages applies the overlay to the images' own nixpkgs, so images built from
# another nixpkgs still build chalkos's programs with this Go. A package of the scope that takes an
# argument named as another of its packages gets that package; kea.nix takes nixpkgs's kea, so it
# is passed explicitly.
{ go-overlay }:
final: _: {
  chalkos = final.lib.makeScope final.newScope (self: {
    go = (go-overlay.lib.mkGoBin final).fromGoModStrict ../go.mod;
    buildGoProgram = self.callPackage ./build-go-program.nix { };
    # The whole tree's source paths, which chalkctl, chalklab, the e2e tests and the go-unit and
    # go-lint checks build from; a program's passthru.goPaths lists its own paths instead.
    treeGoPaths = import ./go-paths.nix { inherit (final) lib; };

    chalkbuild = self.callPackage ./chalkbuild.nix { };
    chalkcfg = self.callPackage ./chalkcfg.nix { };
    chalkctl = self.callPackage ./chalkctl.nix { };
    chalkd = self.callPackage ./chalkd.nix { };
    chalklab = self.callPackage ./chalklab.nix { };
    chalklab-e2e = self.callPackage ./chalklab-e2e.nix { };
    chalkos-console = self.callPackage ./chalkos-console.nix { };
    chalkos-networkd-schema = self.callPackage ./chalkos-networkd-schema.nix { };
    chalkos-render = self.callPackage ./chalkos-render.nix { };
    chalkos-rules = self.callPackage ./chalkos-rules.nix { };
    chalkos-storage = self.callPackage ./chalkos-storage.nix { };
    docgen = self.callPackage ./docgen.nix { };
    image-size = self.callPackage ./image-size.nix { };
    iproute2-ip = self.callPackage ./iproute2-ip.nix { };
    kea = self.callPackage ./kea.nix { inherit (final) kea; };
    kernel-modules = self.callPackage ./kernel-modules.nix { };
    relabel-partitions = self.callPackage ./relabel-partitions.nix { };
    veritysize = self.callPackage ./veritysize.nix { };
  });
}
