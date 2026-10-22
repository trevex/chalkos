---
title: "mkImages and image options"
description: "The arguments of chalkos.lib.mkImages and the options of the images it builds"
---

# mkImages and image options

`chalkos.lib.mkImages` builds a cluster's images from its arguments: each variant on each
platform, and the installer. The NixOS modules of a variant set the options of its images,
NixOS systems with chalkos's node modules. This page is generated from those modules with
nixosOptionsDoc; each option links to its declaration.

A flake calls mkImages and exposes the images to build:

```nix title="flake.nix"
{
  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        platforms = [ "metal" "kvm" ];
        # Every variant's images carry these.
        modules = [ ./site.nix ];
        variants = {
          node = { };
          storage = {
            kubernetes = false;
            modules = [ ./storage.nix ];
          };
        };
        installer.modules = [ ./installer-network.nix ];
      };
    in
    {
      packages.x86_64-linux = images.images // { inherit (images) installer; };
    };
}
```

It returns:

- `images.<variant>-<platform>`: the image directory of a variant on a platform, such as
  `images.node-metal`, each built only when asked for.
- `installer`: the installer, a raw image and a hybrid ISO in one directory, which installs
  images of every platform.
- `nixos.<variant>-<platform>` and `nixos.installer`: the evaluated NixOS systems, to read an
  image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`.

## mkImages arguments

### installer\.modules

NixOS modules of the installer: systemd-networkd with static addresses, VLANs and bonds
for the machines it boots on, further kernel module groups and firmware, consoles\. Secure
Boot fixes the kernel command line, so what differs between machines goes into the image,
matched by MAC address or interface name\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### modules



NixOS modules every variant carries, after its platform’s modules and before its own\. The
installer does not carry them: it holds no config, so the extensions that read config
have nothing to do there; ` installer.modules ` are its own\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### nixpkgs



nixpkgs whose ` lib.nixosSystem ` evaluates the images and whose packages they are built from\.



*Type:*
raw value



*Default:*
the nixpkgs chalkos is tested against

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### osCA



Public part of the cluster’s secrets (` secrets.pub.json ` from ` chalkctl gen secrets `,
version 3), whose OS CA every image and the installer carry, so
chalkd in maintenance mode accepts only clients with a certificate from it\. null builds
images that accept any client until they are installed\.



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
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### platforms



The platforms every variant is built for, exactly those named: a list of names, or an
attribute set of each platform’s NixOS modules\. Either form replaces the default, so
` { dell-r650 = [ ./r650.nix ]; } ` builds dell-r650 alone, and ` metal ` and ` kvm ` are built
only when named\. chalkos has modules of its own for ` metal ` and ` kvm `, which their images
carry before the modules given; any other name is a platform of the modules given alone\.
An image carries one platform’s drivers, agents and console; a node refuses an image of
another platform than its own\. A platform’s name becomes ` CHALKOS_PLATFORM ` in its
images’ os-release\.



*Type:*
(list of string matching the pattern \[a-z0-9]\[a-z0-9-]\*) or attribute set of list of module



*Default:*

```nix
[
  "metal"
  "kvm"
]
```



*Example:*

```nix
{
  metal = [ ./firmware.nix ];
  dell-r650 = [ ./r650.nix ];
}

```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### revision



The git revision the images are built from, which every image directory’s and the
installer’s ` metadata.json ` record and chalkctl shows beside an image’s root hash\. It never
reaches an image’s store, so it changes no root hash, and nothing orders or refuses images
by it: a node identifies an image by its root hash alone\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
self.rev or self.dirtyRev or null
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### specialArgs



Arguments every image’s NixOS modules receive besides NixOS’s own, such as a flake’s inputs\.



*Type:*
attribute set of raw value



*Default:*

```nix
{ }
```



*Example:*

```nix
{ inherit inputs; }
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### system



Platform the images are built for, as Nix names it\.



*Type:*
string



*Default:*

