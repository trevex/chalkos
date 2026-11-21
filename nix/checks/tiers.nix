# The tiers CI runs the flake's checks in, by name. Fast checks build no image and run on every
# ready pull request and push to main, as one job; image checks build the images other checks
# share, fetch the test registry's images or boot a NixOS test VM, and run there too, one job
# each; end-to-end checks boot the test images or build images of their own, one job each,
# nightly and on a ready pull request by its labels. e2eLabels maps each label that selects
# end-to-end checks to its checks: e2e:<name> to e2e-<name>, e2e:examples to the examples'
# checks, and e2e:release to the checks that build the release images, one job each. Every check
# is in exactly one tier, and every name a tier lists is a check. lint names the fast checks that
# a draft pull request runs in place of the tiers, in one job.
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
      "renovate"
      "release-eval"
      "router-dhcp"
      "router-dhcp-relay"
      "router-dhcp6"
      "router-firewall"
      "router-nat64"
      "router-policy-routing"
      "router-vrf"
      "security-insights"
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
      "no-shell"
      "no-shell-calls"
      "rules"
      "sbom"
      "template-cluster-metal"
      "template-lab"
      "template-router-lab"
    ];
    e2e = lib.concatLists (lib.attrValues e2eLabels);
  };
  lint = [
    "ci-tiers"
    "go-lint"
    "nix-lint"
    "renovate"
    "workflows"
  ];
  notFast = lib.subtractLists tiers.fast lint;
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
assert lib.assertMsg (
  notFast == [ ]
) "nix/checks/tiers.nix lists these lint checks, which are no fast checks: ${toString notFast}";
tiers // { inherit e2eLabels lint; }
