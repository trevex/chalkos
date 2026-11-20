# chalkctl, the operators' command.
{
  buildGoProgram,
  treeGoPaths,
}:
buildGoProgram {
  pname = "chalkctl";
  paths = treeGoPaths;
}
