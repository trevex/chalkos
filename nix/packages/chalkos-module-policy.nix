# chalkos-module-policy, statically linked because it runs in the initrd.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-module-policy";
  paths = [
    ../../cmd/chalkos-module-policy
    ../../pkg/modulepolicy
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
