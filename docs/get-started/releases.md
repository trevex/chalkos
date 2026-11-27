---
title: "Releases"
description: "What a chalkos release publishes, when to use its generic images, and how to pin and verify them"
---

# Releases

This page is for operators and image authors who take chalkos from a release instead of building
everything themselves. A release is a tag `vX.Y.Z` of the chalkos repository with a GitHub release
of the same name. It publishes images to the GitHub Container Registry, and the installer,
chalkctl and chalkbuild as downloads. Each of them is signed with cosign and has a provenance
attestation, so you can check that the release workflow of chalkos built it. Each image and each
program also has a software bill of materials (SBOM), which lists its components with their
licences.

The [releases page](https://github.com/trevex/chalkos/releases) lists every release with its
changelog, which `CHANGELOG.md` in the repository collects. Before 1.0, a release with a new
feature or a breaking change raises the minor version, and a release of fixes alone raises the
patch version.

## What a release publishes

Each release pushes one image per Kubernetes minor and platform, one router image per platform,
and for each `kvm` image the same image as a disk for KubeVirt:

| Image | Holds |
| --- | --- |
| `ghcr.io/trevex/chalkos/kubernetes-<minor>-<platform>:<tag>` | The Kubernetes layer with the kubelet of the minor, such as `kubernetes-1.37-metal`. |
| `ghcr.io/trevex/chalkos/router-<platform>:<tag>` | The router layer. |
| `ghcr.io/trevex/chalkos/<image>-disk:<tag>` | The disk of a `kvm` image as a KubeVirt containerDisk, such as `kubernetes-1.37-kvm-disk`, as [Run an image in KubeVirt](#run-an-image-in-kubevirt) describes. |

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
| `<image>.spdx.json` | The SBOM of each image, such as `router-metal.spdx.json`, in SPDX 2.3 JSON. |
| `chalkctl-linux-amd64.cdx.json`, `chalkbuild-linux-amd64.cdx.json` | The SBOM of each program, in CycloneDX JSON. |
| `SHA256SUMS` | The SHA-256 checksum of each file above. |
| `<asset>.sigstore.json` | The cosign signature bundle of each file above and of `SHA256SUMS`. |

The release notes end with the reference of each image by its digest, such as
`ghcr.io/trevex/chalkos/router-metal:vX.Y.Z@sha256:<digest>`.

## Use the release's images or your own

The release's images are generic. They carry no OS CA and no Secure Boot signature, and they hold
the layers as chalkos ships them, with no extension, package or option of a cluster's own. The
release's installer has no OS CA either. They suit a lab, and a cluster that needs nothing of its
own. The `lab` and `router-lab` templates run the release's `kvm` images.
[Published images or your own](decisions.md#published-images-or-your-own) weighs the two paths.

A cluster that needs its own images builds them with `mkImages` from the chalkos of a release, and
pushes them to a registry of its own, as
[Release your own images](../guides/release-your-images.md) describes.

Run the chalkctl of the same release as the images, because before 1.0 the commands and the node
API change between releases.

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

## Pull an image into a directory

Nodes that reach no registry take the image from a directory on the workstation instead.
[`chalkctl image pull`](../reference/cli/chalkctl_image_pull.md) writes an image directory, as
`nix build` makes one, and checks every part of the image as a node would:

```sh
chalkctl image pull ghcr.io/trevex/chalkos/kubernetes-1.37-metal:vX.Y.Z@sha256:<digest> images/kubernetes-metal
```

The inventory's `directory` source then names it, such as
`kubernetes-metal: {source: {directory: images/kubernetes-metal}}`, and chalkctl sends the image
to the nodes on an install or an upgrade. `--layout` writes the OCI layout as the registry serves
it instead, for a registry of your own, as
[Bring the images across](../guides/air-gapped.md#bring-the-images-across) shows.

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

## Run an image in KubeVirt

Each `kvm` image has a second repository with `-disk` appended, such as
`ghcr.io/trevex/chalkos/kubernetes-1.37-kvm-disk`, which holds the image's raw disk in the form
KubeVirt and its Containerized Data Importer (CDI) read: a containerDisk, an OCI image whose one
layer holds the disk as `disk/chalkos.qcow2`. A virtual machine boots from it as a node boots
from the installed image, so a machine in KubeVirt needs no installer. The release tags it with
the image's tag and signs it and attests its provenance like the image itself. It has no SBOM of
its own, because it holds the same image, whose SBOM describes it. The release notes list each
disk's digest after its image's.

Two annotations of its manifest name the image the disk holds: `dev.chalkos.image.name`, the
image's name, such as `kubernetes-kvm`, and `dev.chalkos.image.root-hash`, the root hash of its
store, which the image's header records too. The other annotations are those of the image.
[Image layout](../reference/image-layout.md#container-disk) describes the layout.

A DataVolume imports the disk into a persistent volume, so the node keeps its state across
restarts of its virtual machine. CDI's registry source names the disk by its digest alone, since
CDI refuses a reference with both a tag and a digest:

```yaml title="vm.yaml"
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: {name: w1}
spec:
  runStrategy: Always
  dataVolumeTemplates:
    - metadata: {name: w1-system}
      spec:
        source:
          registry: {url: "docker://ghcr.io/trevex/chalkos/kubernetes-1.37-kvm-disk@sha256:<digest>"}
        storage:
          accessModes: [ReadWriteOnce]
          resources: {requests: {storage: 20Gi}}
  template:
    spec:
      domain:
        memory: {guest: 3Gi}
        # The image boots with UEFI and is unsigned, and KubeVirt's Secure Boot variables hold
        # Microsoft's keys alone, so Secure Boot is off.
        firmware:
          bootloader:
            efi: {secureBoot: false, persistent: true}
        features:
          smm: {enabled: true}
        devices:
          tpm: {persistent: true}
          disks:
            - name: system
              disk: {bus: virtio}
      volumes:
        - name: system
          dataVolume: {name: w1-system}
```

The disk holds the ESP and the store's slot A, and a node's first boot adds slot B and STATE
behind them, so the disk's virtual size is the whole system region: 7,920,943,104 bytes
(7554 MiB) for the release images. That is 1 MiB for the partition table, the 1 GiB ESP, two
slots of a 3 GiB store and its 128 MiB hash tree, the 128 MiB STATE and 1 MiB for the backup
partition table. The persistent volume must be at least that large. CDI grows the disk to the
volume's size, and VAR, which holds etcd, containerd's images and the logs, takes the space
behind the system region, so the example's 20 GiB leaves VAR about 12.6 GiB.
[Size VAR](../operate/storage-volumes.md#size-var) covers how much VAR a node needs. A persistent
TPM and EFI variable store keep the node's sealed disk keys and its boot entries across restarts.

A `containerDisk` volume,
`{name: system, containerDisk: {image: "ghcr.io/trevex/chalkos/kubernetes-1.37-kvm-disk@sha256:<digest>"}}`,
boots the disk without CDI, at its own size. That leaves no room for VAR, and KubeVirt discards
the volume's writes when the virtual machine stops, so the node loses its config and secrets on
every restart. Use a DataVolume for a node.

## Download chalkctl and the installer

Download chalkctl, the checksums and the signature bundles from the release, and check each file
against `SHA256SUMS`:

<!-- x-release-please-start-version -->
```sh
release=https://github.com/trevex/chalkos/releases/download/v0.10.0
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
has no OS CA; a cluster that needs one builds its own installer with `mkImages`.

--8<-- "docs/includes/no-os-ca.md"

## Fetch and verify an SBOM

A software bill of materials (SBOM) lists what an image or a program is made of. Each release image
has an SBOM in [SPDX](https://spdx.dev/) 2.3 JSON, which the `-disk` image of a `kvm` image
shares. It lists each package of the image's NixOS system and the kernel that its UKI carries,
with the package's version, its licence, the URL of its source and its package URL (purl), all
taken from the package's metadata in nixpkgs. A package whose metadata names no licence has the
licence `NOASSERTION`. Files that the system generates, such as configuration files and systemd
units, belong to no package and are not listed. chalkctl and chalkbuild each have an SBOM in
[CycloneDX](https://cyclonedx.org/) JSON, which lists the Go modules linked into the binary with
their versions and licences.

A release publishes each SBOM in two places:

- As release assets: `<image>.spdx.json` for each image, and `chalkctl-linux-amd64.cdx.json` and
  `chalkbuild-linux-amd64.cdx.json`. `SHA256SUMS` lists them, and each has a `.sigstore.json`
  bundle, like the other assets.
- As attestations. An image's SBOM is attached to its digest in the registry twice: as a cosign
  attestation of the type `spdxjson`, and as a GitHub attestation with the predicate type
  `https://spdx.dev/Document/v2.3`. A program's SBOM is a GitHub attestation of the binary with the
  predicate type `https://cyclonedx.org/bom`.

Fetch an image's SBOM from the registry with cosign, which checks the attestation's signature
first:

```sh
cosign verify-attestation ghcr.io/trevex/chalkos/router-metal@sha256:<digest> --type spdxjson \
  --certificate-identity https://github.com/trevex/chalkos/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com |
  jq -r .payload | base64 -d | jq .predicate >router-metal.spdx.json
```

The GitHub CLI checks the GitHub attestation and can print the SBOM it holds:

```sh
gh attestation verify oci://ghcr.io/trevex/chalkos/router-metal@sha256:<digest> \
  --repo trevex/chalkos --predicate-type https://spdx.dev/Document/v2.3 \
  --format json --jq '.[0].verificationResult.statement.predicate' >router-metal.spdx.json
```

For a program, name the binary that you downloaded:

```sh
gh attestation verify chalkctl-linux-amd64 --repo trevex/chalkos \
  --predicate-type https://cyclonedx.org/bom
```

To take an SBOM from the release's assets instead, download it with `SHA256SUMS` and the bundles,
and check it as you would chalkctl:

<!-- x-release-please-start-version -->
```sh
release=https://github.com/trevex/chalkos/releases/download/v0.10.0
curl -fL -O "$release/router-metal.spdx.json" -O "$release/router-metal.spdx.json.sigstore.json" \
  -O "$release/SHA256SUMS" -O "$release/SHA256SUMS.sigstore.json"
sha256sum --check --ignore-missing SHA256SUMS
cosign verify-blob router-metal.spdx.json --bundle router-metal.spdx.json.sigstore.json \
  --certificate-identity https://github.com/trevex/chalkos/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
<!-- x-release-please-end -->

List the packages of an image with their versions and licences:

```sh
jq -r '.packages[] | [.name, .versionInfo, .licenseDeclared] | @tsv' router-metal.spdx.json
```

An image you build yourself has the same SBOM as its `sbom` attribute.
[Inspect what an image contains](../build/add-software.md#inspect-what-an-image-contains) builds
it and lists the image's store paths with plain Nix.
## Licences and source

chalkos is licensed under the Apache License 2.0, and its release images carry the annotation
`org.opencontainers.image.licenses` with the value `Apache-2.0`. The images and the installer also
contain third-party free software, such as the Linux kernel, systemd and the GNU C Library, each
under its own licence, among them the GNU GPL, the GNU LGPL and the MIT licence. Each image's SBOM
lists its packages with their licences and the URLs of their source, as
[Fetch and verify an SBOM](#fetch-and-verify-an-sbom) describes.

The release's `flake.lock` fixes the corresponding source of every component, including the
patches that nixpkgs applies, and Nix fetches it from the components' upstreams or from the
mirrors `cache.nixos.org` and `tarballs.nixos.org`. To copy chalkos's source and every flake
input of a release, nixpkgs with its build instructions and patches included, to the Nix store:

```sh
nix flake archive github:trevex/chalkos/vX.Y.Z
```

To fetch the upstream source of one component, such as util-linux, from the nixpkgs the release
locks:

```sh
nix build --inputs-from github:trevex/chalkos/vX.Y.Z nixpkgs#util-linux.src
```

For three years after each release, the project also provides the complete corresponding source
of that release's third-party components to anyone who asks for it in an
[issue](https://github.com/trevex/chalkos/issues). The repository's
[`NOTICE`](https://github.com/trevex/chalkos/blob/main/NOTICE) file has the full
statement.

## Check that it worked

`cosign verify` prints the signature's claims as JSON and ends with no error, `cosign verify-blob`
prints `Verified OK`, and `gh attestation verify` prints that the verification succeeded. A
fetched SBOM is JSON whose `spdxVersion` is `SPDX-2.3`, and the `jq` listing of its packages
includes `linux` with `GPL-2.0-only`. The
inventory names each image by a digest that `cosign verify` accepted, and `chalkctl validate`
accepts every node's files against it.
