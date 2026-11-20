# Builds Go programs of this repository. They share go.mod and go.sum; with proxyVendor the
# module download depends on those two files alone, so one vendorHash serves programs built from
# different parts of the source. A program is built from cmd/<pname> unless subPackages says
# otherwise, and without its tests, which the go-unit check runs. Unless it runs or builds tests,
# its source leaves out the _test.go files and testdata directories, so a change to a test alone
# rebuilds no program and no image. The Go toolchain is go of the chalkos scope (default.nix).
# Every program is of chalkos's version, from version.nix, which the linker sets in pkg/version;
# the flake's packages of chalkctl and chalkbuild set the git revision there too.
{
  buildGoModule,
  cacert,
  gitMinimal,
  go,
  lib,
}:
{
  pname,
  # Source paths besides go.mod and go.sum.
  paths,
  # Whether the source keeps the tests, for a program that runs or builds them.
  withTests ? false,
  # Flags of the linker besides the one that sets the version.
  ldflags ? [ ],
  ...
}@args:
let
  version = import ../../version.nix;
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
    [ (lib.fileset.fileFilter (f: lib.hasSuffix "_test.go" f.name) ../../.) ]
    ++ lib.concatMap testdata (lib.filter (p: lib.isPath p && lib.pathIsDirectory p) paths)
  );
  sources = lib.fileset.unions (
    [
      ../../go.mod
      ../../go.sum
    ]
    ++ paths
  );
in
(buildGoModule.override { inherit go; }) (
  {
    inherit version;
    ldflags = [ "-X github.com/trevex/chalkos/pkg/version.Version=${version}" ] ++ ldflags;
    src = lib.fileset.toSource {
      root = ../../.;
      fileset = if withTests then sources else lib.fileset.difference sources tests;
    };
    proxyVendor = true;
    # After a change of go.mod or go.sum, `nix run .#update-vendor-hash` sets it again: it builds
    # the module download with lib.fakeHash and writes back the hash the build reports.
    vendorHash = "sha256-qv2tcw2lnnwkfZRZCLLSf+cAGR/KR9wDvZu+KAbpE+E=";
    # Every program shares one module download: the same name, source and inputs make one
    # derivation, which a build fetches once.
    overrideModAttrs = _: _: {
      name = "chalkos-go-modules";
      src = lib.fileset.toSource {
        root = ../../.;
        fileset = lib.fileset.unions [
          ../../go.mod
          ../../go.sum
        ];
      };
      nativeBuildInputs = [
        go
        gitMinimal
        cacert
      ];
      preBuild = "";
      env = { };
    };
    env.CGO_ENABLED = "0";
    subPackages = [ "cmd/${pname}" ];
    doCheck = false;
    meta.mainProgram = pname;
    # The source paths, relative to the repository, which the go-lint check compares with the
    # packages the program imports.
    passthru.goPaths = map (p: lib.path.removePrefix ../../. p) (lib.filter lib.isPath paths);
  }
  // removeAttrs args [
    "ldflags"
    "paths"
    "withTests"
  ]
)
