# The checker of an image that does not run, in the sandbox chalkd runs it in, on a NixOS VM with
# systemd: TestSandboxOnANode of pkg/imagecheck. The image is a second store, a directory laid
# out as nix/store and bound over the VM's own: chalkd's closure as it is, and the image's system,
# renderer and command fact at store paths the VM's store lacks, so the check passes only when
# the checker, its renderer and the fact's program come from the bound store. The image's Demo
# kind reads the fact in a rule; its validator refuses a Demo called veto, and its output's check
# one called refuse; both check the sandbox: no root, /run/chalkos out of reach, and the check
# no capabilities and nft --check in a user and network namespace of its own. While the checker
# waits for its fact, another user of the VM reaches neither the facts socket nor the input or
# the result.
{
  pkgs,
  lib,
  checkLib,
  ...
}:
let
  inherit (checkLib.chalkPkgs) chalkd;
  # pkg/imagecheck's tests as one binary.
  tests = (chalkd.override { withTests = true; }).overrideAttrs {
    pname = "chalkos-imagecheck-tests";
    buildPhase = ''
      runHook preBuild
      go test -c -tags vmtest -o imagecheck.test ./pkg/imagecheck
      runHook postBuild
    '';
    installPhase = "install -D imagecheck.test $out/bin/imagecheck.test";
    postFixup = "";
  };
  fake = digit: name: "/nix/store/${lib.fixedWidthString 32 digit ""}-${name}";
  system = fake "0" "image-system";
  render = fake "1" "demo-render";
  fact = fake "2" "probe-fact";
  tools = lib.makeBinPath [
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.util-linux
  ];
  closure = pkgs.closureInfo {
    rootPaths = [
      chalkd
      pkgs.bash
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.nftables
      pkgs.util-linux
    ];
  };
  outputs = builtins.toJSON {
    demo = {
      inputs = [ "Demo" ];
      render = "${render}/bin/render";
      check = "${render}/bin/check";
      dir = "/run/demo";
      user = "root";
      group = "root";
      mode = "0644";
    };
  };
  validators = builtins.toJSON {
    demo = {
      inputs = [ "Demo" ];
      program = "${render}/bin/validate";
    };
  };
  secondStore =
    pkgs.runCommand "chalkos-second-store"
      {
        nativeBuildInputs = [ pkgs.jq ];
      }
      ''
        store=$out/nix/store
        mkdir -p $store
        xargs -a ${closure}/store-paths cp -a -t $store
        mkdir -p $out${system}/sw/bin $out${system}/etc/chalkos/schemas $out${render}/bin $out${fact}/bin
        ln -s ${lib.getExe chalkd} $out${system}/sw/bin/chalkd
        cp ${../../../test/fixtures/kinds/builtin}/*.json $out${system}/etc/chalkos/schemas/
        jq '.properties.spec.properties.name["x-chalkos-rules"] = [{"rule": "facts.probe == \"image\"", "message": "the probe fact is not the image'"'"'s"}]' \
          ${../../../test/fixtures/kinds/image/schemas/demo.example.org_v1alpha1_Demo.json} >$out${system}/etc/chalkos/schemas/demo.example.org_v1alpha1_Demo.json
        echo '${outputs}' >$out${system}/etc/chalkos/outputs.json
        echo '${validators}' >$out${system}/etc/chalkos/validators.json
        echo '{"probe": {"command": "${fact}/bin/probe"}}' >$out${system}/etc/chalkos/facts.json
        cat >$out${render}/bin/render <<'EOF'
        #!${pkgs.bash}/bin/bash
        set -e
        PATH=${tools}
        cat >"$OUT/demo.json"
        EOF
        cat >$out${render}/bin/check <<'EOF'
        #!${pkgs.bash}/bin/bash
        set -e
        PATH=${tools}
        [ "$(id -u)" -ne 0 ]
        grep -q '^CapEff:[[:space:]]*0*$' /proc/self/status
        if cat /run/chalkos/secret 2>/dev/null; then exit 2; fi
        echo "table inet t { }" | unshare --map-root-user --net ${pkgs.nftables}/bin/nft --check --file -
        ! grep -q '"refuse"' "$OUT/demo.json"
        EOF
        cat >$out${render}/bin/validate <<'EOF'
        #!${pkgs.bash}/bin/bash
        set -e
        PATH=${tools}
        [ "$*" = "validate demo" ]
        [ "$(id -u)" -ne 0 ]
        if cat /run/chalkos/secret 2>/dev/null; then exit 2; fi
        if grep -q '"veto"'; then
          echo '{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "the name is vetoed"}]}'
        fi
        EOF
        cat >$out${fact}/bin/probe <<'EOF'
        #!${pkgs.bash}/bin/bash
        echo '"image"'
        EOF
        chmod +x $out${render}/bin/* $out${fact}/bin/*
      '';
in
pkgs.testers.runNixOSTest {
  name = "chalkos-image-checker";
  nodes.machine.virtualisation.memorySize = 1024;
  testScript = ''
    machine.wait_for_unit("multi-user.target")
    machine.succeed("mkdir -p /run/chalkos && echo hidden >/run/chalkos/secret")
    # Outside the VM's /nix/store, as chalkd mounts an image's store.
    machine.succeed("mkdir /image-store && mount --bind ${secondStore}/nix/store /image-store")
    status, out = machine.execute(
      "CHALKOS_IMAGECHECK_STORE=/image-store CHALKOS_IMAGECHECK_SYSTEM=${system} "
      + "${tests}/bin/imagecheck.test -test.v -test.run '^TestSandboxOnANode$' 2>&1"
    )
    print(out)
    if status != 0 or "--- PASS: TestSandboxOnANode" not in out:
      print(machine.execute("journalctl -b --no-pager -u chalkos-check.service")[1])
      raise Exception("TestSandboxOnANode did not pass")
  '';
}
