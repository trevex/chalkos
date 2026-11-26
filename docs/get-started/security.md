---
title: "Security policy"
description: "Which chalkos releases receive security fixes, how to report a vulnerability, and how the project scans its code and images"
---

# Security policy

A chalkos node guarantees the following, as the [security model](../how-it-works/node/security.md)
explains with what each attacker position reaches:

- With Secure Boot on, the firmware boots only images signed with the cluster's db key, and
  dm-verity checks every block of the store.
- Nothing runs that the image did not bring: there is no shell, every writable file system is
  mounted noexec, and kernel modules load only from the signed image.
- The TPM releases the disk keys only under the Secure Boot state the node was installed under.
- Nobody logs in: chalkd serves the only API, over mutual TLS, and checks the role of every call.
- The keys that sign images and issue certificates never reach a node.

This page is for operators who run chalkos and for anyone who finds a vulnerability in it. It holds
the project's security policy, which `SECURITY.md` in the repository also holds, and describes how
chalkos scans its code and its release images for known vulnerabilities.

--8<-- "SECURITY.md:policy"

## How chalkos scans for vulnerabilities

Three tools look for known vulnerabilities, each in a different part of what a release ships:

| Tool | Scans | When | Result |
| --- | --- | --- | --- |
| [govulncheck](https://go.dev/doc/security/vuln/) | The Go code against the Go vulnerability database | Every pull request, every push to `main` and every night | Fails CI when the code calls a function with a known vulnerability |
| [vulnix](https://github.com/nix-community/vulnix) | The NixOS system of each release image against the CVEs of the National Vulnerability Database | Every night | A report in the nightly run, which never fails it, and an issue labelled `security` for a new match with a CVSS v3 score of 7 or more |
| [Renovate](https://docs.renovatebot.com/) | The Go modules and the GitHub Actions against the GitHub Advisory Database and OSV | Continuously | A pull request labelled `security` per fixed version, outside the weekly schedule |

govulncheck checks reachability: a vulnerable function in a module that chalkos requires but never
calls does not fail CI. Run it on a working tree with `nix run .#govulncheck`.

vulnix matches package names and versions, so some of its matches do not apply, for example when
nixpkgs patches the fix into a package without changing its version.
`.github/vulnix-whitelist.toml` records each such match with its reason. A real match in a nixpkgs
package is fixed by updating nixpkgs, which the `update-nixpkgs` workflow does each week.

The nightly run reports the matches with a CVSS v3 score of 7 or more that the whitelist does not
hold in an issue labelled `security`, titled "vulnix matches high-score CVEs in the release
images". The issue lists each match by image, package and CVE, with its score. A later run that
finds a match the issue does not list updates the list and comments on the issue; a run with no
new match leaves it alone. No run closes the issue. Closing it acknowledges the matches it lists,
and the next new match opens another. A match without a CVSS v3 score opens no issue and shows in
the run's summary alone.

## Software bills of materials

Each release publishes a software bill of materials (SBOM) for every image, in SPDX 2.3 JSON, and
for chalkctl and chalkbuild, in CycloneDX JSON. An image's SBOM lists the packages of its NixOS
system and its kernel with their versions, licences and sources; a program's lists the Go modules
in its binary. The SBOMs are release assets, signed like the others, and attestations on the
images and the binaries, so a scanner can read what a release ships without building it.
[Fetch and verify an SBOM](releases.md#fetch-and-verify-an-sbom) shows how to download and check
one.

## OpenSSF Scorecard

The [OpenSSF Scorecard](https://scorecard.dev/) checks the repository's supply-chain practices
each week and on each push to `main`: branch protection, pinned dependencies, signed releases,
token permissions of the workflows and more. The
[results](https://scorecard.dev/viewer/?uri=github.com/trevex/chalkos) are public, and the
README's badge shows the score.

## Security Insights

`security-insights.yml` in the repository's root describes the project's security practices in the
[OpenSSF Security Insights](https://github.com/ossf/security-insights-spec) format, for tools that
read them: how to report a vulnerability, the security tools CI runs, how releases are signed and
attested, where their SBOMs are, and the licence.

## Limits

- govulncheck reads Go code alone. The C and other programs of the images, such as the kernel,
  systemd or FRR, are vulnix's.
- vulnix scans the release images' NixOS systems. An image you build with `mkImages` adds your own
  extensions and packages, which no scan of chalkos covers.
- vulnix knows a package by its name and version, and the National Vulnerability Database by the
  products its entries name, so it misses a vulnerability filed under another product name.
- A vulnerability that nobody has published yet shows in no scan. Report one as
  [Report a vulnerability](#report-a-vulnerability) describes.

To scan an image of your own as the nightly job does, build its system and run vulnix on it, here
for the `node-metal` image of the `cluster-metal` template:

```sh
nix build -o system .#nixosConfigurations.node-metal.config.system.build.toplevel
nix run nixpkgs#vulnix -- --closure system
```
