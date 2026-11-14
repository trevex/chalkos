# chalkctl, the operators' command. Its tests run as it is built.
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
  doCheck = true;
  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/chalkctl --prefix PATH : ${lib.makeBinPath [ mtools ]}
  '';
}
