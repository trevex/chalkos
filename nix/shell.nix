{ pkgs, self }:
let
  testEnv = import ./testing/env.nix { inherit pkgs self; };
in
pkgs.mkShell (
  {
    packages =
      testEnv.tools
      ++ (with pkgs; [
        chalkosGo
        chalkosGo.tools.gopls.latest
        buf
        protoc-gen-go
        protoc-gen-connect-go
        protoc-gen-doc
        openssl
        python3Packages.virt-firmware
        jq
        nixfmt
        chalkosGo.tools.staticcheck.latest
      ]);
  }
  // testEnv.vars
)
