---
title: "Quick start"
description: "Run a chalkos cluster of two QEMU virtual machines on this machine with chalklab"
---

# Quick start

This tutorial builds a chalkos cluster of one control
plane and one worker on
your machine, runs kubectl against it, upgrades both nodes to a new image and removes the cluster
again. chalklab runs the two nodes as QEMU virtual machines,
each with UEFI firmware, Secure Boot and a
TPM, so they go through the same install, bootstrap and upgrade
steps as real machines. It takes about 15 minutes; most of that is Nix downloading and building the
image the first time.

## Before you begin

You need:

- an x86-64 Linux machine with [Nix](https://nixos.org/download/) and flakes enabled;
- read and write access to `/dev/kvm`, which most distributions give to members of the `kvm`
  group;
- about 5 GiB of free memory, 3 GiB for the control plane and 2 GiB for the worker, and about
  10 GiB of free disk for the Nix store and the lab;
- git.

You do not need root. [Requirements](requirements.md#lab-host) lists the details.

## Create the lab's files

The `lab` template holds a cluster that chalklab can run. Create a directory for it and
initialise it from the template:

```console
$ mkdir lab && cd lab
$ nix flake init -t github:trevex/chalkos#lab
wrote: "/home/alice/lab/.gitignore"
wrote: "/home/alice/lab/README.md"
wrote: "/home/alice/lab/chalkos.yaml"
wrote: "/home/alice/lab/chalklab.yaml"
wrote: "/home/alice/lab/cluster/cluster.yaml"
wrote: "/home/alice/lab/flake.nix"
wrote: "/home/alice/lab/nodes/cp1.yaml"
wrote: "/home/alice/lab/nodes/w1.yaml"
```

Nix builds the image; YAML files describe the cluster.
`flake.nix` pins chalkos as an input, builds the image the nodes run with `mkImages`, and
gives a development shell. `chalkos.yaml` is the inventory: it names the two nodes, the address
chalkctl reaches each at, the image both run, and the files each gets, every file of `cluster/` and
its own file of `nodes/`. The image `node-kvm` is the directory `images/node-kvm`, relative to the
inventory. `all` holds what every node gets: `node-kvm` as its image, and the files of `cluster/`
before its own. `chalklab.yaml` is the lab file, which names the nodes chalklab runs as virtual
machines.

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: lab}
spec:
  images:
    # nix build .#node-kvm -o images/node-kvm puts the image here.
    node-kvm: {source: {directory: images/node-kvm}}
  all: {imageName: node-kvm, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 192.168.123.11, files: [nodes/cp1.yaml]}
    w1: {address: 192.168.123.12, files: [nodes/w1.yaml]}
```

`cluster/cluster.yaml` gives Kubernetes with its defaults and the URL of its API server. The
inventory's `metadata.name` names the cluster in chalkctl's messages and the kubeconfig. Each
node's file says what the node is in Kubernetes, which disk holds its system, and its address on the
lab network, a private network between the virtual machines:

```yaml title="nodes/cp1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: controlplane
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec:
  disk: {path: /dev/vda}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lab}
spec:
  match: {macAddress: "52:54:00:7b:00:11"}
  network: {address: [192.168.123.11/24]}
```

chalklab gives each virtual machine's card on the lab network the MAC address `chalklab.yaml`
names, which the node's `Network` document matches:

```yaml title="chalklab.yaml"
apiVersion: chalklab.chalkos.dev/v1alpha1
kind: Lab
metadata: {name: lab}
spec:
  inventory: ./chalkos.yaml
  nodes:
    cp1: {mac: "52:54:00:7b:00:11"}
    w1: {mac: "52:54:00:7b:00:12"}
