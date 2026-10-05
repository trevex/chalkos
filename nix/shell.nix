{ pkgs, self }:
let
  testEnv = import ./testing/env.nix { inherit pkgs self; };
in
pkgs.mkShell (
  {
    packages =
      testEnv.tools
      ++ (with pkgs; [
        go
        gopls
        openssl
        python3Packages.virt-firmware
        jq
        nixfmt
      ]);
  }
  // testEnv.vars
)
