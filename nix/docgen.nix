# docgen, which writes the documentation pages generated from the code.
{ buildGoProgram }:
buildGoProgram {
  pname = "docgen";
  paths = [
    ../cmd
    ../pkg
  ];
}