```

Nix evaluates a flake in a git repository from the files git tracks, so put the files under git
before anything reads the flake:

```sh
git init && git add .
```

## Enter the development shell

The flake's development shell has chalklab, chalkctl and kubectl, from the chalkos version the
flake pins:

```sh
nix develop
```

The first run writes `flake.lock`, which records that version. Run every later command of this
tutorial in this shell.

## Generate the cluster's secrets

Every cluster has one secrets file with its CAs and
keys, written once by
[`chalkctl gen secrets`](../reference/cli/chalkctl_gen_secrets.md):

```console
$ chalkctl gen secrets --plaintext
wrote secrets.json and secrets.pub.json
warning: secrets.json holds the cluster's secrets unencrypted; protect it by other means, for example keep it out of version control with a .gitignore entry
```

`secrets.json` holds every secret of the cluster in plain text, including the OS
CA's key, which grants admin access to every node. The template's
`.gitignore` keeps it out of git. `secrets.pub.json` is the public part: `flake.nix` names it in the
`osCA` of `mkImages`, so the image trusts the OS CA. A real cluster encrypts its secrets with age:
`--recipient` in place of `--plaintext` writes `secrets.age`, which can live in the repository.

Commit the files, including `flake.lock`, so the lab keeps the same chalkos version:

```sh
git add . && git commit -m "A chalkos lab"
```

## Build the image

The image is built into `images/node-kvm`, where the inventory's image `node-kvm` points.
[`chalkctl validate`](../reference/cli/chalkctl_validate.md) then checks each node's config against
the image's schemas, without a node:

```console
$ nix build .#node-kvm -o images/node-kvm
$ chalkctl validate
cp1: valid for the image node-kvm (node on kvm, root hash 2ece03b0fa09), config 9a7c16c9b2b0e6409c9353ac5551057be4f2cd205fe8fd43f1dbd0c90a4a512d
w1: valid for the image node-kvm (node on kvm, root hash 2ece03b0fa09), config d4f933588f60a6da6b7b33edf93e421474b28d08f0b8dc3d9eed49ab9d2e4850
```

The first build takes most of the tutorial's time. The template's `.gitignore` keeps `images/`
out of git.

## Create the lab

[`chalklab create`](../reference/cli/chalklab_create.md) starts the virtual machines, installs
both nodes and bootstraps the cluster. It reads `chalklab.yaml` and the inventory it names, and
the secrets file that `--secrets` names:

```console
$ chalklab create --secrets secrets.json
creating the lab's Secure Boot keys in /home/alice/.local/state/chalklab/lab/keys
copying the image of cp1 from /home/alice/lab/images/node-kvm
copying the image of w1 from /home/alice/lab/images/node-kvm
starting cp1, w1
wrote /home/alice/.local/state/chalklab/lab/chalkctl.json for chalklab with the admin role; its certificate expires on 2027-10-10
waiting for cp1 to come up in maintenance mode
cp1 runs in maintenance mode with the certificate fingerprint 2db1ac1f7dd6f1088bb5588971225627d90013fbe2c11bc0b8e9713f0b1d8bb1
installing cp1 in place
cp1 is installed and reboots
chalkctl recovery-key cp1 prints the key that unlocks it when its TPM fails
waiting for w1 to come up in maintenance mode
w1 runs in maintenance mode with the certificate fingerprint a0e81810401c142fd8e3780865ed0b01dd17364283643ec1f11d8f9d5b0b9c0d
installing w1 in place
w1 is installed and reboots
chalkctl recovery-key w1 prints the key that unlocks it when its TPM fails
waiting for cp1 to wait for its bootstrap
cp1: kubernetes controlplane: waiting for bootstrap or for the cluster at https://192.168.123.11:6443
bootstrapping cp1; the control plane pulls its images and starts
cp1 is bootstrapped; applied 19 objects
wrote /home/alice/.local/state/chalklab/lab/kubeconfig; its certificate is valid for 8760h0m0s

the lab lab runs cp1, w1; next:
  export KUBECONFIG=/home/alice/.local/state/chalklab/lab/kubeconfig
  export CHALKOSCONFIG=/home/alice/.local/state/chalklab/lab/chalkctl.json
  kubectl get nodes
  chalkctl status cp1 --inventory /home/alice/.local/state/chalklab/lab/chalkos.yaml
  chalklab status
  chalklab destroy
The lab's inventory names unsigned images, which the lab's firmware refuses: sign a copy of an
image with the lab's db key and upgrade the nodes of its variant with it:
  chalklab sign <image dir> --out <signed dir>
  chalkctl upgrade --image <signed dir> --inventory /home/alice/.local/state/chalklab/lab/chalkos.yaml --allow-downtime
