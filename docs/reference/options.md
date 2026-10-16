---
title: "Cluster and node options"
description: "The options of a chalkos cluster definition and of its role images"
---

# Cluster and node options

A cluster definition sets the options under `chalkos` of the cluster modules, which
`chalkos.lib.mkCluster` evaluates; a role's `nixosModules` set the options of its role image,
a NixOS system with the node modules. This page is generated from those modules with
nixosOptionsDoc; each option links to its declaration.

## Cluster definition

### chalkos\.cluster\.endpoint

URL of the Kubernetes API server used by nodes and clients\.



*Type:*
string



*Example:*

```nix
"https://10.0.0.10:6443"
```

*Declared by:*
 - [modules/cluster/options/cluster\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/cluster.nix)



### chalkos\.cluster\.kubernetes\.package



Kubernetes release of the cluster\. Nodes run its kubelet; the control plane runs the
upstream images of the same version\. A Kubernetes upgrade is an image upgrade\.



*Type:*
package



*Default:*

```nix
pkgs.kubernetes
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.allowSchedulingOnControlPlanes



Let workloads run on control-plane nodes, which are otherwise tainted\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.dnsIPs\.ipv4



Service address of the cluster DNS in each family’s service range\. Pods use the primary
family’s: the DNS service has that family alone, as with kubeadm\.



*Type:*
string



*Default:*
the 10th address of serviceCIDRs\.ipv4, ` 10.96.0.10 `

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.dnsIPs\.ipv6



Service address of the cluster DNS in each family’s service range\. Pods use the primary
family’s: the DNS service has that family alone, as with kubeadm\.



*Type:*
string



*Default:*
the 10th address of serviceCIDRs\.ipv6, ` fd00:10:96::a `

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.domain



DNS domain of the cluster\.



*Type:*
string



*Default:*

```nix
"cluster.local"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.extraArgs\.etcd



Extra flags of etcd, named without the leading dashes with letters, digits, ` _ `, ` . ` and ` - `; they override chalkos’s own\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.extraArgs\.kube-apiserver



Extra flags of kube-apiserver, named without the leading dashes with letters, digits, ` _ `, ` . ` and ` - `; they override chalkos’s own\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.extraArgs\.kube-controller-manager



Extra flags of kube-controller-manager, named without the leading dashes with letters, digits, ` _ `, ` . ` and ` - `; they override chalkos’s own\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.extraArgs\.kube-scheduler



Extra flags of kube-scheduler, named without the leading dashes with letters, digits, ` _ `, ` . ` and ` - `; they override chalkos’s own\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.extraArgs\.kubelet



Extra flags of the kubelet, named without the leading dashes with letters, digits, ` _ `, ` . ` and ` - `; they override chalkos’s own\. The node’s labels and taints come from ` chalkos.nodes.<name>.labels ` and ` taints `\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.images\.coredns



Image of the cluster DNS\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/coredns/coredns:v1.14.6"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.images\.etcd



Image of etcd; the default is the version kubeadm uses with this Kubernetes release\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/etcd:3.7.0-0"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.images\.pause



Image of the pod sandbox\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/pause:3.10.2"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.ipFamilies



Address families of the cluster, the primary one first\. Every node has one address of
each: the kubelet registers them, the control plane’s certificates name them, and etcd and
the API server advertise the primary one\. Pods get an address of each, services one of
each family they ask for\. The families, and their order, are fixed when the cluster is
created\.



*Type:*
list of (one of “ipv4”, “ipv6”)



*Default:*

```nix
[
  "ipv4"
]
```



*Example:*

```nix
[
  "ipv4"
  "ipv6"
]
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.nodeCIDRMaskSizes\.ipv4



Prefix length of each node’s part of the pod range of each family\.



*Type:*
signed integer



*Default:*

```nix
24
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.nodeCIDRMaskSizes\.ipv6



Prefix length of each node’s part of the pod range of each family\.



*Type:*
signed integer



*Default:*

```nix
64
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.nodeIP\.timeout



Seconds a node waits at boot for its address, so DHCP leases and addresses a routing
daemon adds late still count\. A node without one runs neither the kubelet nor static
pods\.



*Type:*
positive integer, meaning >0



*Default:*

```nix
300
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.nodeIP\.validSubnets



