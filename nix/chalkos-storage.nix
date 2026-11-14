# chalkos-storage, statically linked because it runs in the initrd.
{ buildGoProgram }:
buildGoProgram {
  pname = "chalkos-storage";
  paths = [
    ../cmd/chalkos-storage
    ../pkg/atomicfile
    ../pkg/internal/poll
    ../pkg/storage
    ../pkg/tool
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