```nix
"x86_64-linux"
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### tag



The git tag the images are built from, such as a release’s, recorded and shown like
` revision `\. Nix cannot read git tags, so whatever builds a release passes it\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"v1.5.0"
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### variants



Variants of the cluster’s images, by name: each is built for every platform as
` images.<variant>-<platform> `\. A variant’s name becomes ` CHALKOS_VARIANT ` in its images’
os-release and ` variant ` in their ` metadata.json `\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{
  node = { };
}
```



*Example:*

```nix
{
  node = { };
  storage = {
    kubernetes = false;
    modules = [ ./storage.nix ];
  };
}

```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### variants\.\<name>\.kubernetes



Whether the variant’s images run Kubernetes\. One image serves control planes and
workers: each node’s Kubernetes Node document makes it one or the other\.



*Type:*
boolean



*Default:*

```nix
true
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



### variants\.\<name>\.modules



NixOS modules of the variant’s images, after those of its platform and of every variant\.



*Type:*
list of module



*Default:*

```nix
[ ]
```

*Declared by:*
 - [lib/images-arguments\.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)



## Image options

The options the node modules declare in every image and in the installer.

### chalkos\.cni\.flannel\.image

Image of flannel\.



*Type:*
string



*Default:*

```nix
"ghcr.io/flannel-io/flannel:v0.28.9"
```

*Declared by:*
 - [modules/node/cni\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)



### chalkos\.cni\.plugins



CNI reference plugins the image ships, in containerd’s plugin directory\. With
` flannel ` they are bridge, host-local, loopback and portmap, which its network runs,
besides flannel’s own plugin; with ` none ` every reference plugin nixpkgs builds, since
the cluster’s own network may use any\. Definitions add to these; ` lib.mkForce `
replaces them\.



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
 - [modules/node/cni\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)



### chalkos\.cni\.provider



Pod network of an image with Kubernetes\. ` flannel ` connects pods across nodes with
VXLAN, configured by the node’s Flannel document; ` none ` installs no network, for
clusters whose Addon documents bring their own, and ships every CNI reference plugin
(see ` chalkos.cni.plugins `)\.



*Type:*
one of “flannel”, “none”



*Default:*

```nix
"flannel"
```

*Declared by:*
 - [modules/node/cni\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/cni.nix)



### chalkos\.debug\.tools



Add coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system path,
and crictl on images with Kubernetes; systemd, bash, less, nftables and kmod are on it
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



### chalkos\.effects



What a delivery does to the node’s services, by a name of the effect’s own\. Once a
delivery is applied, chalkd takes every effect one of whose ` outputs ` changed, the
documents of one of whose ` kinds ` did, or whose ` when ` holds: it reloads the units
of their ` reload `, runs their ` run `, restarts the units of their ` restart `, and acts on
its own loops as their ` chalkd ` says\. An effect exists only when it is declared\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.chalkd



What the effect does to chalkd’s control-plane loops, which run inside chalkd and have
no unit of their own: ` reload ` starts the control plane’s loop again, which reads the
share and applies the control plane’s objects, and ` restart ` every Kubernetes loop,
which read the cluster when they start\.



*Type:*
null or one of “restart”, “reload”



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.kinds



Config kinds, by name as for a grant\. The effect fires when the documents of one of them
changed: one was added, removed or changed, or a value of a Secret one of them
references changed\. A service that reads its kinds’ documents when it starts needs no
output in between\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "Peer"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.outputs



Names of outputs of ` chalkos.outputs `\. The effect fires when one of them changed: when
the hash of its files differs from the one before the delivery, or the values of the
Secrets its input documents reference do, so a rotated password fires the effect even
though the rendered files stay the same\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "bird-conf"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.reload



Units the effect reloads with ` systemctl reload `, before chalkd restarts any\. chalkd reloads only a unit that is active, and not one it restarts\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "systemd-networkd.service"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.restart



Units the effect restarts, by their full names\. chalkd restarts the units of every due
effect in one ` systemctl try-restart `, those of an effect with ` start ` in one
` systemctl restart `, and a unit that failed with ` reset-failed ` and ` restart `\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "bird.service"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.run



Command, with its arguments, the effect runs after the reloads and before the restarts\. chalkd splits it on spaces, with no shell quoting, and runs the first word: a bare program name of chalkd’s PATH, or an absolute path in the image’s store\. chalkd runs it whether or not any unit runs, and a command that fails fails the delivery, so one that talks to a service that may be stopped checks first that the service runs\.



*Type:*
null or non-empty string



*Default:*

```nix
null
```



*Example:*

```nix
"chronyc reload sources"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.start