Subnets in CIDR notation, IPv4 or IPv6, that nodes without a fixed nodeIP pick their
address from at every boot; a leading ` ! ` excludes a subnet\. A node takes the first
matching address: IPv4 before IPv6, then by interface name and address, and the
endpoint’s address, which may be a virtual one, only when no other matches\. Empty takes
any global unicast address\. Addresses in the pod and service ranges and on the
interfaces of the pod network and kube-proxy (` flannel* `, ` cni* `, ` veth* `, ` kube-* `)
are never taken\. chalkos\.nodes\.\<name>\.kubernetes\.validSubnets overrides this per node\.



*Type:*
list of (subnet in CIDR notation, excluded with a leading “!”)



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "10.0.0.0/8"
  "!10.0.0.10/32"
]
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.podCIDRs\.ipv4



Address range of pods of each family of ipFamilies; each node gets a part of it\.



*Type:*
string



*Default:*

```nix
"10.244.0.0/16"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.podCIDRs\.ipv6



Address range of pods of each family of ipFamilies; each node gets a part of it\.



*Type:*
string



*Default:*

```nix
"fd00:10:244::/56"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.serviceCIDRs\.ipv4



Address range of services of each family of ipFamilies\. The API server’s kubernetes
service gets the first address of the primary family’s\.



*Type:*
string



*Default:*

```nix
"10.96.0.0/12"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.serviceCIDRs\.ipv6



Address range of services of each family of ipFamilies\. The API server’s kubernetes
service gets the first address of the primary family’s\.



*Type:*
string



*Default:*

```nix
"fd00:10:96::/112"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.vip\.addresses



Virtual addresses of the API server, at most one per family of ipFamilies\. One healthy
control-plane node at a time holds them, chosen by an election in etcd, so the endpoint
is one of them or a host name that resolves to them\. Empty means no virtual IP\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "10.0.0.10"
]
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.vip\.interface



Interface the addresses are added to; null means the interface holding the node’s
address of the address’s family\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.vip\.mode



How the holder announces the addresses: l2 adds them to an interface and announces
them with gratuitous ARP and unsolicited neighbour advertisements\.



*Type:*
value “l2” (singular enum)



*Default:*

```nix
"l2"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.kubernetes\.vxlanSourceSubnets



Ranges in CIDR notation, IPv4 or IPv6, that flannel’s VXLAN must come from: a node takes
VXLAN only from a source in a range of its family\. Every node’s addresses must be in them:
its fixed nodeIPs and the subnets it picks its addresses from are checked at evaluation,
and a node that picks one outside them refuses to prepare\. They need a range of each of
ipFamilies and may not overlap podCIDRs or serviceCIDRs, from which pods could send\. The
nodes masquerade what pods send to other nodes behind their own addresses, so they drop
VXLAN that pods send to these ranges\. Empty takes VXLAN from any source, so pods too
reach the other nodes’ VXLAN port, directly or through that masquerade\. A node joining
changes nothing on the others as long as its addresses are in these ranges\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "10.0.0.0/16"
  "fd00:10::/48"
]
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.manifests



Kubernetes objects the control plane applies with server-side apply after the built-in
ones, in this order, at bootstrap and at every boot\.



*Type:*
list of attribute set of anything



*Default:*

```nix
[ ]
```



*Example:*

```nix
[ { apiVersion = "v1"; kind = "Namespace"; metadata.name = "apps"; } ]

```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.name



Cluster name\. The cluster is expected under the flake output ` chalkos.<name> `\.



*Type:*
string matching the pattern \[a-z0-9]\[a-z0-9-]\*

*Declared by:*
 - [modules/cluster/options/cluster\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/cluster.nix)



### chalkos\.cluster\.registries\.allowPlainHTTP



Allow mirrors that are not https:// URLs\. It exists for test registries: nothing
authenticates what a plain HTTP mirror serves\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.registries\.mirrors



Mirrors of container registries, by registry host, as https:// URLs\. containerd tries
them in order and falls back to the registry itself\.



*Type:*
attribute set of list of string



*Default:*

```nix
{ }
```



*Example:*

```nix
{
  "docker.io" = [
    "https://mirror.example.com"
  ];
}
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.cluster\.system



Platform the role images are built for\.



*Type:*
string



*Default:*

```nix
"x86_64-linux"
```

*Declared by:*
 - [modules/cluster/options/cluster\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/cluster.nix)