```

The output follows the life of a node:

1. Both nodes boot the one image built for the `kvm` platform;
   what differs between them, such as cp1 being the control plane, travels separately as their
   config.
2. chalklab creates Secure Boot keys for this lab, enrols them in the firmware of the virtual
   machines and signs a copy of the image for each node with them. It writes the lab's
   inventory, `chalkos.yaml` in the lab's state, which names the nodes at the ports chalklab
   forwards to them, and an admin client file,
   `chalkctl.json`.
3. Each virtual machine boots its image in
   maintenance mode, where
   chalkd prints its certificate's fingerprint on the console.
   chalklab reads it there and runs [`chalkctl install`](../reference/cli/chalkctl_install.md)
   with `--fingerprint`, so chalkctl talks only to that chalkd. The install creates the node's
   encrypted STATE and VAR
   partitions, sealed to its TPM, writes its config and certificates and reboots it.
4. After the reboot, cp1 waits to be bootstrapped. chalklab runs
   [`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md), which starts etcd and the
   control plane; the worker joins on its own.
5. Last, chalklab writes a kubeconfig that reaches the API server through its forwarded port.

The commands chalklab prints last export the kubeconfig for kubectl and the client file for
chalkctl: chalkctl reads the client file `$CHALKOSCONFIG` names when a command passes neither
`--config` nor `--secrets`.

The lab keeps its state, disks, keys and logs in `~/.local/state/chalklab/lab`, after the lab
file's `metadata.name`, or under `$XDG_STATE_HOME/chalklab` when that variable is set; the
commands on this page use the default path, and `chalklab status` prints the one in use. A
supervisor process keeps the virtual machines running after the command returns.

Each virtual machine has two network cards. One is on the lab network, a switch that connects
only the virtual machines, with the addresses the nodes' `Network` documents give them. The
other is QEMU's user-mode network, which reaches the internet and carries ports that chalklab
forwards from `127.0.0.1` on your machine: each node's chalkd and cp1's API server. The diagram
shows the layout.

```mermaid
flowchart LR
    subgraph host["your machine"]
        kubectl
        chalkctl
        sup["chalklab supervisor"]
    end
    kubectl -->|"forwarded port to 6443"| cp1["cp1 VM, 192.168.123.11"]
    chalkctl -->|"forwarded port to 50000"| cp1
    chalkctl -->|"forwarded port to 50000"| w1["w1 VM, 192.168.123.12"]
    sup -->|runs| cp1
    sup -->|runs| w1
    sup -->|runs| tpm["swtpm, one per VM"]
    sup -->|runs| switch["lab switch, 192.168.123.0/24"]
    cp1 --- switch
    w1 --- switch
```

## Use the cluster with kubectl

The kubeconfig reaches the API server through its forwarded port. Export it and the client file,
as `chalklab create` printed; the commands of this page rely on both. Then list the nodes:

```console
$ export KUBECONFIG=~/.local/state/chalklab/lab/kubeconfig
$ export CHALKOSCONFIG=~/.local/state/chalklab/lab/chalkctl.json
$ kubectl get nodes
NAME   STATUS   ROLES    AGE   VERSION
cp1    Ready    <none>   28s   v1.37.1
w1     Ready    <none>   28s   v1.37.1
```

The nodes may show `NotReady` for the first minute, until flannel, the network plugin, runs on
them. The control plane runs as static pods that chalkd writes, next to the add-ons the bootstrap
applied:

```console
$ kubectl get pods -A
NAMESPACE      NAME                          READY   STATUS    RESTARTS   AGE
kube-flannel   kube-flannel-ds-56wr8         1/1     Running   0          2m9s
kube-flannel   kube-flannel-ds-s6wb6         1/1     Running   0          2m9s
kube-system    coredns-559f6c778d-6592l      1/1     Running   0          2m9s
kube-system    coredns-559f6c778d-gx5sw      1/1     Running   0          2m9s
kube-system    etcd-cp1                      1/1     Running   0          2m12s
kube-system    kube-apiserver-cp1            1/1     Running   0          2m18s
kube-system    kube-controller-manager-cp1   1/1     Running   0          2m12s
kube-system    kube-proxy-99fgz              1/1     Running   0          2m9s
kube-system    kube-proxy-pq4kt              1/1     Running   0          2m9s
kube-system    kube-scheduler-cp1            1/1     Running   0          2m19s
```

