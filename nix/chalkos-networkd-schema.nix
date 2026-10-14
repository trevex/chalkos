# Generates the networkd kinds' spec schemas from systemd's gperf tables while images are built.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-networkd-schema";
  paths = [
    ../cmd/chalkos-networkd-schema
    ../pkg/networkdschema
  ];
  subPackages = [ "cmd/chalkos-networkd-schema" ];
  # The go-unit check runs the tests.
  doCheck = false;
  meta.mainProgram = "chalkos-networkd-schema";
}