Start the units of ` restart ` that do not run, with ` systemctl restart ` instead of ` try-restart `, as for a unit whose condition only the delivery meets\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.effects\.\<name>\.when



CEL expression that is true when a delivery takes the effect, for a trigger that no
output renders; the effect fires when ` when ` holds, one of ` outputs ` changed or the
documents of one of ` kinds ` did\. It reads ` config ` and
` oldConfig `, the node’s config after and before the delivery, each by apiVersion, kind
and name with every Secret value replaced by its SHA-256, the node’s name as
` node.name `, and ` facts.<name> ` after the delivery and ` oldFacts.<name> ` before it\.



*Type:*
null or non-empty string



*Default:*

```nix
null
```



*Example:*

```nix
"facts.share != oldFacts.share"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.facts



Facts about the node that change rules read as ` facts.<name> `, each from a file or a
command\. chalkd loads a fact when a rule first reads it and keeps it for the delivery: a
file is read once, and a command runs once for each config it reads; chalkctl
runs no rule that reads one\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.facts\.\<name>\.command



Program of the image’s store, with its arguments, that prints the fact as JSON\. It reads
` {"config": …, "oldConfig": …} ` and its inputs as JSON on standard input, runs through
` systemd-run ` with the system read-only and no network, and must finish within 30
seconds with at most 1 MiB of output\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"${pkgs.my-tool}/bin/my-tool facts"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.facts\.\<name>\.fields



Keys of the file’s JSON object the fact keeps, so rules and their messages see nothing else of the file; empty keeps the whole content, unless ` hashOthers ` is set\.



*Type:*
list of non-empty string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "kind"
]
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.facts\.\<name>\.file



File whose JSON content is the fact: null while the file is missing\. A file that cannot be read, or holds no JSON, refuses the delivery\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"/state/kubernetes/share.json"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.facts\.\<name>\.hashOthers



Keep the other keys of the file’s JSON object too, each as the hex SHA-256 of its value, so rules see whether a value changed but not what it holds, as of a file of private keys\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.facts\.\<name>\.inputs



Delivery inputs the command receives besides the config: ` fallbackSecret `, the fallback secret delivered with it\.



*Type:*
list of value “fallbackSecret” (singular enum)



*Default:*

```nix
[ ]
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



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
a variant’s modules add to them, for example ` [ "gpu" ] `; ` lib.mkForce ` replaces
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
document leaves the field out, or its list is shorter than the index, the command
exits with status 1; without ` name `, one such document fails the whole command\. A
field set to null prints ` null `\. ` * ` over an attribute set prints the values in no particular order; for a
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



### chalkos\.kinds\.\<name>\.rules



Change rules over the node’s documents of the kind, CEL expressions that are true for
every config the node accepts\. ` documents ` and ` oldDocuments ` map document names to
the documents of the kind in the delivered config and in the running one; ` config `,
` oldConfig `, ` node.name ` and ` facts ` are as for every rule\. A rule that is false
refuses the config, or says it takes a reset or a reinstall\.



*Type:*
list of (submodule)



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  {
    rule = "size(documents) <= 1";
    message = "a node is in one rack";
  }
]

```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.rules\.\*\.message



Why a change is refused and what to do instead\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.rules\.\*\.messageExpression



CEL expression that yields the message, with the rule’s variables; ` message ` is used when it fails\.



*Type:*
null or string



*Default:*

```nix
null
```



*Example:*

```nix
"\"the rack moves from \" + oldSelf + \" to \" + self"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.rules\.\*\.rule



CEL expression that is true for every change the node accepts\.



*Type:*
non-empty string



*Example:*

