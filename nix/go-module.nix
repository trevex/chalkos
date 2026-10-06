# Builds Go programs of this repository. They share go.mod and go.sum; with proxyVendor the
# module download depends on those two files alone, so one vendorHash serves programs built from
# different parts of the source.
{ lib, buildGoModule }:
{
  pname,
  # Source paths besides go.mod and go.sum.
  paths,
  ...
}@args:
buildGoModule (
  {
    version = "0.1.0";
    src = lib.fileset.toSource {
      root = ../.;
      fileset = lib.fileset.unions (
        [
          ../go.mod
          ../go.sum
        ]
        ++ paths
      );
    };
    proxyVendor = true;
    # After changing go.mod or go.sum, set this to lib.fakeHash, run `nix build .#chalkctl`, and
    # copy the hash it reports.
    vendorHash = "sha256-i1kT/Y+xYpcfsjwgJ//TIlHSLO9wQbaZwxk6QaDxHYE=";
    env.CGO_ENABLED = "0";
  }
  // removeAttrs args [ "paths" ]
)
