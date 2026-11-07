# Generates the networkd kinds' spec schemas from systemd's gperf tables while images are built.
{ callPackage }:
callPackage ./go-module.nix { } {
  pname = "chalkos-networkd-schema";
  paths = [
    ../cmd/chalkos-networkd-schema
    ../pkg/networkdschema
  ];
}
