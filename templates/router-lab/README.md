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

- `flake.nix` builds the two images the nodes run, with `chalkos.lib.mkImages`: the router's,
  with the router layer, and the client's, without a layer.
- `chalkos.yaml` is the inventory: the nodes, the image each runs and its config file.
- `nodes/router.yaml` holds the router's interfaces, firewall, NAT, DHCP server and resolver;
  `nodes/client.yaml` the client's interfaces.
- `chalklab.yaml` names the nodes chalklab runs and the MAC address of each.

```sh
# The flake sees the files git tracks.
git init && git add .
# A shell with chalklab and chalkctl of the chalkos version flake.lock pins.
nix develop
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
chalkctl gen secrets --plaintext
# Commit flake.lock with the rest, so the lab keeps the same chalkos version.
git add . && git commit -m "A chalkos router lab"
# Build the images where the inventory points, and check the nodes' config against them.
nix build .#router-kvm -o images/router-kvm
nix build .#client-kvm -o images/client-kvm
chalkctl validate
# Start the VMs and install the nodes.
chalklab create --secrets secrets.json
```

`chalklab create` prints the next commands: the client file to export as `CHALKOSCONFIG`, the
lab's inventory to export as `CHALKOSINVENTORY`, `chalkctl status`, `chalklab status` and
`chalklab destroy`.

To change the router's config, edit `nodes/router.yaml`, check it, and deliver it on trial, so
that the router rolls it back when chalkctl loses it:

```sh
chalkctl validate
chalkctl config apply router --secrets secrets.json --try 2m
```
