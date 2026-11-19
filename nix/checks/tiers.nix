# The tiers CI runs the flake's checks in, by name. Fast checks build no image and run on every
# pull request and push to main, as one job; image checks build the images other checks share,
# fetch the test registry's images or boot a NixOS test VM, and run there too, one job each;
# end-to-end checks boot the test images or build images of their own, one job each, on a push
# to main, nightly, and on a pull request by its labels. e2eLabels maps each label that selects
# end-to-end checks to its checks: e2e:<name> to e2e-<name>, e2e:examples to the examples'
# checks, and e2e:release to the checks that build the release images, one job each. Every check is in exactly one tier, and every name a tier lists is a check.
{ lib, names }:
let
  examples = [
    "example-bgp-extension"
    "example-bgp-extension-validator-docs"
    "example-extensions"
    "example-homelab"
  ];
  e2eLabels =
    lib.listToAttrs (
      map (name: lib.nameValuePair ("e2e:" + lib.removePrefix "e2e-" name) [ name ]) (
        lib.filter (lib.hasPrefix "e2e-") names
      )
    )
    // {
      "e2e:examples" = examples;
      "e2e:release" = lib.filter (lib.hasPrefix "release-image-") names;
    };
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
      "release-eval"
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
    e2e = lib.concatLists (lib.attrValues e2eLabels);
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
tiers // { inherit e2eLabels; }
