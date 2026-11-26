---
title: "Image options"
description: "The options of chalkos's node modules that an image's NixOS modules set"
---

# Image options

This page is for image authors who set the options of chalkos's node modules in an image's
NixOS modules, the `modules` of [mkImage and mkImages](mkimages.md). Every image and the
installer carry these modules. The options an extension uses to declare kinds, facts, outputs
and effects are on [Extension API](extension-api.md).

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

## Storage { #storage }

The directories below the noexec VAR and root that programs run from.

### `chalkos.storage.execPaths` { #chalkosstorageexecpaths }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/base/exec-paths.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/exec-paths.nix)

Directories programs run from, below the noexec VAR or root, by absolute path. Each is
mounted onto itself with `exec` and `nosuid` or `suid`, `nodev` or `dev`, after the file
systems it lies on, VAR or a Volume, and before the units that need it, which name it in
`RequiresMountsFor`. A Volume mounted at the path itself takes these options instead of its
own. Paths hold no spaces or `=`.

Example:

```nix
{
  "/var/lib/containerd" = {
    suid = true;
  };
}
```

The options within each, by their path below `chalkos.storage.execPaths`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.dev`{ #chalkosstorageexecpathsdev } | boolean | `false` | Whether device files below the path open as devices. |
| `<name>.suid`{ #chalkosstorageexecpathssuid } | boolean | `false` | Whether set-user-ID and set-group-ID files below the path take effect. |

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

### `chalkos.kernel.groups` { #chalkoskernelgroups }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/kernel.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)

Groups of kernel modules by name, which `chalkos.kernel.moduleGroups` selects. chalkos
declares the groups of the hardware an image may run on and of its base software:
`can`, `filesystems`, `gpu`, `industrial`, `infiniband`, `media`, `network`, `platform`, `sound`, `storage`, `virtualisation` and `wireless`. The module of a feature declares the group the feature
needs, such as the base firewall's `firewall`, and adds it to
`chalkos.kernel.moduleGroups` while the feature is on.

The options within each, by their path below `chalkos.kernel.groups`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.directories`{ #chalkoskernelgroupsdirectories } | list of string | `[ ]` | Directories of the kernel's module tree, relative to its `kernel/` directory, whose modules the group holds, with those of their subdirectories. A directory without modules fails the build. Example: `[ "drivers/net/ethernet" ]` |
| `<name>.modules`{ #chalkoskernelgroupsmodules } | list of string | `[ ]` | Kernel modules the group holds besides those of its directories. A name the kernel does not know fails the build. Example: `[ "tun" ]` |

### `chalkos.kernel.moduleGroups` { #chalkoskernelmodulegroups }

Type: list of string · Default: `[ ]` · Declared in: [modules/node/kernel.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)

Groups of `chalkos.kernel.groups` whose modules the image carries. Every image carries
`storage`, `network`, `virtualisation`, `filesystems`, `platform` and `firewall`; the layer an image runs adds the groups it
needs, and an image's modules add to them, for example `[ "gpu" ]`; `lib.mkForce`
replaces them. A name that `chalkos.kernel.groups` does not declare fails the
evaluation. Modules from `boot.extraModulePackages` are always included whole, with the
modules of the kernel they depend on.

Example: `[ "gpu" ]`

### `chalkos.kernel.modulePolicy.enable` { #chalkoskernelmodulepolicyenable }

Type: boolean · Default: `true` · Declared in: [modules/node/base/module-policy.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/module-policy.nix)

Whether the kernel loads modules from the image's verity store and its initrd alone. The
image carries an IPE policy that allows those two and denies every other module, which
`chalkbuild sign` signs with the Secure Boot db key. A node whose firmware has the key in
db enforces it from the initrd on; a node without Secure Boot boots without it, and
`chalkctl status` says so.

## Kubernetes and CNI { #kubernetes-and-cni }

Whether an image runs Kubernetes, the packages and images it runs it with, and its CNI.

### `chalkos.kubernetes.bgp.enable` { #chalkoskubernetesbgpenable }

Type: boolean · Default: `true` · Declared in: [modules/node/kubernetes/bgp/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/bgp/default.nix)

Whether Kubernetes images carry FRR, which runs BGP and BFD, and accept the kinds `BGPPeer` and `BGPSpeaker`. Only with the Kubernetes layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.kubernetes.ceph.enable` { #chalkoskubernetescephenable }

Type: boolean · Default: `true` · Declared in: [modules/node/kubernetes/ceph.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/ceph.nix)

Whether Kubernetes images carry the `rbd`, `ceph` and `nbd` kernel modules, loaded at boot, and LVM for Rook's OSDs. Only with the Kubernetes layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.kubernetes.containerd.allowMacAdmin` { #chalkoskubernetescontainerdallowmacadmin }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/containerd.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/containerd.nix)

Whether privileged containers may hold `CAP_MAC_ADMIN`, as the Security Profiles Operator
needs. With it, such a container can switch the module policy to permissive, which
`chalkctl status` then reports as tampered. No container ever holds `CAP_SYS_MODULE`: the
kernel loads the modules pods need, on demand or at boot.

### `chalkos.kubernetes.containerd.runtimes` { #chalkoskubernetescontainerdruntimes }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/kubernetes/containerd.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/containerd.nix)

containerd's runtime handlers besides `runc`, by name, which a RuntimeClass's `handler`
names. They are part of containerd's read-only config, built with the image.

Example:

```nix
{ kata.options.ConfigPath = "/etc/kata/configuration.toml"; }
```

The options within each, by their path below `chalkos.kubernetes.containerd.runtimes`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.options`{ #chalkoskubernetescontainerdruntimesoptions } | TOML value | `{ }` | The handler's options, as containerd's CRI plugin takes them. Example: `{ BinaryName = "/nix/store/…/bin/nvidia-container-runtime"; }` |
| `<name>.runtimeType`{ #chalkoskubernetescontainerdruntimesruntimetype } | string | `"io.containerd.runc.v2"` | containerd's runtime type of the handler. |

### `chalkos.kubernetes.enable` { #chalkoskubernetesenable }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Whether the image runs Kubernetes: containerd, the kubelet and the preparation of the
node's Kubernetes configuration. An image's modules set it by listing the Kubernetes layer,
`chalkos.nixosModules.kubernetes`; the installer runs none.

### `chalkos.kubernetes.hostTools` { #chalkoskuberneteshosttools }

Type: map of path · Default: `{ }` · Declared in: [modules/node/kubernetes/host.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/host.nix)

Programs of the image, by name, that the image links into `/usr/bin` and `/usr/sbin`, where
workloads find them when they run them in the host's mount namespace through `nsenter`, as
Longhorn runs `fstrim`. Empty by default; features add theirs.

Example:

```nix
{ fstrim = "${pkgs.util-linux}/bin/fstrim"; }
```

### `chalkos.kubernetes.kubevirt.enable` { #chalkoskuberneteskubevirtenable }

Type: boolean · Default: `true` · Declared in: [modules/node/kubernetes/kubevirt.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/kubevirt.nix)

Whether Kubernetes images carry the kernel modules of KubeVirt's virtual machines, KVM, vhost, VFIO and UIO, loaded on demand. Only with the Kubernetes layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.kubernetes.kubevirt.iommu` { #chalkoskuberneteskubevirtiommu }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/kubevirt.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/kubevirt.nix)

Whether the kernel turns the IOMMU on in passthrough mode and loads `vfio_pci` at boot, as
KubeVirt needs to pass PCI devices through to virtual machines. AMD's IOMMU is on unless
the kernel's command line turns it off; this adds `intel_iommu=on iommu=pt`. Only with
`chalkos.kubernetes.kubevirt.enable`.

### `chalkos.kubernetes.longhorn.enable` { #chalkoskuberneteslonghornenable }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/longhorn.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/longhorn.nix)

Whether Kubernetes images carry what Longhorn runs on the host: iscsid and iscsiadm, the NFS client, the `iscsi_tcp` and `dm_crypt` kernel modules, loaded at boot, and links for `iscsiadm`, `fstrim`, `cryptsetup`, `dmsetup` and `mount` in `/usr/bin` and `/usr/sbin`. Only with the Kubernetes layer; set it true in an image's modules to add the feature to it.

### `chalkos.kubernetes.nvidia.enable` { #chalkoskubernetesnvidiaenable }

Type: boolean · Default: `false` · Declared in: [modules/node/kubernetes/nvidia.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/nvidia.nix)

Whether Kubernetes images carry NVIDIA's GPU driver, loaded at boot, with its firmware, containerd's runtime handler `nvidia`, a CDI specification of the node's GPUs written at boot and the driver's programs and libraries in `/usr/local/nvidia`, for NVIDIA's GPU operator with the driver and the toolkit on the host. The driver is unfree: the image's nixpkgs must allow it. Only with the Kubernetes layer; set it true in an image's modules to add the feature to it.

### `chalkos.kubernetes.nvidia.kernelModuleType` { #chalkoskubernetesnvidiakernelmoduletype }

Type: one of `open`, `proprietary` · Default: `"open"` · Declared in: [modules/node/kubernetes/nvidia.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/nvidia.nix)

Which of NVIDIA's kernel modules the image carries: `open`, NVIDIA's open modules, which
GPUs from Turing on run, or `proprietary`, which GPUs before Turing need. Only with
`chalkos.kubernetes.nvidia.enable`.

### `chalkos.kubernetes.nvidia.package` { #chalkoskubernetesnvidiapackage }

Type: package · Default: `config.boot.kernelPackages.nvidiaPackages.stable` · Declared in: [modules/node/kubernetes/nvidia.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/nvidia.nix)

NVIDIA's driver, of the image's kernel packages, whose kernel modules, firmware,
programs and libraries the image carries. Only with `chalkos.kubernetes.nvidia.enable`.

### `chalkos.kubernetes.package` { #chalkoskubernetespackage }

Type: package · Default: the release `chalkos.kubernetes.version` selects · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Kubernetes release whose kubelet the image runs, whose version the image's Kubernetes
facts name. The Cluster document names the images of the control plane, kube-proxy,
CoreDNS, flannel and the pod sandbox, and the image's checker holds their versions to
Kubernetes's version skew policy against this kubelet's. Set it for a release
`chalkos.kubernetes.version` does not offer, such as another patch release; an image
that sets both names one minor in both.

### `chalkos.kubernetes.registry.enable` { #chalkoskubernetesregistryenable }

Type: boolean · Default: `true` · Declared in: [modules/node/kubernetes/registry/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/registry/default.nix)

Whether Kubernetes images carry chalkos-registry, the cluster registry, and accept the kind `ClusterRegistry`. Only with the Kubernetes layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.kubernetes.version` { #chalkoskubernetesversion }

Type: one of `1.35`, `1.36`, `1.37` · Default: `"1.37"` · Declared in: [modules/node/kubernetes/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/default.nix)

Minor release of Kubernetes whose kubelet the image runs, which selects
`chalkos.kubernetes.package`: one of the three newest minors, the newest by default.
chalkos pins each to a patch release, whose kubelet the image's build compiles from
source, so the image's nixpkgs does not choose the release.

Example: `"1.36"`

### `chalkos.cni.plugins` { #chalkoscniplugins }

Type: list of one of `dhcp`, `host-local`, `static`, `bridge`, `dummy`, `host-device`, `ipvlan`, `loopback`, `macvlan`, `ptp`, `tap`, `vlan`, `bandwidth`, `firewall`, `portmap`, `sbr`, `tuning`, `vrf` · Default: `[ ]` · Declared in: [modules/node/kubernetes/cni.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/cni.nix)

CNI reference plugins the image ships, in containerd's plugin directory. With
`flannel` they are bridge, host-local, loopback and portmap, which its network runs,
besides flannel's own plugin; with `none` every reference plugin nixpkgs builds, since
the cluster's own network may use any. Definitions add to these; `lib.mkForce`
replaces them. containerd searches `/opt/cni/bin`, where CNI DaemonSets install their
plugins, before this directory, so a DaemonSet's plugin of the same name wins.

Example: `[ "bandwidth" ]`

### `chalkos.cni.provider` { #chalkoscniprovider }

Type: one of `flannel`, `none` · Default: `"flannel"` · Declared in: [modules/node/kubernetes/cni.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes/cni.nix)

Pod network of an image with Kubernetes. `flannel` connects pods across nodes with
VXLAN, configured by the node's Flannel document; `none` installs no network, for
clusters whose Addon documents bring their own, and ships every CNI reference plugin
(see `chalkos.cni.plugins`).

## Router and forwarding { #router-and-forwarding }

Whether an image carries the router layer, and whether its node forwards packets between its interfaces.

### `chalkos.router.bgp.enable` { #chalkosrouterbgpenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/bgp/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/bgp/default.nix)

Whether router images carry FRR, which runs BGP and BFD, and accept the kinds `BGP`, `BGPPeer`, `CommunityList`, `PrefixList` and `RoutingPolicy`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.dhcp.enable` { #chalkosrouterdhcpenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/dhcp/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/dhcp/default.nix)

Whether router images carry the DHCP server, Kea, which chalkos builds from source, and accept the kind `DHCPSubnet`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.dhcpRelay.enable` { #chalkosrouterdhcprelayenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/dhcp-relay/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/dhcp-relay/default.nix)

Whether router images carry the DHCP relay, dnsmasq, and accept the kind `DHCPRelay`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.enable` { #chalkosrouterenable }

Type: boolean · Default: `false` · Declared in: [modules/node/router/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/default.nix)

Whether the image carries the router layer, which forwards IPv4 and IPv6 packets between
the node's interfaces and accepts the kinds of `router.chalkos.dev`. An image's modules set
it by listing the router layer, `chalkos.nixosModules.router`; an image carries one layer at
most, the installer none.

### `chalkos.router.nat64.enable` { #chalkosrouternat64enable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/nat64/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/nat64/default.nix)

Whether router images carry Jool, the kernel module and tool of the NAT64 instances, and accept the kind `NAT64`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.ntp.enable` { #chalkosrouterntpenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/ntp/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/ntp/default.nix)

Whether router images carry an NTP server, the node's chrony, and accept the kind `TimeServer`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.resolver.enable` { #chalkosrouterresolverenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/resolver/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/resolver/default.nix)

Whether router images carry Unbound, the DNS resolvers of the router's VRFs, and accept the kind `Resolver`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

### `chalkos.router.wireguard.enable` { #chalkosrouterwireguardenable }

Type: boolean · Default: `true` · Declared in: [modules/node/router/wireguard/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/router/wireguard/default.nix)

Whether router images carry WireGuard, whose interfaces networkd runs, with wg from wireguard-tools for its diagnostic, and accept the kinds `WireGuard` and `WireGuardPeer`. Only with the router layer; set it false in an image's modules to leave the feature out of it.

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

Units of the boot whose failure leaves the node healthy. Only units multi-user.target and
sysinit.target pull in count; jobs that timers and sockets start never do. A config's trial
ignores them too.

Example: `[ "example-sync.service" ]`

### `chalkos.upgrade.healthTimeout` { #chalkosupgradehealthtimeout }

Type: integer of at least 1 · Default: `300` · Declared in: [modules/node/upgrade.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)

Seconds a boot of this image that systemd-boot counts has to become healthy. Then
chalkd reboots the node, so the boot loader tries again or falls back. A boot that is
not healthy this long after systemd finished starting it has settled, also when it is not
counted, and chalkctl stops waiting for it.

## Console { #console }

The kernel's consoles, where the boot and its passphrase prompts appear, whether chalkd serves the console on the machine's screen, and whether the console may act on the node.

### `chalkos.consoles` { #chalkosconsoles }

Type: non-empty (list of string matching the pattern [A-Za-z0-9]+(,[A-Za-z0-9]+)*) · Default: `[ "ttyS0,115200" "tty0" ]` · Declared in: [modules/node/consoles.nix](https://github.com/trevex/chalkos/blob/main/modules/node/consoles.nix)

The kernel's consoles in order, each as the kernel's `console=` takes it, such as
`ttyS0,115200` for the first serial port at 115200 baud or `tty0` for the screen. The
kernel prints on every one, and systemd asks for each passphrase and recovery key on every
one, so any of them can answer. An image whose modules set the list twice fails to build,
unless one of them uses `lib.mkDefault` or `lib.mkForce`.

The last entry becomes `/dev/console`, where systemd's boot status, the emergency messages and
chalkd's lines (its fingerprint and addresses among them) appear. While it is `tty0` or
`tty1` and the console runs on tty1, chalkd keeps off `/dev/console` and the console shows
the fingerprint and addresses. While it is a virtual terminal, `chalkos-serial-log@` copies
chalkd's lines from the journal to every serial port of the list (`ttyS0`, `ttyAMA0`,
`ttyUSB0`, `hvc0`, …), so a BMC's serial-over-LAN shows them. An entry of another form,
such as `uart8250,mmio32,0xfe215040`, gets only the kernel's messages. No entry gets a login
prompt: no account logs in on a node.

The `metal` platform, a platform of your own and the installer put the screen last; `kvm`
has the first serial port alone. Where people watch a BMC's serial-over-LAN, put that port
last, such as `[ "tty0" "ttyS0,115200" ]`, so the boot's status and the emergency messages
appear there too.

Example: `[ "tty0" "ttyS1,115200" ]`

### `chalkos.console.actions.enable` { #chalkosconsoleactionsenable }

Type: boolean · Default: `true` · Declared in: [modules/node/console.nix](https://github.com/trevex/chalkos/blob/main/modules/node/console.nix)

Whether the console may reboot and shut down the node, roll back its image and its
config, and run the image's diagnostics of operators. Anyone at the machine's keyboard
can use them; chalkd refuses them on the console's socket when this is false, and the
console runs the diagnostics of readers alone.

### `chalkos.console.enable` { #chalkosconsoleenable }

Type: boolean · Default: `true` · Declared in: [modules/node/console.nix](https://github.com/trevex/chalkos/blob/main/modules/node/console.nix)

Whether the console runs on the machine's screen, tty1, in place of a login prompt.
chalkd serves it, chalkos-console, through the socket `/run/chalkd/console.sock`, which
root alone reaches, without TLS. The console holds no certificate: it calls what the API
reference says the console may call.

## Debug and maintenance { #debug-and-maintenance }

The debug tools an image carries, whether its build lets a shell in, and the OS CA whose clients its maintenance mode accepts.

### `chalkos.debug.tools` { #chalkosdebugtools }

Type: boolean · Default: `false` · Declared in: [modules/node/base/closure.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/closure.nix)

Add bash, coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system
path, and crictl on images with Kubernetes; systemd, less, nftables and kmod are on it
already. Without the debug tools the system path holds no shell, and the build fails when
a shell gets into the image unless [`chalkos.shell.allow`](#chalkosshellallow) is set.
With them the image carries bash and allows a shell, and still has no `/bin/sh`.
Nodes have no logins; use the tools from a privileged pod on the node, for example
`kubectl debug node/<node> -it --profile=sysadmin --image=busybox -- chroot /host /run/current-system/sw/bin/bash`.

### `chalkos.shell.allow` { #chalkosshellallow }

Type: boolean · Default: `false` · Declared in: [modules/node/base/closure.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/closure.nix)

Let a shell, such as bash, dash or busybox, into the image's system or its initrd. Without
it the build fails when one gets in, and names the shell's store paths and what references
them. Nodes run no shell: nothing on them needs one, and a shell an attacker finds is the
first tool they use.

Set it on an image whose own units, outputs' checks, validators or diagnostics are shell
scripts, such as those `pkgs.writeShellScript` and `pkgs.writeShellApplication` write,
and to keep a package whose scripts the image does not run. A script still names its
shell by its store path: the image has no `/bin/sh` either way.
[Keep an image shell-free](../build/shell-free.md) shows how to find what brings a shell
in and how to do without it. The debug tools allow a shell too.

### `chalkos.maintenance.osCA` { #chalkosmaintenanceosca }

Type: path or null · Default: `null` · Declared in: [modules/node/chalkd.nix](https://github.com/trevex/chalkos/blob/main/modules/node/chalkd.nix)

The OS CA the image carries, from which chalkd in maintenance mode accepts client
certificates; null accepts any client until the node is installed. mkImage and mkInstaller
set it, in every image and the installer, from their [`osCA`](mkimages.md#osca) argument,
which describes the file; the image's build checks the file's version again.

## Set by mkImage { #set-by-mkimage }

mkImage sets these from its arguments; a module reads them rather than setting them.

### `chalkos.image.name` { #chalkosimagename }

Type: string or null · Default: `null` · Declared in: [modules/node/settings.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)

The image's name, which mkImage sets, recorded as `CHALKOS_IMAGE` in its os-release and
as `name` in its image directory's `metadata.json`. `chalkctl upgrade --image` upgrades
the nodes running an image of this name. null on the installer, which is no node image.
No check compares it: a node may change the image it runs through an upgrade, and the
image's schemas decide which config it takes.

### `chalkos.platform.name` { #chalkosplatformname }

Type: string matching `[a-z0-9][a-z0-9-]*` or null · Default: `null` · Declared in: [modules/node/settings.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)

The platform the image is built for, mkImage's `platform`, which mkImage sets; null on
the installer, which installs images of every platform. A node refuses an image of another
platform, and translates its config for its own.

### `chalkos.provenance.revision` { #chalkosprovenancerevision }

Type: string or null · Default: `null` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

The git revision `metadata.json` records; mkImage sets it from its
[`revision`](mkimages.md#revision) argument.

### `chalkos.provenance.tag` { #chalkosprovenancetag }

Type: string or null · Default: `null` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

The git tag `metadata.json` records; mkImage sets it from its [`tag`](mkimages.md#tag)
argument.

Sources: the option declarations of the node modules in `modules/node/`, through
nixosOptionsDoc.
