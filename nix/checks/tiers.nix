# The tiers CI runs the flake's checks in, by name. Fast checks build no image and run on every
# pull request and push to main, as one job; image checks build images, fetch the test registry's
# images or boot a NixOS test VM, and run there too, one job each; end-to-end checks boot the
# test images, one job each, on a push to main, nightly, and on a pull request by its labels.
# Every check is in exactly one tier, and every name a tier lists is a check.
{ lib, names }:
let
  tiers = {
    fast = [
      "api-generated"
      "chrony-dhcp"
      "ci-tiers"
      "containerd-config"
      "controlplane-ports"
      "docs"
      "docs-generated"
      "eval"
      "eval-kinds"
      "frr-credentials"
      "go-lint"
      "go-unit"
      "kubernetes-bgp-nht"
      "nix-lint"
      "registry-mirrors"
      "router-dhcp"
      "router-dhcp-relay"
      "router-dhcp6"
      "router-firewall"
      "router-nat64"
      "router-policy-routing"
      "router-vrf"
      "vxlan-rule"
      "workflows"
    ];
    images = [
      "bootspec"
      "example-bgp-extension"
      "example-bgp-extension-validator-docs"
      "example-extensions"
      "example-homelab"
      "fixtures"
      "frr-reload"
      "image-checker"
      "image-contract"
      "image-oci"
      "image-size"
      "image-tests"
      "kernel-modules"
      "rules"
      "template-cluster-metal"
      "template-lab"
      "template-router-lab"
    ];
    e2e = lib.filter (lib.hasPrefix "e2e-") names;
  };
  listed = lib.concatLists (lib.attrValues tiers);
  unlisted = lib.subtractLists listed names;
  unknown = lib.subtractLists names listed;
  twice = lib.filter (name: lib.count (n: n == name) listed > 1) (lib.unique listed);
in
assert lib.assertMsg (
  unlisted == [ ]
) "nix/checks/tiers.nix puts these checks in no tier: ${toString unlisted}";
assert lib.assertMsg (
  unknown == [ ]
) "nix/checks/tiers.nix lists these names, which are no checks: ${toString unknown}";
assert lib.assertMsg (
  twice == [ ]
) "nix/checks/tiers.nix puts these checks in more than one tier: ${toString twice}";
tiers
