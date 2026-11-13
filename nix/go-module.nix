# Builds Go programs of this repository. They share go.mod and go.sum; with proxyVendor the
# module download depends on those two files alone, so one vendorHash serves programs built from
# different parts of the source. A program is built from cmd/<pname> unless subPackages says
# otherwise, and without its tests, which the go-unit check runs. Unless it runs or builds tests,
# its source leaves out the _test.go files and testdata directories, so a change to a test alone
# rebuilds no program and no image. The Go toolchain is chalkosGo of nix/go.nix's overlay.
{
  lib,
  buildGoModule,
  chalkosGo,
}:
{
  pname,
  # Source paths besides go.mod and go.sum.
  paths,
  doCheck ? false,
  # Whether the source keeps the tests, for a program that runs or builds them.
  withTests ? doCheck,
  ...
}@args:
let
  # The testdata directories below a source path.
  testdata =
    dir:
    lib.concatLists (
      lib.mapAttrsToList (
        name: type:
        if type != "directory" then
          [ ]
        else if name == "testdata" then
          [ (dir + "/${name}") ]
        else
          testdata (dir + "/${name}")
      ) (builtins.readDir dir)
    );
  tests = lib.fileset.unions (
    [ (lib.fileset.fileFilter (f: lib.hasSuffix "_test.go" f.name) ../.) ]
    ++ lib.concatMap testdata (lib.filter (p: lib.isPath p && lib.pathIsDirectory p) paths)
  );
  sources = lib.fileset.unions (
    [
      ../go.mod
      ../go.sum
    ]
    ++ paths
  );
in
(buildGoModule.override { go = chalkosGo; }) (
  {
    version = "0.1.0";
    src = lib.fileset.toSource {
      root = ../.;
      fileset = if withTests then sources else lib.fileset.difference sources tests;
    };
    proxyVendor = true;
    # After changing go.mod or go.sum, set this to lib.fakeHash, run `nix build .#chalkctl`, and
    # copy the hash it reports.
    vendorHash = "sha256-FxJji46/hz2knQAzPQ+G2P2THt1xYvBKkwI/aIWFQxc=";
    env.CGO_ENABLED = "0";
    subPackages = [ "cmd/${pname}" ];
    inherit doCheck;
    meta.mainProgram = pname;
    # The source paths, relative to the repository, which the go-lint check compares with the
    # packages the program imports.
    passthru.goPaths = map (p: lib.path.removePrefix ../. p) (lib.filter lib.isPath paths);
  }
  // removeAttrs args [
    "paths"
    "withTests"
  ]
)
