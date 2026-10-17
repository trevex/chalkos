# A chalkos lab

A cluster of one control plane and one worker, run by `chalklab` as QEMU virtual machines on this
machine, with Secure Boot and a TPM each. The getting-started tutorial of the chalkos
documentation, https://trevex.github.io/chalkos/, walks through it.

You need an x86-64 Linux machine with Nix and flakes, about 5 GiB of free memory (3 GiB for the
control plane, 2 GiB for the worker), and access to `/dev/kvm`, which on most distributions means
being in the `kvm` group; not root.

The files:

- `flake.nix` builds the image the nodes run, with `chalkos.lib.mkImages`.
- `chalkos.yaml` is the inventory: the nodes, their addresses and their config files.
- `cluster/` holds the config every node gets, `nodes/` each node's own.
- `chalklab.yaml` names the nodes chalklab runs and the MAC address of each.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalklab, chalkctl and kubectl of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
chalkctl gen secrets --plaintext
# Commit flake.lock with the rest, so the lab keeps the same chalkos version.
git add . && git commit -m "A chalkos lab"
# Build the image where the Image document points, and check the nodes' config against it.
nix build .#node-kvm -o images/node-kvm
chalkctl validate
# Start the VMs, install and bootstrap the cluster.
chalklab create --secrets secrets.json
```

`chalklab create` prints the next commands: the kubeconfig to export, the client file to export
as `CHALKOSCONFIG`, `chalkctl status` with the lab's inventory, `chalklab status` and
`chalklab destroy`. `chalklab status` names the lab's files: the kubeconfig, the inventory, the
client file and the Secure Boot db key and certificate the lab's firmware trusts. After a reboot
of this machine, `chalklab start` starts the lab again; a node that powered off stops alone, and
`chalklab start` starts it again.

To change a node's config, edit the YAML of `cluster/` or `nodes/`, check it, and deliver it to
the node. The lab's inventory reads the files of this directory; delivering config needs the
secrets file:

```sh
chalkctl validate
chalkctl apply-config w1 --inventory <lab inventory> --secrets secrets.json
```

To upgrade, change the `node` variant's `modules` in `flake.nix` and commit, build the image again
with `nix build .#node-kvm -o images/node-kvm`, and let chalkctl sign it with the lab's db key and
install it, with the files `chalklab status` names and the client file `CHALKOSCONFIG` names. A
changed image has a new root hash, so there is no version to raise; chalkctl shows the commit the
flake passes to `mkImages` as `revision` beside it:

```sh
chalkctl upgrade --inventory <lab inventory> \
  --sign-key <db key> --sign-cert <db certificate> --allow-downtime
```

The lab has a single control plane: while it reboots, etcd and the API server are down, which
`--allow-downtime` accepts. `chalkctl status` then shows each node running the image
`images/node-kvm` holds, and says so when a node runs another build.