### chalkos\.cni\.flannel\.image



Image of flannel\.



*Type:*
string



*Default:*

```nix
"ghcr.io/flannel-io/flannel:v0.28.9"
```

*Declared by:*
 - [modules/cluster/features/cni/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/features/cni/default.nix)



### chalkos\.cni\.flannel\.mtu



MTU flannel’s VXLAN assumes for the network between the nodes; its VXLAN devices and the
pods get 50 less\. null takes the MTU of the interface holding the node’s address\. Nodes
whose address is on a loopback interface need it set, and a dummy interface holding a
node’s address needs an MTU of at least this, 20 more for an IPv6 address: the nodes
refuse to prepare otherwise\. flannel subtracts 50 in both families but IPv6 VXLAN needs
70, so with IPv6 in use set this to the network’s MTU minus 20; otherwise IPv6 pod packets
near the full size rely on path MTU discovery\. At least 1330 with IPv6 in use, so pods get
IPv6’s minimum of 1280, and 626 otherwise\.



*Type:*
null or (positive integer, meaning >0)



*Default:*

```nix
null
```



*Example:*

```nix
1430
```

*Declared by:*
 - [modules/cluster/features/cni/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/features/cni/default.nix)



### chalkos\.cni\.plugins



CNI reference plugins the images ship, in containerd’s plugin directory\. With ` flannel `
they are bridge, host-local, loopback and portmap, which its network runs, besides
flannel’s own plugin; with ` none ` every reference plugin nixpkgs builds, since the
cluster’s manifests may use any\. Definitions add to these; ` lib.mkForce ` replaces them\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "bandwidth"
]
```

*Declared by:*
 - [modules/cluster/features/cni/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/features/cni/default.nix)



### chalkos\.cni\.provider



Pod network\. ` flannel ` connects pods across nodes with VXLAN; ` none ` installs no
network, for clusters whose manifests bring their own, and ships every CNI reference
plugin (see ` chalkos.cni.plugins `)\.



*Type:*
one of “flannel”, “none”



*Default:*

```nix
"flannel"
```

*Declared by:*
 - [modules/cluster/features/cni/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/features/cni/default.nix)



### chalkos\.installer\.image



Installer of the cluster: a raw image and a hybrid ISO that boot chalkd in maintenance
mode, from which chalkctl install writes a node’s role image of any platform to its
system disk\.



*Type:*
package *(read only)*

*Declared by:*
 - [modules/cluster/options/installer\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/installer.nix)



### chalkos\.installer\.nixosModules



NixOS modules added to the installer: systemd-networkd with static addresses, VLANs and
bonds for the machines it boots on, further kernel module groups and firmware, consoles\.
Secure Boot fixes the kernel command line, so what differs between machines goes into the
image, matched by MAC address or interface name\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/cluster/options/installer\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/installer.nix)



### chalkos\.manifest



Generated cluster description read by chalkctl (JSON-serialisable, schemaVersion 0)\.



*Type:*
raw value *(read only)*

*Declared by:*
 - [modules/cluster/options/manifest\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/manifest.nix)



### chalkos\.nodes



Nodes of the cluster\. Extensions add their own per-node options to this submodule, and the documents of their kinds to ` documents `\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.documents



Config documents of the node besides those chalkctl generates from its other options,
each an attribute set of ` apiVersion `, ` kind `, ` metadata ` and ` spec `\. Extensions add
the documents of the kinds their role modules declare under ` chalkos.kinds `; chalkctl
and chalkd check them against the schemas of the node’s image\. Values are serialised to
JSON, so a path becomes the path of its copy in the Nix store, not the text of the file\.
A ` Secret ` document here puts its values in the manifest and the Nix store\.



*Type:*
list of attribute set of anything



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  {
    apiVersion = "rack.example.org/v1alpha1";
    kind = "Rack";
    metadata.name = "rack";
    spec.location = "rack-a/u12";
  }
]

```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.hostname



Hostname set at boot from the node’s config\.



*Type:*
string



*Default:*

```nix
"‹name›"
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.kubernetes\.kind



What the node is in Kubernetes\. A control plane runs etcd and the control plane
besides the kubelet\. It reaches the node as ` Node.spec.type `, which a node keeps
once it holds its Kubernetes share\. A role without Kubernetes takes no control
plane\.



*Type:*
one of “controlplane”, “worker”



*Default:*

```nix
"worker"
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.nodes\.\<name>\.kubernetes\.nodeIP