```nix
"self == oldSelf"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.rules\.\*\.takes



What a change the rule refuses needs: ` refuse ` cannot be made, ` reset ` takes ` chalkctl storage reset `, ` reinstall ` a reinstall of the node\.



*Type:*
one of “refuse”, “reset”, “reinstall”



*Default:*

```nix
"refuse"
```

*Declared by:*
 - [modules/node/kinds/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)



### chalkos\.kinds\.\<name>\.spec



Option declarations of the document’s ` spec `, as under a module’s ` options `\. They are
converted into the kind’s JSON Schema: an option without a default is required, and
` description `, ` default ` and ` example ` carry over\. A field declares change rules with
` mkOption { … } // { "x-chalkos-rules" = [ { rule = "self == oldSelf"; message = "…"; } ]; } `,
in the shape of ` rules `, where ` self ` is the field’s new value and ` oldSelf ` its value in
the running config\.



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



### chalkos\.kubernetes\.enable



Whether the image runs Kubernetes: containerd, the kubelet and the preparation of the
node’s Kubernetes configuration\. One image serves control planes and workers: the type
of the node’s Kubernetes Node document makes it one or the other\. mkImages sets it from
the variant’s ` kubernetes `; the installer runs none\.



*Type:*
boolean



*Default:*

```nix
false
```

*Declared by:*
 - [modules/node/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes.nix)



### chalkos\.kubernetes\.package



Kubernetes release of the image\. Nodes run its kubelet; control planes run the
upstream images of the same version\. A Kubernetes upgrade is an image upgrade\.



*Type:*
package



*Default:*

```nix
pkgs.kubernetes
```

*Declared by:*
 - [modules/node/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes.nix)



### chalkos\.kubernetes\.images\.coredns



Image of the cluster DNS\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/coredns/coredns:v1.14.6"
```

*Declared by:*
 - [modules/node/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes.nix)



### chalkos\.kubernetes\.images\.etcd



Image of etcd\. The default is the version kubeadm uses with the release of the default ` chalkos.kubernetes.package `; an image with another release may need another\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/etcd:3.7.0-0"
```

*Declared by:*
 - [modules/node/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes.nix)



### chalkos\.kubernetes\.images\.pause



Image of the pod sandbox\.



*Type:*
string



*Default:*

```nix
"registry.k8s.io/pause:3.10.2"
```

*Declared by:*
 - [modules/node/kubernetes\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kubernetes.nix)



### chalkos\.maintenance\.osCA



Public part of the cluster’s secrets (` secrets.pub.json ` from ` chalkctl gen secrets `),
whose OS CA the image carries, so chalkd in maintenance mode accepts only clients with a
certificate from it\. null builds an image that accepts any client until it is installed\.
mkImages sets it, in every image and the installer, from its ` osCA `, which checks the
file’s version; the image’s build checks it again\.



*Type:*
null or absolute path



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/chalkd\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/chalkd.nix)



### chalkos\.outputs



Config files of the node’s services, by a name of the output’s own, each rendered from
documents by a program of the image’s store\. chalkd renders and checks every output
before a delivery changes anything, and refuses the delivery when one fails; once the
delivery is stored it swaps the files in, and the effects of ` chalkos.effects ` that name
a changed output fire\. ` chalkos-config.service ` renders them at boot, before services
start, and fails when one fails\.



*Type:*
attribute set of (submodule)



*Default:*

```nix
{ }
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.check



Shell commands, run by bash with ` set -e ` in the renderer’s sandbox, with coreutils, grep
and sed on the ` PATH `, that check the rendered files in ` $OUT `; they read the renderer’s
input on standard input and may not write ` $OUT `\. The files are as the renderer wrote
them, before they get the output’s owner and mode\. A failure refuses the delivery
before it changes anything, and at boot fails ` chalkos-config.service `\.



*Type:*
null or strings concatenated with “\\n”



*Default:*

```nix
null
```



*Example:*

```nix
"${pkgs.bird}/bin/bird -p -c $OUT/bird.conf"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.dir



