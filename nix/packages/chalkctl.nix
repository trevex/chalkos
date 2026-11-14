# chalkctl, the operators' command.
{
  buildGoProgram,
  lib,
  makeWrapper,
  mtools,
  treeGoPaths,
}:
buildGoProgram {
  pname = "chalkctl";
  paths = treeGoPaths;
  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/chalkctl --prefix PATH : ${lib.makeBinPath [ mtools ]}
  '';
}