Shorthand for nodeIPs with one address\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.nodes\.\<name>\.kubernetes\.nodeIPs



Fixed addresses of the node, at most one per family of
chalkos\.cluster\.kubernetes\.ipFamilies: the kubelet registers them and, on
control-plane nodes, the certificates name them and etcd and the API server
advertise the primary family’s\. The node waits at boot until its interfaces hold
them\. The node picks the address of a family without one from the validSubnets that
apply\.



*Type:*
list of string



*Default:*
nodeIP, or else the node’s first static address of each family, unless validSubnets apply to the node

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.nodes\.\<name>\.kubernetes\.validSubnets



Subnets the node picks its address from instead of
chalkos\.cluster\.kubernetes\.nodeIP\.validSubnets; null uses those\.



*Type:*
null or (list of (subnet in CIDR notation, excluded with a leading “!”))



*Default:*

```nix
null
```



*Example:*

```nix
[
  "192.168.100.0/24"
]
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.nodes\.\<name>\.labels



Kubernetes labels applied to the node\.



*Type:*
attribute set of string



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.network



systemd-networkd configuration in the shape of NixOS’s ` systemd.network ` options
(` networks `, ` netdevs `, ` links `), delivered to the node as networkd documents of its
config\.



*Type:*
lazy attribute set of anything



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.platform



Platform the node runs on, which must name an entry of ` chalkos.platforms `: the node
runs its role’s image for it, and refuses images of another platform\. Changing it is a
reinstall\.



*Type:*
one of “kvm”, “metal”



*Default:*

```nix
"metal"
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.role



Role whose image this node runs; must name an entry of ` chalkos.roles `\.



*Type:*
impossible (empty enum)

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.storage\.encryption\.fallback



Second keyslot asked for on the console when TPM2 unsealing fails\.



*Type:*
one of “recovery-key”, “password”, “none”



*Default:*

```nix
"recovery-key"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.encryption\.mode



Encryption of STATE, VAR and volumes: ` tpm2 ` seals a LUKS2 key to PCR 7, ` none ` uses no
encryption\. STATE holds the node’s secrets\.



*Type:*
one of “tpm2”, “none”



*Default:*

```nix
"tpm2"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.system\.disk



Disk holding the system region and VAR\.



*Type:*
disk reference (a /dev/ path, or a selector with model, serial, wwn, size or type)



*Example:*

```nix
"/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.var\.encryption\.mode



Overrides the node’s encryption mode for VAR\.



*Type:*
null or one of “tpm2”, “none”



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.var\.size



Size of VAR; null fills the remaining space on the system disk\.



*Type:*
null or size such as 512M or 2T (base 1024)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes



Additional volumes, by name\. The name is also the partition label\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.enable



Whether the node has this volume; set to false on a node to drop a volume its role defines\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.disk



Disk the volume occupies on its own; null places it on the system disk\.



*Type:*
null or disk reference (a /dev/ path, or a selector with model, serial, wwn, size or type)



*Default:*

```nix
null
```



*Example:*

```nix
{
  model = "Samsung SSD 990*";
  type = "nvme";
}
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.encryption\.mode



Overrides the node’s encryption mode for this volume\.



*Type:*
null or one of “tpm2”, “none”



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.format



File system of the volume; null leaves a raw block device\.



*Type:*
null or one of “ext4”, “xfs”, “btrfs”, “swap”



*Default:*

```nix
"ext4"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.mountPoint



Where the volume is mounted; null leaves it unmounted\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"/var/lib/longhorn"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.repart



Extra repart\.d keys of the volume’s partition, merged last\. Type, Label, Encrypt,
Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options\.



*Type:*
attribute set of (string or signed integer or boolean or list of string)



*Default:*

```nix
{ }
```



*Example:*

```nix
{
  Weight = 2000;
}
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.storage\.volumes\.\<name>\.size



Size of the volume; null fills the remaining space on its disk\.



*Type:*
null or size such as 512M or 2T (base 1024)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.nodes\.\<name>\.taints



Kubernetes taints applied to the node\.



*Type:*
list of (submodule)



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.taints\.\*\.effect



What happens to pods that do not tolerate the taint\.



