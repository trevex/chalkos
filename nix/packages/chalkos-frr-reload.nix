# chalkos-frr-reload, which frr.service runs to apply a changed frr.conf without a restart. Images
# that run FRR carry it, so it is stripped.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-frr-reload";
  paths = [
    ../../cmd/chalkos-frr-reload
    ../../pkg/frr
    ../../pkg/tool
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
