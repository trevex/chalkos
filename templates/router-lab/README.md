# A chalkos router lab

A router and a client on its LAN, run by `chalklab` as QEMU virtual machines on this machine, with
Secure Boot and a TPM each. The router takes its WAN address by DHCP from QEMU's user-mode
network, leases the LAN's addresses, answers the LAN's DNS queries, masquerades the LAN's IPv4
traffic and filters it with zones and a policy. The router quick start of the chalkos
documentation, https://trevex.github.io/chalkos/, walks through it.

You need an x86-64 Linux machine with Nix and flakes, about 2 GiB of free memory (1 GiB for the
router, 768 MiB for the client), and access to `/dev/kvm`, which on most distributions means being
in the `kvm` group; not root.

The files:

- `chalkos.yaml` is the inventory: the nodes, the image each runs and its config file. Both
  nodes run the router image this chalkos release publishes for VMs,
  `ghcr.io/trevex/chalkos/router-kvm`, which chalklab pulls from the registry; the client gets no
  router documents.
- `nodes/router.yaml` holds the router's interfaces, firewall, NAT, DHCP server and resolver;
  `nodes/client.yaml` the client's interfaces.
- `chalklab.yaml` names the nodes chalklab runs and the MAC address of each.
- `flake.nix` pins the chalkos version of the tools, and builds images of your own with
  `chalkos.lib.mkImages`: the router's, `router-kvm`, with the router layer, and `client-kvm`,
  without a layer.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalklab and chalkctl of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
chalkctl gen secrets --plaintext
# Commit flake.lock with the rest, so the lab keeps the same chalkos version.
git add . && git commit -m "A chalkos router lab"
# Check the nodes' config against the image the inventory names.
chalkctl validate
# Pull the image, start the VMs and install the nodes.
chalklab create --secrets=secrets.json
```

`chalklab create` resolves the image's tag to its digest, pulls the image into the lab's state
directory once for both nodes, and names it by that digest in the lab's inventory. It then prints
the next commands: the client file to export as `CHALKOSCONFIG`, the lab's inventory to export as
`CHALKOSINVENTORY`, `chalkctl status`, `chalklab status` and `chalklab destroy`.

The published image carries no OS CA: until chalklab installs a node, the node accepts any client
in maintenance mode. That is fine on the lab's private network. The images `flake.nix` builds
carry the OS CA of `secrets.pub.json`, so in maintenance mode they accept only the cluster's
clients.

To change the router's config, edit `nodes/router.yaml`, check it, and deliver it on trial, so
that the router rolls it back when chalkctl loses it:

```sh
chalkctl validate
chalkctl config apply router --secrets=secrets.json --try 2m
```

To run images of your own, change them in `flake.nix` and commit, point the router image's source
in `chalkos.yaml` at its build, `router-kvm: {source: {directory: images/router-kvm}}`, build it,
and create the lab again. To run the client on the image without a layer, also add
`client-kvm: {source: {directory: images/client-kvm}}`, give the client `imageName: client-kvm`
and build that image too:

```sh
nix build .#router-kvm -o images/router-kvm
nix build .#client-kvm -o images/client-kvm
chalkctl validate
chalklab destroy
chalklab create --secrets=secrets.json
```