*Type:*
one of “NoSchedule”, “PreferNoSchedule”, “NoExecute”

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.taints\.\*\.key



Taint key\.



*Type:*
string



*Example:*

```nix
"dedicated"
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.taints\.\*\.value



Taint value; null for a taint without a value\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.time\.servers



Time servers of this node, replacing ` chalkos.time.servers `; null uses those\.



*Type:*
null or (list of (submodule))



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.time\.servers\.\*\.host



Host name or address of the time server\.



*Type:*
string matching the pattern \[A-Za-z0-9]\[A-Za-z0-9\.:-]\*



*Example:*

```nix
"ptbtime1.ptb.de"
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.time\.servers\.\*\.nts



Authenticate the server with Network Time Security (NTS)\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.nodes\.\<name>\.time\.servers\.\*\.port



UDP port of the server’s NTP; null is NTP’s port, 123\.



*Type:*
null or 16 bit unsigned integer; between 0 and 65535 (both inclusive)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/nodes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/nodes.nix)



### chalkos\.osCA



Public part of the cluster’s secrets (` secrets.pub.json ` from ` chalkctl gen secrets `, version
3), whose OS CA every role image and the installer carry, so chalkd
in maintenance mode accepts only clients with a certificate from it\. null builds images that
accept any client until they are installed\. It is an input of the images, which the role
builder and the installer set as their ` chalkos.maintenance.osCA `; the nodes’ config does
not carry it\.



*Type:*
null or absolute path



*Default:*

```nix
null
```



*Example:*

```nix
./secrets.pub.json
```

*Declared by:*
 - [modules/cluster/options/cluster\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/cluster.nix)



### chalkos\.platforms



Platforms the cluster’s role images are built for, by name: ` chalkos.roles.<role>.images.<platform> `
is a role’s image for one\. chalkos defines ` metal ` and ` kvm `; a definition adds modules to one
of them or a platform of its own\. A node runs the image of its role and its
` chalkos.nodes.<node>.platform `\.



*Type:*
attribute set of (submodule)

*Declared by:*
 - [modules/cluster/options/platforms\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/platforms.nix)



### chalkos\.platforms\.\<name>\.nixosModules



NixOS modules every role image of this platform carries, before the role’s own: kernel
module groups, firmware, agents, the kernel’s console and command line\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/cluster/options/platforms\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/platforms.nix)



### chalkos\.roles



Node roles\. Each role is built into one image per platform, which all its nodes on that platform share\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)
 - [modules/cluster/options/roles\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/roles.nix)



### chalkos\.roles\.\<name>\.images



Unsigned disk image of this role by platform, such as ` images.kvm `: the raw image with
` repart-output.json ` and ` repart.d `, each built only when asked for\. Cluster settings
and the role’s own flow into every platform’s image, so they must not be derived from
` chalkos.roles `\.



*Type:*
lazy attribute set of package *(read only)*



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/roles\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/roles.nix)



### chalkos\.roles\.\<name>\.kubernetes\.enable



Whether the role’s images run Kubernetes\. One image serves control planes and workers:
each node’s ` kubernetes.kind ` makes it one or the other at runtime\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/kubernetes.nix)



### chalkos\.roles\.\<name>\.nixosModules



NixOS modules added to this role’s images\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/cluster/options/roles\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/roles.nix)



### chalkos\.roles\.\<name>\.storage\.encryption\.fallback



Second keyslot asked for on the console when TPM2 unsealing fails\.



*Type:*
one of “recovery-key”, “password”, “none”



*Default:*

```nix
"recovery-key"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.encryption\.mode



Encryption of STATE, VAR and volumes: ` tpm2 ` seals a LUKS2 key to PCR 7, ` none ` uses no
encryption\. STATE holds the node’s secrets\.



*Type:*
one of “tpm2”, “none”



*Default:*

```nix
"tpm2"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.system\.disk



Disk holding the system region and VAR\.



*Type:*
null or disk reference (a /dev/ path, or a selector with model, serial, wwn, size or type)



*Default:*

```nix
null
```



*Example:*

