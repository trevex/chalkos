{ pkgs, self }:
let
  testEnv = import ./testing/env.nix { inherit pkgs self; };
in
pkgs.mkShell (
  {
    packages =
      testEnv.tools
      ++ (with pkgs; [
        chalkos.go
        chalkos.go.tools.gopls.latest
        buf
        protoc-gen-go
        protoc-gen-connect-go
        protoc-gen-doc
        openssl
        python3Packages.virt-firmware
        jq
        nixfmt
        chalkos.go.tools.staticcheck.latest
      ]);
  }
  // testEnv.vars
)
