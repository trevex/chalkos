---
title: "Image options"
description: "The options of chalkos's node modules that a variant's NixOS modules set"
---

# Image options

This page is for image authors who set the options of chalkos's node modules in a variant's
NixOS modules, the `modules` of [mkImages](mkimages.md). Every image and the installer carry
these modules. The options an extension uses to declare kinds, facts, outputs and effects
are on [Extension API](extension-api.md).

## Disk { #disk }

The sizes of the partitions an image's build lays out.

### `chalkos.disk.espSize` { #chalkosdiskespsize }

Type: string · Default: `"1G"` · Declared in: [modules/node/base/disk.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)

Size of the EFI system partition, which holds the UKIs of both slots; at least 260M, the smallest systemd-repart formats as vfat.

### `chalkos.disk.stateSize` { #chalkosdiskstatesize }

Type: string · Default: `"128M"` · Declared in: [modules/node/base/disk.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)

Size of the STATE partition holding the node's config and secrets; at least 64M, the smallest encrypted ext4 partition systemd-repart makes.

### `chalkos.disk.storeSize` { #chalkosdiskstoresize }

Type: string or null · Default: `"3G"` · Declared in: [modules/node/base/disk.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)

Size of each store slot's erofs data partition; null sizes it to its contents, for
images that are never upgraded.

### `chalkos.disk.storeVeritySize` { #chalkosdiskstoreveritysize }

Type: string or null · Default: `"128M"` · Declared in: [modules/node/base/disk.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)

Size of each store slot's dm-verity hash partition; null sizes it to its contents. The
image build fails when the hash tree takes more than 80% of it. With 4 KiB blocks the
tree needs up to about 1/128 of storeSize.

## Kernel { #kernel }

The kernel modules an image carries.

### `chalkos.kernel.allModules` { #chalkoskernelallmodules }

Type: boolean · Default: `false` · Declared in: [modules/node/kernel.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)

Carry every module of the kernel instead of the groups and names.

### `chalkos.kernel.extraModules` { #chalkoskernelextramodules }

Type: list of string · Default: `[ ]` · Declared in: [modules/node/kernel.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)

Kernel modules the image carries besides its groups, with what they depend on. A name
the kernel does not know fails the build.

Example: `[ "kvm_amd" ]`

### `chalkos.kernel.moduleGroups` { #chalkoskernelmodulegroups }

Type: list of one of `can`, `filesystems`, `gpu`, `industrial`, `infiniband`, `kubernetes`, `media`, `network`, `platform`, `sound`, `storage`, `virtualisation`, `wireless` · Default: `[ ]` · Declared in: [modules/node/kernel.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)

Groups of kernel modules the image carries. The base groups are set by default and
a variant's modules add to them, for example `[ "gpu" ]`; `lib.mkForce` replaces
them. Base: storage, network, virtualisation, filesystems, kubernetes, platform. Further:
can, gpu, industrial, infiniband, media, sound, wireless.
Modules from `boot.extraModulePackages` are always included whole, with the modules
of the kernel they depend on.

## Kubernetes and CNI { #kubernetes-and-cni }

Whether an image runs Kubernetes, the packages and images it runs it with, and its CNI.

### `chalkos.kubernetes.enable` { #chalkoskubernetesenable }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Whether the image runs Kubernetes: containerd, the kubelet and the preparation of the
node's Kubernetes configuration. One image serves control planes and workers: the type
of the node's Kubernetes Node document makes it one or the other. mkImages sets it from
the variant's `kubernetes`; the installer runs none.

### `chalkos.kubernetes.images.coredns` { #chalkoskubernetesimagescoredns }

Type: string · Default: `"registry.k8s.io/coredns/coredns:v1.14.6"` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Image of the cluster DNS.

### `chalkos.kubernetes.images.etcd` { #chalkoskubernetesimagesetcd }

Type: string · Default: `"registry.k8s.io/etcd:3.7.0-0"` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Image of etcd. The default is the version kubeadm uses with the release of the default `chalkos.kubernetes.package`; an image with another release may need another.

### `chalkos.kubernetes.images.pause` { #chalkoskubernetesimagespause }

Type: string · Default: `"registry.k8s.io/pause:3.10.2"` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Image of the pod sandbox.

### `chalkos.kubernetes.package` { #chalkoskubernetespackage }

Type: package · Default: `pkgs.kubernetes` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Kubernetes release of the image. Nodes run its kubelet; control planes run the
upstream images of the same version. A Kubernetes upgrade is an image upgrade.

### `chalkos.cni.flannel.image` { #chalkoscniflannelimage }

Type: string · Default: `"ghcr.io/flannel-io/flannel:v0.28.9"` · Declared in: [modules/node/cni.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)

Image of flannel.

### `chalkos.cni.plugins` { #chalkoscniplugins }

Type: list of string · Default: `[ ]` · Declared in: [modules/node/cni.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)

CNI reference plugins the image ships, in containerd's plugin directory. With
`flannel` they are bridge, host-local, loopback and portmap, which its network runs,
besides flannel's own plugin; with `none` every reference plugin nixpkgs builds, since
the cluster's own network may use any. Definitions add to these; `lib.mkForce`
replaces them.