```nix
"/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.var\.encryption\.mode



Overrides the node’s encryption mode for VAR\.



*Type:*
null or one of “tpm2”, “none”



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.var\.size



Size of VAR; null fills the remaining space on the system disk\.



*Type:*
null or size such as 512M or 2T (base 1024)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes



Additional volumes, by name\. The name is also the partition label\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.enable



Whether the node has this volume; set to false on a node to drop a volume its role defines\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.disk



Disk the volume occupies on its own; null places it on the system disk\.



*Type:*
null or disk reference (a /dev/ path, or a selector with model, serial, wwn, size or type)



*Default:*

```nix
null
```



*Example:*

```nix
{
  model = "Samsung SSD 990*";
  type = "nvme";
}
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.encryption\.mode



Overrides the node’s encryption mode for this volume\.



*Type:*
null or one of “tpm2”, “none”



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.format



File system of the volume; null leaves a raw block device\.



*Type:*
null or one of “ext4”, “xfs”, “btrfs”, “swap”



*Default:*

```nix
"ext4"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.mountPoint



Where the volume is mounted; null leaves it unmounted\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"/var/lib/longhorn"
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.repart



Extra repart\.d keys of the volume’s partition, merged last\. Type, Label, Encrypt,
Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options\.



*Type:*
attribute set of (string or signed integer or boolean or list of string)



*Default:*

```nix
{ }
```



*Example:*

```nix
{
  Weight = 2000;
}
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.roles\.\<name>\.storage\.volumes\.\<name>\.size



Size of the volume; null fills the remaining space on its disk\.



*Type:*
null or size such as 512M or 2T (base 1024)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/storage\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/storage.nix)



### chalkos\.secureBoot\.enrollment



How installation writes Secure Boot variables: ` append ` keeps existing db and dbx entries
and adds the cluster certificates, ` strict ` keeps only the cluster certificate plus
option ROM hashes, ` none ` never writes variables because the firmware is prepared
beforehand\.

chalkos does not act on this option yet: enroll the firmware keys out of band\. The option
is reserved for key enrollment by chalkd\.



*Type:*
one of “append”, “strict”, “none”



*Default:*

```nix
"append"
```

*Declared by:*
 - [modules/cluster/options/secure-boot\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/secure-boot.nix)



### chalkos\.secureBoot\.require



Refuse to install a node that could not boot the signed image under Secure Boot\.

chalkos does not act on this option yet: enroll the firmware keys out of band\. The option
is reserved for key enrollment by chalkd\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/secure-boot\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/secure-boot.nix)



### chalkos\.secureBoot\.signerCertificate



PEM certificate of the db signing key (public)\. chalkctl install and chalkctl upgrade
refuse an image whose UKI or boot loader this certificate’s key did not sign\.



*Type:*
null or absolute path



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/secure-boot\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/secure-boot.nix)



### chalkos\.time\.dhcpServers



Also synchronise with the time servers the network announces through DHCP, which are not
authenticated\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/cluster/options/time\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/time.nix)



### chalkos\.time\.servers



Time servers every node synchronises its clock with, authenticated with NTS unless
` nts = false `\. A node’s ` chalkos.nodes.<name>.time.servers ` replaces them\.



*Type:*
list of (submodule)



*Default:*

```nix
[ { host = "ptbtime1.ptb.de"; } { host = "ptbtime2.ptb.de"; } { host = "ptbtime3.ptb.de"; } ]
```

*Declared by:*
 - [modules/cluster/options/time\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/time.nix)



### chalkos\.time\.servers\.\*\.host



Host name or address of the time server\.



*Type:*
string matching the pattern \[A-Za-z0-9]\[A-Za-z0-9\.:-]\*



*Example:*

```nix
"ptbtime1.ptb.de"
```

*Declared by:*
 - [modules/cluster/options/time\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/time.nix)



### chalkos\.time\.servers\.\*\.nts



Authenticate the server with Network Time Security (NTS)\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/cluster/options/time\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/time.nix)



### chalkos\.time\.servers\.\*\.port



UDP port of the server’s NTP; null is NTP’s port, 123\.



*Type:*
null or 16 bit unsigned integer; between 0 and 65535 (both inclusive)



*Default:*

```nix
null
```

