# The overlay that adds chalkosGo, the Go toolchain that builds, tests and lints chalkos's Go code:
# go-overlay's build of the Go release that go.mod's go directive names. Updating Go is a change
# to that line. The images apply the overlay to their own nixpkgs, so images built from another
# nixpkgs still build chalkos's programs with this Go.
{ go-overlay }:
final: _: {
  chalkosGo = (go-overlay.lib.mkGoBin final).fromGoModStrict ../go.mod;
}
