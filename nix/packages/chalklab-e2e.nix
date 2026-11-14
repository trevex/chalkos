# chalklab-e2e, the e2e tests as one program.
{ buildGoProgram, treeGoPaths }:
buildGoProgram {
  pname = "chalklab-e2e";
  paths = treeGoPaths;
  withTests = true;
  subPackages = null;
  buildPhase = ''
    runHook preBuild
    go test -c -o chalklab-e2e ./test/e2e
    runHook postBuild
  '';
  installPhase = ''
    runHook preInstall
    install -Dm755 chalklab-e2e $out/bin/chalklab-e2e
    runHook postInstall
  '';
}
