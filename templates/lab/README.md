# A chalkos lab

A cluster of one control plane and one worker, run by `chalklab` as QEMU virtual machines on this
machine, with Secure Boot and a TPM each. The quick start of the chalkos
documentation, https://trevex.github.io/chalkos/, walks through it.

You need an x86-64 Linux machine with Nix and flakes, about 5 GiB of free memory (3 GiB for the
control plane, 2 GiB for the worker), and access to `/dev/kvm`, which on most distributions means
being in the `kvm` group; not root.

The files:

- `chalkos.yaml` is the inventory: the nodes, their addresses, the image they run and their config
  files. The nodes run the Kubernetes image this chalkos release publishes for VMs,
  `ghcr.io/trevex/chalkos/kubernetes-1.37-kvm`, which chalklab pulls from the registry.
- `cluster/` holds the config every node gets, `nodes/` each node's own.
- `chalklab.yaml` names the nodes chalklab runs and the MAC address of each.
- `flake.nix` pins the chalkos version of the tools, and builds an image of your own, `node-kvm`,
  with `chalkos.lib.mkImages`.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalklab, chalkctl and kubectl of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
chalkctl gen secrets --plaintext
# Commit flake.lock with the rest, so the lab keeps the same chalkos version.
git add . && git commit -m "A chalkos lab"
# Check the nodes' config against the image the inventory names.
chalkctl validate
# Pull the image, start the VMs, install and bootstrap the cluster.
chalklab create --secrets secrets.json
```

`chalklab create` resolves the image's tag to its digest, pulls the image into the lab's state
directory, and names it by that digest in the lab's inventory. It then prints the next commands:
the kubeconfig to export, the client file to export as `CHALKOSCONFIG`, the lab's inventory to
export as `CHALKOSINVENTORY`, `chalkctl status`, `chalklab status` and `chalklab destroy`.
`chalklab status` names the lab's files: the kubeconfig, the inventory, the client file and the
Secure Boot db key and certificate the lab's firmware trusts. After a reboot of this machine,
`chalklab start` starts the lab again; a node that powered off stops alone, and `chalklab start`
starts it again.

The published image carries no OS CA: until chalklab installs a node, the node accepts any client
in maintenance mode. That is fine on the lab's private network. The image `flake.nix` builds
carries the OS CA of `secrets.pub.json`, so in maintenance mode it accepts only the cluster's
clients.

To change a node's config, edit the YAML of `cluster/` or `nodes/`, check it, and deliver it to
the node. The lab's inventory, which `CHALKOSINVENTORY` names, reads the files of this directory;
delivering config needs the secrets file:

```sh
chalkctl validate
chalkctl config apply w1 --secrets secrets.json
```

To run an image of your own, with other modules or packages, change the `node-kvm` image in
`flake.nix` and commit, point the image's source in `chalkos.yaml` at the build,
`node-kvm: {source: {directory: images/node-kvm}}`, build it, and create the lab again:

```sh
nix build .#node-kvm -o images/node-kvm
chalkctl validate
chalklab destroy
chalklab create --secrets secrets.json
```

Once the lab runs your build, upgrade it after a change to `flake.nix`: commit, build the image
again with `nix build .#node-kvm -o images/node-kvm`, sign a copy of it with the lab's db key, and
upgrade the nodes with the copy, with the inventory `CHALKOSINVENTORY` names and the client file
`CHALKOSCONFIG` names. The copy's directory must not exist yet, so remove the copy of an earlier
upgrade first. A changed image has a new root hash, so there is no version to raise; chalkctl
shows the commit the flake passes to `mkImages` as `revision` beside it:

```sh
rm -rf images/node-kvm-signed
chalklab sign images/node-kvm --out images/node-kvm-signed
chalkctl upgrade --image images/node-kvm-signed --allow-downtime
```

The lab has a single control plane: while it reboots, etcd and the API server are down, which
`--allow-downtime` accepts. `chalkctl status` then shows each node running the image
`images/node-kvm` holds, and says so when a node runs another build.
