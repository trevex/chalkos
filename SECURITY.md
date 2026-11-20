# Security policy

This file says which releases of chalkos receive security fixes, how to report a vulnerability,
and what the maintainers do with a report. The
[Security](https://trevex.github.io/chalkos/get-started/security/) page of the documentation shows
it too, with how the project scans its code and images.

<!-- --8<-- [start:policy] -->
## Supported versions

Before 1.0, the project supports the latest release alone. A fix ships in a new release and on
`main`; earlier releases receive none, so upgrade to the release that fixes a vulnerability.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Earlier releases | No |

## Report a vulnerability

Report a vulnerability privately through GitHub's private vulnerability reporting: open
[a new report](https://github.com/trevex/chalkos/security/advisories/new), or select Report a
vulnerability on the repository's Security tab. Only the maintainers and the reporter see the
report. Never report a vulnerability in a public issue, pull request or discussion.

A useful report names:

- the release, image or commit that has the vulnerability, and the platform, `metal` or `kvm`
- the component, such as chalkd, chalkctl, an image's configuration or the installer
- the steps that reproduce it, with the inventory or node config that shows it where possible
- what an attacker gains, and from where: the network, a node, a workstation or a cluster's
  workloads

## What happens after a report

The maintainers aim to:

1. acknowledge the report within 7 days
2. assess it within 14 days: whether it is a vulnerability in chalkos, how severe it is, and which
   releases it affects
3. release a fix and disclose the vulnerability in coordination with the reporter, normally within
   90 days of the report

The report's private advisory carries the discussion, and the reporter hears of each step there.
A vulnerability that is already public or exploited gets a fix sooner. The maintainers publish a
GitHub Security Advisory with the release that fixes the vulnerability, credit the reporter unless
they decline, and request a CVE for it when the vulnerability warrants one.

## Scope

The policy covers:

- the code of this repository: chalkd, chalkctl, chalkbuild, chalklab, the NixOS modules and the
  Nix library that builds images
- what a release publishes: the images at `ghcr.io/trevex/chalkos`, the installer, and the
  chalkctl and chalkbuild downloads
- the configuration that chalkos generates for the components it runs, such as the kubelet, etcd,
  FRR or the firewall

A vulnerability in an upstream component, such as the Linux kernel, systemd, Kubernetes, a
nixpkgs package or a Go module, belongs to its upstream project: report it there too. chalkos
ships the fix in a release once nixpkgs or the module has it. How chalkos configures or exposes an
upstream component is in scope.

## Verify a release

The release workflow signs each image and download of a release with cosign, keyless, and attests
its provenance. [Releases](https://trevex.github.io/chalkos/get-started/releases/) shows how to
check an image or a download before you use it.
<!-- --8<-- [end:policy] -->