*Declared by:*
 - [modules/cluster/options/time\.nix](https://github.com/trevex/chalkos/blob/main/modules/cluster/options/time.nix)



## Role images

The options the node modules declare in each role image. The cluster's settings appear in a
role image under the same names as in the cluster definition, read-only.

### chalkos\.consumers

Services, by name, that read config kinds\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.consumers\.\<name>\.kinds



Config kinds the unit reads, at least one, by name: attributes of ` chalkos.kinds `, or
networkd’s ` Network `, ` NetDev ` and ` Link `\. The documents of a kind of ` chalkos.kinds ` are
in its ` dir `, networkd’s below ` /run/chalkos/config/networkd.chalkos.dev `\. The values
their ` secretRef ` fields name are the unit’s credentials ` secret_<secret>.<key> `\.



*Type:*
non-empty (list of string)



*Example:*

```nix
[
  "BGPPeering"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.consumers\.\<name>\.restartOnChange



Restart the unit when a document of one of its kinds changes, or a Secret one of them references\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.debug\.tools



Add coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system path,
and crictl on roles with Kubernetes; systemd, bash, less, nftables and kmod are on it
already\. Nodes have no logins; use them from a privileged pod on the node, for example
` kubectl debug node/<node> -it --profile=sysadmin --image=busybox -- chroot /host /run/current-system/sw/bin/bash `\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/base/closure\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/closure.nix)



### chalkos\.disk\.espSize



Size of the EFI system partition, which holds the UKIs of both slots; at least 260M, the smallest systemd-repart formats as vfat\.



*Type:*
string



*Default:*

```nix
"1G"
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



### chalkos\.disk\.stateSize



Size of the STATE partition holding the node’s config and secrets; at least 64M, the smallest encrypted ext4 partition systemd-repart makes\.



*Type:*
string



*Default:*

```nix
"128M"
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



### chalkos\.disk\.storeSize



Size of each store slot’s erofs data partition; null sizes it to its contents, for
images that are never upgraded\.



*Type:*
null or string



*Default:*

```nix
"3G"
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



### chalkos\.disk\.storeVeritySize



Size of each store slot’s dm-verity hash partition; null sizes it to its contents\. The
image build fails when the hash tree takes more than 80% of it\. With 4 KiB blocks the
tree needs up to about 1/128 of storeSize\.



*Type:*
null or string



*Default:*

```nix
"128M"
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



### chalkos\.kernel\.allModules



Carry every module of the kernel instead of the groups and names\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/kernel\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)



### chalkos\.kernel\.extraModules



Kernel modules the image carries besides its groups, with what they depend on\. A name
the kernel does not know fails the build\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "kvm_amd"
]
```

*Declared by:*
 - [modules/node/kernel\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)



### chalkos\.kernel\.moduleGroups



Groups of kernel modules the image carries\. The base groups are set by default and
a role’s definitions add to them, for example ` [ "gpu" ] `; ` lib.mkForce ` replaces
them\. Base: storage, network, virtualisation, filesystems, kubernetes, platform\. Further:
can, gpu, industrial, infiniband, media, sound, wireless\.
Modules from ` boot.extraModulePackages ` are always included whole, with the modules
of the kernel they depend on\.



*Type:*
list of (one of “can”, “filesystems”, “gpu”, “industrial”, “infiniband”, “kubernetes”, “media”, “network”, “platform”, “sound”, “storage”, “virtualisation”, “wireless”)



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/node/kernel\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kernel.nix)



### chalkos\.kinds



Config kinds the image accepts, by kind name\. A node’s config is a set of documents of
these kinds; chalkctl and chalkd validate each against the kind’s schema\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.dir



Directory on the running node that holds the node’s documents of the kind, one JSON file per document\.



*Type:*
string *(read only)*



*Default:*

```nix
"/run/chalkos/config/${group}/<kind>"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.get



Builds a shell command that prints the node’s documents of the kind with chalkcfg\.
` name ` picks one document, and must be a document name such as ` rack `; without it the
command prints every document of the kind, one line each\. ` path ` prints one field, such
as ` spec.location `: fields separated by dots, ` * ` or an index for the elements of a
list, and a key or ` * ` for the values of an attribute set\. The path must follow the
kind’s options, or the evaluation fails and lists the fields where it went wrong\.
` output ` passes ` json `, ` yaml `, ` jsonpath=<expression> ` or ` go-template=<template> `
instead of a path\. Run the command from a shell script, such as
` pkgs.writeShellScript `, since systemd expands ` $ ` and ` % ` in ` ExecStart= ` itself\.

A path is checked against the kind’s options, not against the documents\. Where a
document’s value is null or missing, or its list is shorter than the index, the
command exits with status 1; without ` name `, one such document fails the whole
command\. ` * ` over an attribute set prints the values in no particular order; for a
stable order, use an ` output ` such as
` go-template={{range $k, $v := .spec.labels}}{{$k}}={{$v}} {{end}} `, which goes
through the keys sorted\. A path cannot address a key that contains ` . `, whitespace or
one of ` ' `, ` \ `, ` @ `, ` $ `, ` , `, ` [ `, ` ] `, ` { ` and ` } `; the evaluation fails on such a
key, and a go-template ` output ` reads it instead\.



