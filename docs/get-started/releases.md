---
title: "Releases"
description: "What a chalkos release publishes, when to use its generic images, and how to pin and verify them"
---

# Releases

This page is for operators and image authors who take chalkos from a release instead of building
everything themselves. A release is a tag `vX.Y.Z` of the chalkos repository with a GitHub release
of the same name. It publishes images to the GitHub Container Registry, and the installer,
chalkctl and chalkbuild as downloads. Each of them is signed with cosign and has a provenance
attestation, so you can check that the release workflow of chalkos built it.

The [releases page](https://github.com/trevex/chalkos/releases) lists every release with its
changelog, which `CHANGELOG.md` in the repository collects. Before 1.0, a release with a new
feature or a breaking change raises the minor version, and a release of fixes alone raises the
patch version.

## What a release publishes

Each release pushes one image per Kubernetes minor and platform, and one router image per
platform:

| Image | Holds |
| --- | --- |
| `ghcr.io/trevex/chalkos/kubernetes-<minor>-<platform>:<tag>` | The Kubernetes layer with the kubelet of the minor, such as `kubernetes-1.37-metal`. |
| `ghcr.io/trevex/chalkos/router-<platform>:<tag>` | The router layer. |

`<platform>` is `metal` for physical machines or `kvm` for virtual machines, as
[Images and platforms](../build/images-and-platforms.md) describes. `<tag>` is the release's tag,
such as `v0.1.0`. A release builds Kubernetes images for the minors that
`modules/node/kubernetes/releases.nix` marks to publish. A Kubernetes image's own name, which
its os-release records, leaves out the minor: `kubernetes-metal` or `kubernetes-kvm`. So
`chalkctl upgrade --image` finds the nodes that run the image of an earlier minor.

The release's assets are these files:

| Asset | Holds |
| --- | --- |
| `chalkos-installer.iso` | The installer, to boot from a CD, from virtual media or from a USB stick. |
| `chalkos-installer.raw.zst` | The installer as a raw disk image, compressed with zstd. |
| `chalkctl-linux-amd64` | chalkctl for x86-64 Linux, a static binary. |
| `chalkbuild-linux-amd64` | chalkbuild for x86-64 Linux, a static binary. |
| `SHA256SUMS` | The SHA-256 checksum of each file above. |
| `<asset>.sigstore.json` | The cosign signature bundle of each file above and of `SHA256SUMS`. |

The release notes end with the reference of each image by its digest, such as
`ghcr.io/trevex/chalkos/router-metal:v0.1.0@sha256:<digest>`.

## Choose the release's images or your own

The release's images are generic. They suit a cluster whose images need nothing of their own:

- They carry no OS CA. In maintenance mode an image without one accepts any client until the
  node is installed, as
  [Tie the images to one cluster](../build/images-and-platforms.md#tie-the-images-to-one-cluster)
  explains. The installer of the release has none either.
- They carry no Secure Boot signature. Firmware that enforces Secure Boot does not boot them.
- They hold the layers as chalkos ships them, with no extension, package or option of your own.

When the cluster needs any of these, build its images with `mkImages`, as
[Your first image flake](../build/first-image.md) shows, from the chalkos of a release. Pin the
flake's `chalkos` input to the release's tag:

<!-- x-release-please-start-version -->
```nix title="flake.nix"
{
  inputs.chalkos.url = "github:trevex/chalkos/v0.2.1";
  outputs = { chalkos, ... }: { };
}
```
<!-- x-release-please-end -->

The templates of a release name its tag already. Run the chalkctl of the same release as the
images, because before 1.0 the commands and the node API change between releases.

## Name an image by its digest

The inventory names a release's image by its reference in `spec.images`. Copy the reference with
its digest from the release notes:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: cluster}
spec:
  images:
    kubernetes-metal: {source: {oci: "ghcr.io/trevex/chalkos/kubernetes-1.37-metal:vX.Y.Z@sha256:<digest>"}}
  groups:
    nodes: {imageName: kubernetes-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 192.168.1.11, groups: [nodes], files: [nodes/cp1.yaml]}
```

chalkctl reads and the nodes pull the image by the digest, so the inventory keeps naming the same
image even when a tag moves. The tag in the reference only tells a reader which release it is.
[Publish images to a registry](../build/publish-images.md#name-the-digest-in-the-inventory)
describes the `oci` source.

## Verify an image

Check an image's signature with [cosign](https://docs.sigstore.dev/cosign/) before the inventory
names it:

```sh
cosign verify ghcr.io/trevex/chalkos/kubernetes-1.37-metal@sha256:<digest> \
  --certificate-identity https://github.com/trevex/chalkos/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The signature is keyless: cosign checks a certificate that Sigstore issued to the workflow run that
signed the image. The identity is the release workflow, `release.yml`, on `refs/heads/main`,
because a release runs in the workflow that merging the release pull request starts on `main`.
`cosign verify` fails when another workflow, another branch or another repository signed the
digest.

The [GitHub CLI](https://cli.github.com/) checks the image's provenance attestation, which records
the commit and the workflow run that built it:

```sh
gh attestation verify oci://ghcr.io/trevex/chalkos/kubernetes-1.37-metal@sha256:<digest> \
  --repo trevex/chalkos
```

## Download chalkctl and the installer

Download chalkctl, the checksums and the signature bundles from the release, and check each file
against `SHA256SUMS`:

<!-- x-release-please-start-version -->
```sh
release=https://github.com/trevex/chalkos/releases/download/v0.2.1
curl -fL -O "$release/chalkctl-linux-amd64" -O "$release/chalkctl-linux-amd64.sigstore.json" \
  -O "$release/SHA256SUMS" -O "$release/SHA256SUMS.sigstore.json"
sha256sum --check --ignore-missing SHA256SUMS
```
<!-- x-release-please-end -->

`sha256sum` prints `chalkctl-linux-amd64: OK`. Check that the release workflow signed the
checksums and the program:

```sh
for file in SHA256SUMS chalkctl-linux-amd64; do
  cosign verify-blob "$file" --bundle "$file.sigstore.json" \
    --certificate-identity https://github.com/trevex/chalkos/.github/workflows/release.yml@refs/heads/main \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
done
gh attestation verify chalkctl-linux-amd64 --repo trevex/chalkos
```

Install the program as `chalkctl` on the `PATH`. It reads and writes the ESP of an image itself
and runs no other program:

```sh
install -m 0755 chalkctl-linux-amd64 ~/.local/bin/chalkctl
chalkctl version
```

`chalkctl version` prints the release's version and the commit it was built from. chalkbuild
downloads and checks the same way. Like chalkctl, it is a static binary that runs no other
program, `chalkbuild sign` included.

The installer downloads and checks the same way, as `chalkos-installer.iso` or
`chalkos-installer.raw.zst`. Decompress the raw image before writing it to a disk:

```sh
zstd -d chalkos-installer.raw.zst
```

[Install a node](../operate/install.md) boots a machine from the installer. The release's installer
has no OS CA, so it accepts any client until it installs a node; a cluster that needs one builds
its own installer with `mkImages`.

## Check that it worked

`cosign verify` prints the signature's claims as JSON and ends with no error, `cosign verify-blob`
prints `Verified OK`, and `gh attestation verify` prints that the verification succeeded. The
inventory names each image by a digest that `cosign verify` accepted, and `chalkctl validate`
accepts every node's files against it.
