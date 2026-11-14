# Generates the networkd kinds' spec schemas from systemd's gperf tables while images are built.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-networkd-schema";
  paths = [
    ../cmd/chalkos-networkd-schema
    ../pkg/networkdschema
  ];
}
