# What the flake checks share: the test images and their environment, and the builders of the
# checks that run a node's units, validate a directory of YAML or report evaluation tests.
{
  pkgs,
  self,
  testing,
}:
let
  inherit (pkgs) lib;
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  # The Kubernetes test image, whose units the unit checks run.
  worker = testing.nixos.k8s-kvm;
  # A router image, evaluated alone, whose rules, outputs and kernel modules the rules check
  # checks.
  router =
    (self.lib.mkImages {
      platforms = [ "metal" ];
      variants.router = {
        kubernetes = false;
        router = true;
      };
    }).nixos.router-metal;

  # Runs the script as root of user and network namespaces of its own. In there:
  # - run/ and var/ of the build directory are /run and /var;
  # - /etc holds the worker's schemas and the root user alone, nothing of the host's.
  # The units' scripts so run as on a node, and their chalkcfg reads the documents the script
  # writes below /run/chalkos/config.
  onNode = script: ''
    mkdir -p run/chalkos/config var etc/chalkos
    echo 'root:x:0:0:root:/:/bin/sh' >etc/passwd
    echo 'root:x:0:' >etc/group
    printf '127.0.0.1 localhost\n::1 localhost\n' >etc/hosts
    ln -s ${worker.config.environment.etc."chalkos/schemas".source} etc/chalkos/schemas
    bwrap --unshare-user --uid 0 --gid 0 --unshare-net --cap-add ALL \
      --ro-bind /nix /nix --ro-bind /bin /bin --dev /dev --bind /proc /proc --tmpfs /tmp \
      --ro-bind "$PWD/etc" /etc --bind "$PWD/run" /run --bind "$PWD/var" /var \
      --bind "$PWD" "$PWD" --chdir "$PWD" ${lib.getExe script}
  '';
in
{
  inherit
    chalkPkgs
    router
    testing
    worker
    ;
  testEnv = import ../testing/env.nix { inherit pkgs self; };

  # The script NixOS runs to load and reload the worker's firewall.
  firewall =
    lib.findFirst (lib.hasSuffix "-nftables-rules")
      (throw "the worker's nftables.service runs no nftables-rules script")
      worker.config.systemd.services.nftables.serviceConfig.ExecStart;

  # Tests skip when a tool or variable is missing, so a check must not pass on skipped or zero tests.
  runTests = command: ''
    set -o pipefail
    ${command} 2>&1 | tee "$TMPDIR/test.log"
    if grep -q -- '--- SKIP' "$TMPDIR/test.log"; then
      echo "error: a test was skipped" >&2
      exit 1
    fi
    if ! grep -q -- '--- PASS' "$TMPDIR/test.log"; then
      echo "error: no test ran" >&2
      exit 1
    fi
  '';

  # A check that runs the test script as on a node, with the tools given on its PATH. The script
  # is shellchecked, and runs with errexit, nounset and pipefail.
  unitCheck =
    name: runtimeInputs: text:
    pkgs.runCommand "chalkos-${name}" { nativeBuildInputs = [ pkgs.bubblewrap ]; } ''
      ${onNode (
        pkgs.writeShellApplication {
          name = "chalkos-${name}-test";
          inherit runtimeInputs text;
        }
      )}
      touch $out
    '';

  # The tools of the unit checks that send packets between network namespaces.
  networkTools = [
    pkgs.nftables
    pkgs.iproute2
    pkgs.util-linux
    pkgs.socat
  ];

  # Makes this network namespace the node and gives a peer one of its own, linked over a veth
  # pair. The node's v0 holds 10.0.0.11/24 and fd00::11/64, the peer's v1 10.0.0.12/24 and
  # fd00::12/64. on_peer runs a command in the peer's namespace.
  peerNamespace = ''
    ip link set lo up
    unshare -n sh -c 'touch peer-ready; exec sleep 600' &
    peer=$!
    until [ -e peer-ready ]; do sleep 0.1; done
    on_peer() { nsenter -t "$peer" -n "$@"; }
    ip link add v0 type veth peer name v1 netns "$peer"
    ip addr add 10.0.0.11/24 dev v0
    ip addr add fd00::11/64 dev v0 nodad
    ip link set v0 up
    on_peer ip link set lo up
    on_peer ip addr add 10.0.0.12/24 dev v1
    on_peer ip addr add fd00::12/64 dev v1 nodad
    on_peer ip link set v1 up
  '';

  # A check that copies the directory, links the image directories given below it by their
  # relative paths, and runs the command in it; the command is chalkctl validate unless given.
  validateDirectory =
    {
      name,
      src,
      images,
      command ? "chalkctl validate",
    }:
    pkgs.runCommand "chalkos-${name}" { nativeBuildInputs = [ chalkPkgs.chalkctl ]; } ''
      cp -r ${src} tree
      chmod -R u+w tree
      ${lib.concatStrings (
        lib.mapAttrsToList (link: image: ''
          mkdir -p "$(dirname tree/${link})"
          ln -s ${image} tree/${link}
        '') images
      )}
      cd tree
      ${command}
      touch $out
    '';

  # A check of lib.runTests failures: it builds when there are none, and fails to evaluate with
  # them otherwise.
  evalTests =
    name: description: failures:
    if failures == [ ] then
      pkgs.runCommand "chalkos-${name}-tests" { } "touch $out"
    else
      throw "${description} tests failed:\n${lib.generators.toPretty { } failures}";
}
