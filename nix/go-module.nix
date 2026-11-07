# Builds Go programs of this repository. They share go.mod and go.sum; with proxyVendor the
# module download depends on those two files alone, so one vendorHash serves programs built from
# different parts of the source. A program is built from cmd/<pname> unless subPackages says
# otherwise, and without its tests, which the go-unit check runs. Unless it runs or builds tests,
# its source leaves out the _test.go files and testdata directories, so a change to a test alone
# rebuilds no program and no image.
{ lib, buildGoModule }:
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
buildGoModule (
  {
    version = "0.1.0";
    src = lib.fileset.toSource {
      root = ../.;
      fileset = if withTests then sources else lib.fileset.difference sources tests;
    };
    proxyVendor = true;
    # After changing go.mod or go.sum, set this to lib.fakeHash, run `nix build .#chalkctl`, and
    # copy the hash it reports.
    vendorHash = "sha256-wDc7+u9ue31pott6agQnX6I+AJH6wsQYNG+ZUdu6zzU=";
    env.CGO_ENABLED = "0";
    subPackages = [ "cmd/${pname}" ];
    inherit doCheck;
    meta.mainProgram = pname;
  }
  // removeAttrs args [
    "paths"
    "withTests"
  ]
)
