# A chalkos cluster on real machines

A Kubernetes cluster of three control planes that also run workloads, on three amd64 machines
with one Ethernet port each on one network. The nodes have static IPv4 addresses, and the
Kubernetes API answers at a VIP that one control plane holds at a time. The guide "Build a cluster
on real machines" of the chalkos documentation, https://trevex.github.io/chalkos/, walks through
it.

You need three machines with UEFI firmware and a TPM 2.0, four free IPv4 addresses on their
network, a USB stick of at least 1 GiB, and an x86-64 Linux workstation with Nix and flakes.

The files:

- `flake.nix` builds the image the nodes run, `node-metal`, and the installer, with
  `chalkos.lib.mkImages`.
- `chalkos.yaml` is the inventory: the nodes, the address chalkctl reaches each at, the image
  they run and their config files.
- `cluster/cluster.yaml` holds the Kubernetes cluster every node shares, with its VIP, and the
  network flannel takes its VXLAN from.
- `nodes/cp1.yaml` to `nodes/cp3.yaml` hold each node's disk, MAC address and static address.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalkctl, chalkbuild, kubectl and jq of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets, encrypted to your SSH key: secrets.age and secrets.pub.json.
chalkctl gen secrets --recipient "$(cat ~/.ssh/id_ed25519.pub)"
git add . && git commit -m "A chalkos cluster"
# Build the image where the inventory points, and the installer for the USB stick.
nix build .#node-metal -o images/node-metal
nix build .#installer -o installer
```

Replace the addresses of `chalkos.yaml`, `cluster/cluster.yaml` and `nodes/` with free ones of
your network. Boot each machine from the installer, put each node's disk and MAC address into its
file, and check the files with `chalkctl validate`. Install each node with `chalkctl install`, with
`--address` set to the installer's address and the `--fingerprint` its console shows. Then
bootstrap one control plane with `chalkctl bootstrap cp1` and write a kubeconfig with
`chalkctl kubeconfig`.

The template builds its own image, which carries the cluster's OS CA, so in maintenance mode the
nodes accept only the cluster's clients. A cluster that needs nothing of its own can run the
Kubernetes image a chalkos release publishes for physical machines instead. Name it in
`chalkos.yaml` by its digest from the release notes,
`node-metal: {source: {oci: "ghcr.io/trevex/chalkos/kubernetes-1.37-metal:<tag>@sha256:<digest>"}}`,
and boot the machines from the release's installer. The published image carries no OS CA and no
Secure Boot signature, as the Releases page of the documentation describes.