*Type:*
function that evaluates to a(n) string *(read only)*



*Default:*
a function of ` { name ? null, path ? null, output ? null } `



*Example:*

```nix
config.chalkos.kinds.Rack.get { name = "rack"; path = "spec.location"; }
# "/nix/store/…-chalkcfg-0.1.0/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'"

```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.group



API group of the kind: a DNS name its author controls, as for Kubernetes custom resources\.



*Type:*
string matching the pattern \[a-z0-9](\[-a-z0-9]\*\[a-z0-9])?(\\\.\[a-z0-9](\[-a-z0-9]\*\[a-z0-9])?)+



*Example:*

```nix
"bgp.example.org"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.multiple



Whether a node’s config may hold more than one document of the kind\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.spec



Option declarations of the document’s ` spec `, as under a module’s ` options `\. They are
converted into the kind’s JSON Schema: an option without a default is required, and
` description `, ` default ` and ` example ` carry over\.



*Type:*
attribute set of raw value



*Default:*

```nix
{ }
```



*Example:*

```nix
{
  asn = lib.mkOption { type = lib.types.ints.u32; description = "Local AS number."; };
}

```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.version



API version of the kind\. An incompatible change to the spec is a new version\.



*Type:*
string matching the pattern v\[0-9]+((alpha|beta)\[0-9]+)?



*Example:*

```nix
"v1alpha1"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.maintenance\.osCA



Public part of the cluster’s secrets (` secrets.pub.json ` from ` chalkctl gen secrets `),
whose OS CA the image carries, so chalkd in maintenance mode accepts only clients with a
certificate from it\. null builds an image that accepts any client until it is installed\.
The role builder and the installer set it from the cluster definition’s ` chalkos.osCA `,
which checks the file’s version; the image’s build checks it again\.



*Type:*
null or absolute path



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/chalkd\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/chalkd.nix)



### chalkos\.platform\.name



The platform the image is built for, a name of ` chalkos.platforms `, set by the role
builder; null on the installer, which installs images of every platform\. A node refuses
an image of another platform, and translates its config for its own\.



*Type:*
null or string matching the pattern \[a-z0-9]\[a-z0-9-]\*



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/settings\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)



### chalkos\.role\.kubernetes\.enable



Whether the image runs Kubernetes, from the role’s ` kubernetes.enable `, set by the role
builder; the installer runs none\. A node’s kind comes with its config\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/settings\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)



### chalkos\.upgrade\.bootTries



How often systemd-boot boots this image after an upgrade installed it before it falls
back to the image the node ran before, unless a boot was found healthy first\.



*Type:*
positive integer, meaning >0



*Default:*

```nix
3
```

*Declared by:*
 - [modules/node/upgrade\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)



### chalkos\.upgrade\.healthIgnoreUnits



Units of the boot whose failure leaves a node without Kubernetes healthy\. Only units
multi-user\.target and sysinit\.target pull in count; jobs that timers and sockets start
never do\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "example-sync.service"
]
```

*Declared by:*
 - [modules/node/upgrade\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)



### chalkos\.upgrade\.healthTimeout



Seconds a boot of this image that systemd-boot counts has to become healthy\. Then
chalkd reboots the node, so the boot loader tries again or falls back\.



*Type:*
positive integer, meaning >0



*Default:*

```nix
300
```

*Declared by:*
 - [modules/node/upgrade\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/upgrade.nix)



### chalkos\.variant\.name



The image’s variant, which its os-release names as ` CHALKOS_VARIANT ` and its image
directory’s ` metadata.json ` as ` variant `\. The role builder sets it to the role’s name\.
null on the installer, which is no variant\. No check compares it: a node may change its
variant through an upgrade, and the image’s schemas decide which config it takes\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/settings\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)