Where services read the output’s files: a link that chalkd replaces in one rename, so
a service reads the files of one render, never a mix of two\. Refer to it as
` config.chalkos.outputs.<name>.dir `, so a misspelled name fails the evaluation\. A
directory at the path is removed when the link first replaces it, so the path is a
clean path below ` /run ` whose components start with no dot, outside ` /run/chalkos `
but for ` /run/chalkos/outputs/<name> ` and the paths below it, and neither another
output’s dir nor inside one\.



*Type:*
string



*Default:*

```nix
"/run/chalkos/outputs/<name>"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.examples



Directory of config files, YAML or JSON, each one example of a node’s documents\. The
image’s build renders and checks the output from each, as the node ` example `, and fails
when one fails\.



*Type:*
null or absolute path



*Default:*

```nix
null
```



*Example:*

```nix
./examples
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.group



Group of the output’s files and directories\.



*Type:*
string



*Default:*

```nix
"root"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.inputs



Config kinds, by name as for a grant, whose documents the renderer reads\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "Peer"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.mode



Mode of the output’s files; its directories are 0755\.



*Type:*
string matching the pattern 0?\[0-7]{3}



*Default:*

```nix
"0644"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.render



Program of the image’s store, with its arguments, that renders the output\. It reads
` {"node": {"name": …}, "documents": {"<Kind>": [<document>, …]}} ` on standard input,
the documents of each kind of ` inputs ` sorted by name, with their schema defaults and
without any Secret’s values, and writes the output’s files into ` $OUT `, the only path
it may write\. It runs as the unprivileged user ` chalkos-render `, without capabilities,
devices or network, on a read-only system without STATE, ` /var `, or the config,
credentials and Kubernetes keys chalkd writes below ` /run/chalkos `, and must finish
within 30 seconds; the files may take 16 MiB, and hold no links\. The same documents
must give the same files: chalkd renders every output again at boot\.
` chalkosLib.render.template ` renders a Go template, in which a key a document lacks, or
that holds null, prints as nothing\.



*Type:*
non-empty string or package convertible to it



*Example:*

```nix
chalkosLib.render.template ./bird.conf.tmpl
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.outputs\.\<name>\.user



Owner of the output’s files and directories\.



*Type:*
string



*Default:*

```nix
"root"
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)



### chalkos\.platform\.name



The platform the image is built for, a name of mkImages’s ` platforms `, which mkImages
sets; null on the installer, which installs images of every platform\. A node refuses an
image of another platform, and translates its config for its own\.



*Type:*
null or string matching the pattern \[a-z0-9]\[a-z0-9-]\*



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/settings\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)



### chalkos\.provenance\.revision



The git revision the image is built from, as ` metadata.json ` records it; mkImages sets
it from its ` revision ` argument\. It never reaches the store, so it changes no root hash,
and nothing orders or refuses images by it\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



### chalkos\.provenance\.tag



The git tag the image is built from, as ` metadata.json ` records it; mkImages sets it
from its ` tag ` argument\. Like the revision, it only says where the image came from\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/base/disk\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/disk.nix)



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
directory’s ` metadata.json ` as ` variant `\. mkImages sets it to the variant’s name\. null
on the installer, which is no variant\. No check compares it: a node may change its
variant through an upgrade, and the image’s schemas decide which config it takes\.



*Type:*
null or string



*Default:*

```nix
null
```

*Declared by:*
 - [modules/node/settings\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/settings.nix)



### systemd\.services\.\<name>\.chalkos\.kinds



Config kinds the unit reads, by name: attributes of ` chalkos.kinds `, or networkd’s
` Network `, ` NetDev ` and ` Link `\. The documents of a kind of ` chalkos.kinds ` are in its
` dir `, networkd’s below ` /run/chalkos/config/networkd.chalkos.dev `, and ` chalkcfg ` is
on the unit’s ` PATH `\. The values their ` secretRef ` fields name are the unit’s
credentials ` secret_<secret>.<key> `\. A grant never restarts the unit: an effect of
` chalkos.effects ` does\.



*Type:*
list of string



*Default:*

```nix
[ ]
```



*Example:*

```nix
[
  "Peer"
]
```

*Declared by:*
 - [modules/node/runtime/default\.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/default.nix)