Example: `[ "bandwidth" ]`

### `chalkos.cni.provider` { #chalkoscniprovider }

Type: one of `flannel`, `none` · Default: `"flannel"` · Declared in: [modules/node/cni.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)

Pod network of an image with Kubernetes. `flannel` connects pods across nodes with
VXLAN, configured by the node's Flannel document; `none` installs no network, for
clusters whose Addon documents bring their own, and ships every CNI reference plugin
(see `chalkos.cni.plugins`).

## Router and forwarding { #router-and-forwarding }

Whether an image carries the router layer, and whether its node forwards packets between its interfaces.

### `chalkos.router.enable` { #chalkosrouterenable }

Type: boolean · Default: `false` · Declared in: [modules/node/router/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/default.nix)

Whether the image carries the router layer, which forwards IPv4 and IPv6 packets between
the node's interfaces. An image carries the Kubernetes layer or the router layer, not both.
mkImages sets it from the variant's `router`; the installer carries no layer.

### `chalkos.forwarding.enable` { #chalkosforwardingenable }

Type: boolean · Default: `false` · Declared in: [modules/node/network.nix](https://github.com/trevex/chalkos/blob/main/modules/node/network.nix)

Whether the node forwards IPv4 and IPv6 packets between its interfaces, as a Kubernetes node
does for its pods and a router for its networks. The Kubernetes and the router layer turn it
on.

## Upgrade and health { #upgrade-and-health }

How many times systemd-boot boots a new image before it falls back, and when a boot of it is healthy.

### `chalkos.upgrade.bootTries` { #chalkosupgradeboottries }

Type: integer of at least 1 · Default: `3` · Declared in: [modules/node/upgrade.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)

How often systemd-boot boots this image after an upgrade installed it before it falls
back to the image the node ran before, unless a boot was found healthy first.

### `chalkos.upgrade.healthIgnoreUnits` { #chalkosupgradehealthignoreunits }

Type: list of string · Default: `[ ]` · Declared in: [modules/node/upgrade.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)

Units of the boot whose failure leaves a node without Kubernetes healthy. Only units
multi-user.target and sysinit.target pull in count; jobs that timers and sockets start
never do.

Example: `[ "example-sync.service" ]`

### `chalkos.upgrade.healthTimeout` { #chalkosupgradehealthtimeout }

Type: integer of at least 1 · Default: `300` · Declared in: [modules/node/upgrade.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)

Seconds a boot of this image that systemd-boot counts has to become healthy. Then
chalkd reboots the node, so the boot loader tries again or falls back.

## Debug and maintenance { #debug-and-maintenance }

The debug tools an image carries, and the OS CA whose clients its maintenance mode accepts.

### `chalkos.debug.tools` { #chalkosdebugtools }

Type: boolean · Default: `false` · Declared in: [modules/node/base/closure.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/closure.nix)

Add coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system path,
and crictl on images with Kubernetes; systemd, bash, less, nftables and kmod are on it
already. Nodes have no logins; use them from a privileged pod on the node, for example
`kubectl debug node/<node> -it --profile=sysadmin --image=busybox -- chroot /host /run/current-system/sw/bin/bash`.

### `chalkos.maintenance.osCA` { #chalkosmaintenanceosca }

Type: path or null · Default: `null` · Declared in: [modules/node/chalkd.nix](https://github.com/trevex/chalkos/blob/main/modules/node/chalkd.nix)

Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`),
whose OS CA the image carries, so chalkd in maintenance mode accepts only clients with a
certificate from it. null builds an image that accepts any client until it is installed.
mkImages sets it, in every image and the installer, from its `osCA`, which checks the
file's version; the image's build checks it again.

## Set by mkImages { #set-by-mkimages }

mkImages sets these from its arguments; a module reads them rather than setting them.

### `chalkos.variant.name` { #chalkosvariantname }

Type: string or null · Default: `null` · Declared in: [modules/node/settings.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)

The image's variant, which its os-release names as `CHALKOS_VARIANT` and its image
directory's `metadata.json` as `variant`. mkImages sets it to the variant's name. null
on the installer, which is no variant. No check compares it: a node may change its
variant through an upgrade, and the image's schemas decide which config it takes.

### `chalkos.platform.name` { #chalkosplatformname }

Type: string matching `[a-z0-9][a-z0-9-]*` or null · Default: `null` · Declared in: [modules/node/settings.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)

The platform the image is built for, a name of mkImages's `platforms`, which mkImages
sets; null on the installer, which installs images of every platform. A node refuses an
image of another platform, and translates its config for its own.

### `chalkos.provenance.revision` { #chalkosprovenancerevision }

Type: string or null · Default: `null` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

The git revision the image is built from, as `metadata.json` records it; mkImages sets
it from its `revision` argument. It never reaches the store, so it changes no root hash,
and nothing orders or refuses images by it.

### `chalkos.provenance.tag` { #chalkosprovenancetag }

Type: string or null · Default: `null` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

The git tag the image is built from, as `metadata.json` records it; mkImages sets it
from its `tag` argument. Like the revision, it only says where the image came from.

Sources: the option declarations of the node modules in `modules/node/`, through
nixosOptionsDoc.