## Look at a node with chalkctl

The nodes have no SSH and no login. chalkctl talks to chalkd on each node over mutual TLS instead,
with the client file `$CHALKOSCONFIG` names, and reaches them at the forwarded ports the lab's
inventory names. [`chalkctl status`](../reference/cli/chalkctl_status.md) shows what a node runs:

```console
$ chalkctl status cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml
config 9a7c16c9b2b0e6409c9353ac5551057be4f2cd205fe8fd43f1dbd0c90a4a512d (the inventory's)
platform kvm (the inventory's)
image 81bd02c4a9e1, booted from chalkos_1.efi
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
kubernetes controlplane: bootstrapped, node ready: True, control plane current
certificates:
  node                      expires 2027-10-10
  OS CA                     expires 2036-10-07
  node CA                   expires 2031-10-09
  Kubernetes CA             expires 2036-10-07
  front-proxy CA            expires 2036-10-07
  etcd CA                   expires 2036-10-07
  Kubernetes control plane  expires 2027-10-10
  kubelet client            expires 2027-10-10
  kubelet serving           expires 2027-10-10
trust:
  OS CA                 6c801dfa460f3c12
  Kubernetes CA         03169729fc14b177 (issues)
  front-proxy CA        aa6bf678236985fc (issues)
  etcd CA               cfa2b3cf6ce8bd2c (issues)
  service-account keys  f8dc2d5f576177e6 (issues)
  encryption keys       2eca90c86582dd14 (issues)
time: synchronised to 192.53.103.103, offset +0.000642 s
```

The first lines say that the node runs the config the inventory gives it and the image whose
store's root hash begins with `81bd02c4a9e1`, from its first boot entry, `chalkos_1.efi`. The
certificates section lists the node's certificates and CAs with their expiry
dates; the control plane renews the node's certificates before they expire. The trust section
lists the CAs and keys the node trusts by the start of their fingerprints; `(issues)` marks the
one the control plane signs with, which matters during a
rotation, when two are trusted.

[`chalklab status`](../reference/cli/chalklab_status.md) shows the lab itself: the virtual
machines, their forwarded ports and the lab's files.

```console
$ chalklab status
lab lab in /home/alice/.local/state/chalklab/lab; its supervisor runs as PID 1540233
NODE  TYPE          VM       CHALKD           API SERVER       CONSOLE
cp1   controlplane  running  127.0.0.1:46353  127.0.0.1:43323  /home/alice/.local/state/chalklab/lab/cp1/console.log
w1    worker        running  127.0.0.1:44069  -                /home/alice/.local/state/chalklab/lab/w1/console.log
kubeconfig: /home/alice/.local/state/chalklab/lab/kubeconfig
inventory: /home/alice/.local/state/chalklab/lab/chalkos.yaml
client file: /home/alice/.local/state/chalklab/lab/chalkctl.json
Secure Boot db key: /home/alice/.local/state/chalklab/lab/keys/db.key
Secure Boot db certificate: /home/alice/.local/state/chalklab/lab/keys/db.crt
```

The serial console of a node is where its firmware, kernel and chalkd write.
[`chalklab console`](../reference/cli/chalklab_console.md) prints it and follows it until you
press Ctrl+C. On cp1 it shows chalkd in maintenance mode, the install, and chalkd in normal
mode after the reboot:

```console
$ chalklab console cp1
...
[    7.325509] chalkd[537]: chalkd: maintenance mode, accepting clients of the OS CA; certificate fingerprint 2db1ac1f7dd6f1088bb5588971225627d90013fbe2c11bc0b8e9713f0b1d8bb1
...
[    7.398444] chalkd[537]: chalkd: installing in place
...
[   19.241571] chalkd[537]: chalkd: installed; rebooting
...
[    8.046725] chalkd[544]: chalkd: normal mode; certificate fingerprint c2855a14ec45b6ae2f5fb5bf66f5c480067b6e1484bb84b71e43278b65b6674b
[    8.048971] chalkd[544]: chalkd: addresses 10.0.2.15 192.168.123.11; certificate fingerprint c2855a14ec45b6ae2f5fb5bf66f5c480067b6e1484bb84b71e43278b65b6674b
...
[   33.495178] chalkd[544]: chalkd: kubernetes: applied 19 objects
```

