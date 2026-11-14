# veritysize, which the image-size check uses to measure an image's store and hash tree the way
# package verity does.
{ buildGoProgram }:
buildGoProgram {
  pname = "veritysize";
  paths = [
    ../test/veritysize
    ../pkg/verity
  ];
  subPackages = [ "test/veritysize" ];
}
