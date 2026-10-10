# A chalkos lab

A cluster of one control plane and one worker, run by `chalklab` as QEMU virtual machines on this
machine, with Secure Boot and a TPM each. The getting-started tutorial of the chalkos
documentation, https://trevex.github.io/chalkos/, walks through it.

You need an x86-64 Linux machine with Nix and flakes, about 5 GiB of free memory (3 GiB for the
control plane, 2 GiB for the worker), and access to `/dev/kvm`, which on most distributions means
being in the `kvm` group; not root.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalklab, chalkctl and kubectl of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
chalkctl gen secrets --plaintext
# Commit flake.lock with the rest, so the lab keeps the same chalkos version.
git add . && git commit -m "A chalkos lab"
# Build the images, start the VMs, install and bootstrap the cluster.
chalklab create
```

`chalklab create` prints the next commands: the kubeconfig to use, `chalkctl status` with the
lab's client file, `chalklab status` and `chalklab destroy`. `chalklab status` names the lab's
files: the kubeconfig, the client file and the Secure Boot db key and certificate the lab's
firmware trusts. After a reboot of this machine, `chalklab start` starts the lab again.

To upgrade, edit `cluster.nix` and let chalkctl build each node's image, sign it with the lab's
db key and install it, with the files `chalklab status` names:

```sh
chalkctl upgrade --config <client file> --sign-key <db key> --sign-cert <db certificate> --allow-downtime
```

The lab has a single control plane: while it reboots, etcd and the API server are down, which
`--allow-downtime` accepts.

An image built otherwise is signed for the lab with `chalklab sign <image> --out <role>-image` and
installed with `chalkctl upgrade --image <role>-image` and the same `--config`.