## Change the nodes' config

A node's config is the YAML of its files, which changes without a new image.
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) delivers it to a running
node, which applies it in place. Add a file to `cluster/` that gives every node another time
server:

```yaml title="cluster/time.yaml"
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: time}
spec:
  servers:
    - host: time.cloudflare.com
```

Check the nodes' config against the image, and deliver it to each node. Delivering config needs
the secrets file, which `--secrets` names; a client file does not suffice:

```console
$ chalkctl validate
cp1: valid for the image node-kvm (node on kvm, root hash 2ece03b0fa09), config fdf98e0f1ca7c61fb4eecd3e6bb260c5e8c343adee34d499a2b5407e7eedbc9c
w1: valid for the image node-kvm (node on kvm, root hash 2ece03b0fa09), config 3731f10df5beb905747533b2726ac8727fe1073068fe705f73919a62663b288e
$ chalkctl apply-config cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml --secrets secrets.json
cp1 runs config fdf98e0f1ca7c61fb4eecd3e6bb260c5e8c343adee34d499a2b5407e7eedbc9c
$ chalkctl apply-config w1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml --secrets secrets.json
w1 runs config 3731f10df5beb905747533b2726ac8727fe1073068fe705f73919a62663b288e
```

The lab's inventory takes the files of `cluster/` and `nodes/` from this directory, so it sees
the new file. `chalkctl status` now shows each node's new config version as the inventory's.

## Upgrade the cluster

A node changes its software only by booting a new image. An
upgrade writes the new image into the node's inactive
slot and reboots into it; if the new image does not become
healthy, the node falls back to the image it ran before.

Any change to an image's modules makes a new image, which its store's root hash tells apart from
the one before; there is no version to raise. Edit `flake.nix` so the variant `node` turns on
[`chalkos.debug.tools`](../reference/options.md#chalkosdebugtools), which adds the tools
`kubectl debug` uses on a node:

```diff
-        variants.node = { };
+        # The tools for kubectl debug.
+        variants.node.modules = [ { chalkos.debug.tools = true; } ];
```

Commit the change and build the image again; `images/node-kvm` then points at the new build. The
flake passes its commit to `mkImages` as `revision`, so chalkctl can say which commit each node
runs:

```sh
git commit -am "Add the debug tools"
nix build .#node-kvm -o images/node-kvm
```

The lab's firmware boots only images signed with the lab's Secure Boot key, and the build is
unsigned. [`chalklab sign`](../reference/cli/chalklab_sign.md) copies the image directory and
signs the copy with the lab's db key. The copy's directory must not exist yet, so remove the copy
from an earlier upgrade first:

```sh
rm -rf images/node-kvm-signed
chalklab sign images/node-kvm --out images/node-kvm-signed
```

[`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) with `--image` gives the signed copy to
the nodes that run its variant and rolls it through the cluster, one node at a time:

```console
$ chalkctl upgrade --image images/node-kvm-signed \
  --inventory ~/.local/state/chalklab/lab/chalkos.yaml --allow-downtime
upgrading node on kvm to 3fa9c1e2b7d0 (a1b2c3d4e5f6): cp1, w1
cp1: installing 3fa9c1e2b7d0 (a1b2c3d4e5f6)
cp1: installed 3fa9c1e2b7d0 (a1b2c3d4e5f6), which boots next as chalkos_2+3.efi
cp1: 3fa9c1e2b7d0 (a1b2c3d4e5f6) renders and checks any outputs it declares when it first boots; if one fails, the node falls back to the image it runs
cp1: cordoned, evicted 0 pods, kept 6
cp1: rebooting into 3fa9c1e2b7d0 (a1b2c3d4e5f6)
cp1: runs 3fa9c1e2b7d0 (a1b2c3d4e5f6), found healthy
cp1: uncordoned
w1: cordoned, evicted 2 pods, kept 2
w1: installing 3fa9c1e2b7d0 (a1b2c3d4e5f6)
w1: installed 3fa9c1e2b7d0 (a1b2c3d4e5f6), which boots next as chalkos_2+3.efi
w1: 3fa9c1e2b7d0 (a1b2c3d4e5f6) renders and checks any outputs it declares when it first boots; if one fails, the node falls back to the image it runs
w1: rebooting into 3fa9c1e2b7d0 (a1b2c3d4e5f6)
w1: runs 3fa9c1e2b7d0 (a1b2c3d4e5f6), found healthy
w1: uncordoned
upgraded 2 nodes to 3fa9c1e2b7d0 (a1b2c3d4e5f6)
```

`3fa9c1e2b7d0` begins the new image's root hash, and `a1b2c3d4e5f6` the commit it was built from.

`--allow-downtime` is needed because the lab has a single control plane: while cp1 reboots, etcd
has no quorum and the API server is down, and chalkctl refuses that unless told to accept it. In
a cluster of three control planes, the other two keep etcd and the API server running while one
reboots.

The nodes' config is unchanged, so the upgrade needs no `chalkctl apply-config` after it.
chalkctl reads the image's root hash from the image directory on this machine, and
`chalkctl status` compares it with the root hash each node runs.

Each node goes through the same steps. chalkctl sends the image to chalkd, which writes it into
the inactive slot and adds a boot entry with three tries, `chalkos_2+3.efi`, one generation above
the entry the node booted, so systemd-boot boots it first. The node is
drained and reboots into the new image with
boot counting. Once its
health check passes, the boot is
blessed, which removes the counter, and chalkctl
uncordons the node. A node whose new image is not found healthy within its three tries boots the
image before again, and chalkctl stops there and shows what the failed boot logged.

## Check that it worked

`chalkctl status` shows that w1 booted the new image from the blessed entry, which has lost its
`+3`:

```console
$ chalkctl status w1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml
config 3731f10df5beb905747533b2726ac8727fe1073068fe705f73919a62663b288e (the inventory's)
platform kvm (the inventory's)
image 3fa9c1e2b7d0 (a1b2c3d4e5f6), booted from chalkos_2.efi
VOLUME  DISK    MOUNT POINT  STATE
var     system  /var         mounted
kubernetes worker: joined, node ready: True
certificates:
  node             expires 2027-10-10
  OS CA            expires 2036-10-07
  Kubernetes CA    expires 2036-10-07
  kubelet client   expires 2027-10-10
  kubelet serving  expires 2027-10-10
trust:
  OS CA          6c801dfa460f3c12
  Kubernetes CA  03169729fc14b177
time: synchronised to 162.159.200.1, offset -0.000031 s
```

Both nodes are `Ready` in Kubernetes again:

```console
$ kubectl get nodes
NAME   STATUS   ROLES    AGE    VERSION
cp1    Ready    <none>   6m2s   v1.37.1
w1     Ready    <none>   6m2s   v1.37.1
```

The new image has the debugging tools, so a debugging pod can run the node's own `cat` on its
files. `kubectl debug` starts one on w1 and prints the node's os-release, which names the image's
variant and platform:

```console
$ kubectl debug node/w1 -i --image=busybox -- chroot /host /run/current-system/sw/bin/cat /etc/os-release
Creating debugging pod node-debugger-w1-lw2hn with container debugger on node w1.
...
CHALKOS_BOOT_TRIES=3
CHALKOS_PLATFORM=kvm
CHALKOS_VARIANT=node
...
IMAGE_ID=chalkos
IMAGE_VERSION=""
...
```

## Remove the lab

[`chalklab destroy`](../reference/cli/chalklab_destroy.md) stops the virtual machines and removes
the lab's state, including its disks and keys:

```console
$ chalklab destroy
removed the lab in /home/alice/.local/state/chalklab/lab
```

The files and the image stay, so `chalklab create --secrets secrets.json` makes the same cluster
again. A lab you keep stays on disk across a reboot of your machine, but its virtual machines do
not start by themselves;
[`chalklab start`](../reference/cli/chalklab_start.md) starts them again with their disks, TPM
state and ports.

## What next

- [chalkos in five minutes](what-is-chalkos.md) puts what this tutorial showed into one model.
- [Architecture](../how-it-works/node/architecture.md) explains the parts and how a node is installed.
- [Upgrades](../how-it-works/node/upgrades.md) explains slots, boot counting and
  rollback, and [Upgrade a
  cluster](../operate/upgrade-cluster.md) upgrades a real one.
- [Install on bare metal](../operate/install-bare-metal.md) installs chalkos on hardware.
