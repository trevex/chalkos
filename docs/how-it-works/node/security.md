---
title: "Security model"
description: "What Secure Boot, the TPM, the OS CA and client roles protect, and what they do not"
---

# Security model

chalkos protects three things: the code a node boots, the secrets a node keeps on its disks, and
the API through which a node is installed and operated. Secure Boot and dm-verity cover the code,
the TPM covers the disks, and mutual TLS with the cluster's own certificate authority covers the
API.

## What does a node trust?

A node's trust rests on four anchors:

| Anchor | Where it lives | What it vouches for |
| --- | --- | --- |
| The db signing certificate | The firmware's db | The boot loader and the UKI, and through the UKI the whole store |
| The OS CA | The secrets file; its certificate is in every image built with [`osCA`](../../reference/options.md#osca) | Every node certificate and client certificate of the node API |
| The Kubernetes, front-proxy and etcd CAs | The secrets file; a control plane holds them in its Kubernetes share | The control plane's certificates, the kubelets' and admin kubeconfigs |
| The TPM | The machine | That the node booted under the same Secure Boot state it was installed under |

The db key and the OS CA's key never reach a node. Control planes hold the Kubernetes CAs' keys,
with which they issue their own certificates, and the node CA,
an intermediate of the OS CA that may issue only node certificates. The
[Certificates](certificates.md) page lists every certificate these anchors issue.

## How does Secure Boot cover the whole image?

Secure Boot makes the firmware refuse a boot binary that
no certificate in db signed. chalkos signs two binaries per image, systemd-boot and the UKI, and
the UKI's signature reaches every file of the store through one hash on its kernel command line.

The diagram shows the chain from the firmware's db down to a block of the store.

```mermaid
flowchart TD
    db["db certificate in firmware"] -->|verifies| sdboot["systemd-boot"]
    db -->|verifies| uki["UKI: kernel, initrd, command line"]
    sdboot -->|loads| uki
    uki -->|"carries usrhash="| root["root hash"]
    root -->|"dm-verity checks every read"| store["store: erofs, read-only"]
```

The store is an erofs file system on dm-verity: every
block read from it is hashed and checked against a hash tree, and a block that does not match
fails to read. The top of that tree, the root hash, is on
the UKI's command line as `usrhash=`. The UKI is signed, so changing one byte of the store
requires a new root hash, which requires a new UKI, which requires the db key. The kernel modules
live in the store too, so they need no signatures of their own.

Images are built unsigned by Nix and signed afterwards, before an install or upgrade, with
[`chalkbuild sign`](../../reference/cli/chalkbuild_sign.md) on the machine that holds the db key. It
signs a copy of the image directory and checks the copy against the db certificate, as firmware
with that certificate in db would. Keeping the key out of the build
keeps it out of `/nix/store`, which any user of the build machine can read, and out of every
binary cache the store is pushed to. Nodes never hold the db key and never sign anything.

Before a node writes an image's UKI or boot loader, chalkd
checks its signature against the firmware's db and dbx as the firmware will at the next boot,
when Secure Boot is enforced. It refuses a binary that no db certificate signed, one whose digest
or any certificate of its signature dbx lists, and any binary at all while dbx holds an entry it
cannot read. A node therefore never reboots into an image its firmware would refuse.
chalkctl checks no signatures; it sends the image as it is,
and the node's check is the one that refuses an unsigned or wrongly signed image. The
[signing guide](../../build/sign-images.md) creates the key and enrols it.

## What does Secure Boot do for upgrades?

Secure Boot limits what the operator role can install to
images the db key's holder signed. The [Upgrade](../../reference/api.md#method-upgrade) method checks
that an image is built for the node's platform and that its UKI boots the store it carries.
It cannot tell an image the cluster's owners built from one somebody else built. Without Secure
Boot, anyone with an operator certificate can install an image of their own and run it as root,
with STATE and VAR unlocked, because their keys are sealed to PCR 7 alone. With Secure Boot
enforced, the firmware boots only images signed with the db key, so an operator can roll out only
what the key holder signed.

Once it has written the image to the inactive slot, and before the image becomes the next boot,
Upgrade checks the config it carries against the schemas and `image.json` inside the image, read
through dm-verity from the store it wrote. The request carries no schemas of its own, so the check
always uses those the image will translate the config with. A refusal gives the reason in the
reply and in the node's journal, as ApplyConfig's refusals do, and the reason can quote values of
the config.

An upgrade that carries config or a fallback secret needs the admin role, as ApplyConfig does, so
an operator cannot change a node's networks, volumes or `Addon` documents through an upgrade.
An operator's upgrade carries no config, and the node checks the running image's config against
the new image instead; a refusal of that config can still quote its values to the operator.

## Why are the disk keys sealed to PCR 7?

STATE, VAR and encrypted
volumes are LUKS2 volumes whose key the TPM seals to
PCR 7 under the default
encryption mode `tpm2`; the mode
`none` leaves them unencrypted. PCR 7 measures the Secure Boot state: whether it is on, the
contents of PK, KEK, db and dbx, and the db certificate that verified the boot binaries. The TPM
releases the key only when PCR 7 holds the value it was sealed under.

PCR 7 is chosen over the PCRs that measure the kernel and initrd because an
upgrade changes those with every image, while an image signed
with the same db key leaves PCR 7 as it was. Upgrades therefore unlock without anyone typing a
key. The consequences:

- A machine that boots with Secure Boot off, or with a different db, KEK or dbx, measures a
  different PCR 7, and the TPM does not unseal. This includes a firmware update that adds dbx
  entries.
- A disk moved to another machine does not unlock there, because the other TPM does not hold the
  sealed key.
- Any boot whose binaries the same db certificate signed measures the same PCR 7, older chalkos
  images included.

When the TPM does not unseal, the console asks for the second keyslot of each volume: the node's
recovery key by default, or a password. The recovery key
is derived from a secret in the secrets file and the node's name, so no file stores it and
installing a node never changes the secrets file.
[`chalkctl recovery-key`](../../reference/cli/chalkctl_recovery-key.md) prints it:

```sh
chalkctl recovery-key <node>
```

Recovery keys derived before this release took the cluster's name as well, so they differ from
the ones derived now. A node installed before keeps the key it was enrolled with, which
`chalkctl recovery-key` no longer prints; such a node unlocks with that old key until it is
reinstalled. A delivery that adds an encrypted volume to such a node is refused, because chalkctl
sends the new key and the node checks it against the existing fallback keyslot. chalkctl derives
a recovery key and never reads one from `--password-file`, so the delivery goes through only when
the node's [`SystemDisk`](../../reference/kinds.md#systemdisk) document sets `encryption.fallback` to
`password` and `--password-file` holds the old key. The new volume is then enrolled with the old
key as well, and the config keeps `password` until a reinstall of the node enrolls the new key.
Without the old key, only a reinstall adds the volume.

[Storage and encryption](storage.md) describes the keyslots and what happens on a node whose
fallback is `none`.

## Who may call the node API?

chalkd serves the [node API](../../reference/api.md) on TCP port 50000 with TLS 1.3. On an installed
node, in normal mode, it serves its
node certificate and accepts only clients whose
certificate chains to an OS CA the node trusts. It verifies the client's chain again for every
request, against the OS CAs it trusts at that moment, so a client whose certificate expired or
whose OS CA was removed by a rotation loses access on its
next call, even on a connection it keeps open.

A client's role comes from its certificate. A
certificate the OS CA issued directly grants the role its Organization names. A certificate whose
chain passes through the node CA has the role `node`, whatever its Organization says, because the
node CA is the only intermediate the OS CA issues; this keeps a control plane, which holds the
node CA, from issuing itself an admin certificate.

| Role | May call |
| --- | --- |
| reader | Info, Disks, Contract, Status, Logs, EtcdMembers |
| operator | What a reader may, and Reboot, Upgrade without config, DrainNode, UncordonNode |
| admin | Every method except RenewNodeCertificate: also Install, ApplyConfig, GetConfig, Upgrade with config, ResetVolume, Bootstrap, EtcdRemoveMember, EtcdLeave, RotationStep, RevokeNode, UnrevokeNode |
| `node` | RenewNodeCertificate alone, on control planes |

Each role allows what the roles above it in the table allow; `node` is apart and allows only its
one method. A call the role does not allow fails with a message naming the role it needs, such as
`/chalkos.node.v1.NodeService/Reboot needs the operator role`.

An operator may upgrade a node, but an upgrade that carries config or a fallback secret needs the
admin role, as ApplyConfig does. Config names a node's networks, volumes and `Addon` documents,
and a control plane applies the objects of `Addon` documents to the cluster, so config an operator
may not deliver to the running image it may not deliver with the next one either. Without config
the node carries the running image's over to the new image. `chalkctl upgrade` with an operator
client file therefore sends no config and, with an inventory, prints
`config not delivered: the operator role cannot deliver config; each node carries its own over`.
GetConfig, which returns a node's config with its secret values hashed, needs the admin role too:
names, addresses and the shape of the config tell an attacker much about the node.

## Where do clients get their certificates?

chalkctl authenticates with one of two credentials.

The secrets file holds every CA key of the cluster. A command that reads it issues itself an admin
certificate from the OS CA, valid for one hour and never written to disk, so using the secrets file
leaves no admin certificate behind. The file is `secrets.age`, encrypted with age to the recipients
given to [`chalkctl gen secrets`](../../reference/cli/chalkctl_gen_secrets.md) (age keys, SSH keys or
age plugin recipients such as hardware tokens), or `secrets.json` in plaintext with `--plaintext`,
for a file protected by other means. chalkctl reads it only where `--secrets` names it: no inventory
names it, and chalkctl looks for none of its own accord. `--secrets -` reads it from standard input,
encrypted or not, for example from a password manager, so a plaintext copy never touches the disk;
`chalkctl gen secrets --out - --public-out secrets.pub.json` writes a new one to standard output for
such a manager, and its public half to `secrets.pub.json`:

```sh
chalkctl gen secrets --recipient=<age-public-key>
```

A client file holds one certificate of one role, its key
and the OS CA certificates. It lets someone operate the cluster without the
secrets file, limited to the role.
[`chalkctl config new`](../../reference/cli/chalkctl_config_new.md) issues it, valid for one year
unless `--ttl` says otherwise, and writes it to `~/.config/chalkos/config` with mode 0600:

```sh
chalkctl config new --name=grafana --role=reader --ttl=720h --out=grafana.json
```

A client file names no cluster. Its OS CA ties it to one: a node of another cluster serves a
certificate that the file's OS CA does not verify, and refuses the file's certificate, so the TLS
handshake fails and chalkctl says `the node does not accept this client file, <path>; is it another
cluster's?`. A client file issued before an OS CA rotation fails the same way later in the
rotation.

Commands that accept both take the client file `--config` names, else the secrets file `--secrets`
names, else the client file `$CHALKOSCONFIG` names, else `~/.config/chalkos/config`. chalkctl
refuses a client file whose certificate expired and warns during its last 30 days. Installing nodes,
`apply-config`, issuing kubeconfigs and rotations need the secrets file, because they issue
certificates or deliver keys. `chalkctl upgrade` delivers config
with the secrets file or an admin client file, and none with an operator client file.

## Revoking a node cuts off its writes and renewals

On the shipped variant, a revoked node can no longer renew its certificates or change anything in
the cluster. A control plane lists the revoked nodes in its status:

```text
revoked nodes:
  w1  since 2026-10-23T22:42:25Z
```

[`chalkctl node revoke`](../../reference/cli/chalkctl_node_revoke.md) asks a control plane to add
the node's name and the time to the ConfigMap `kube-system/chalkos-revoked-nodes`, and to delete
the node's Node object. The list takes effect in three places:

- A control plane's RenewNodeCertificate refuses a certificate that names a listed node. The
  node's status shows the refusal on its node certificate:
  `renewal failing: the control plane at 192.168.100.11:50000: permission_denied: node w1 is revoked since 2026-10-23T22:42:25Z; chalkctl node unrevoke w1 lets it renew again`.
  The node's certificate has the role `node`, which allows that one method alone, so the node
  can call nothing else.
- chalkd's approver leaves the node's kubelet serving certificate requests pending.
- The ValidatingAdmissionPolicy `chalkos-revoked-nodes` denies every create, update, delete and
  connect by the user `system:node:<name>` of a listed node, on subresources too. The kubelet can
  therefore neither register its Node again nor request a new client certificate:
  `nodes "w1" is forbidden: ValidatingAdmissionPolicy 'chalkos-revoked-nodes' with binding 'chalkos-revoked-nodes' denied request: node w1 is revoked since 2026-10-23T22:42:25Z`.

The policy takes no parameters. chalkd writes the revoked names into the policy's CEL expression
itself, and the policy fails closed. A control plane writes the policy again from the ConfigMap
after each revoke and unrevoke. Every control plane also compares the ConfigMap's resource version
with the one it last applied the policy from, about every 10 seconds, and applies the policy again
when they differ or its last apply failed. So the policy follows a ConfigMap edited by hand, or a
revocation whose apply was cancelled, within about 10 seconds. The API server that chalkctl went
through denies the node's writes within seconds. The other control planes' API servers pick up
the policy shortly after.

`chalkctl node revoke` first asks every other control plane whether it reads the revoked nodes.
A control plane whose chalkd predates node revocation renews the node's certificate regardless,
so the command refuses while one does not answer, cannot read the list, or does not say it read
it. `--force` revokes the node anyway.

chalkctl reads the list as well. `chalkctl rotate` reads it before each phase and skips the listed
nodes, so no rotation hands a revoked node a certificate. `chalkctl node renew` and
`chalkctl apply-config --kubernetes-share` refuse a listed node unless `--force` is given, and
`chalkctl install` refuses to install a node under a listed name unless `--force` is given. All
four refuse to run while no control plane says which nodes are revoked. `chalkctl rotate
--ignore-revoked` rotates without the list, as during an outage of the API server, and every node
of the inventory then takes part. install counts a control plane that does not answer as an
installed node as one not installed yet, so a cluster's first nodes install while the others
wait.

[`chalkctl node unrevoke`](../../reference/cli/chalkctl_node_unrevoke.md) removes the name and
applies the policy again. A node's old certificates chain to the OS CA, which chalkd trusts with
every node CA it signed, and to the Kubernetes CA. So the control plane unrevokes the node only
when every OS CA and Kubernetes CA it trusts was made after the revocation, and refuses otherwise.
`--force` unrevokes it anyway, and the old certificates work again until they expire. The check
compares the revocation time, taken from the control plane's clock, with the time chalkctl made
the CA, taken from the workstation's clock. The two clocks must agree: a workstation clock far
ahead can make a CA from before the revocation pass the check.

### Revocation leaves reads and the certificates valid

A revocation shuts out a node's writes and renewals. It leaves these gaps:

- The node's certificates stay valid until they expire, and the API server's admission sees
  writes alone. The kubelet can still read what every kubelet may: Services, Endpoints and
  EndpointSlices, and a few cluster-scoped kinds such as Nodes and RuntimeClasses. The Secrets and
  ConfigMaps of the pods bound to the node stay readable until the pod garbage collector deletes
  those pods, shortly after their Node is gone.
- A control plane's disk holds the node CA's and the Kubernetes CA's keys. Revoking a control
  plane, or a stolen copy of its disk, does not cut it off. Rotating `os-ca` and `kubernetes-ca`
  does. A former control plane's etcd and front-proxy certificates stay valid until the
  `kubernetes-ca` rotation has finished.
- A cluster without Kubernetes keeps no list. A node there renews its certificate only through
  `chalkctl node renew`, so not renewing it is how it is revoked. `chalkctl rotate os-ca` cuts it
  off at once.
- A renewal already in flight when the revoke lands can still complete, and the node then holds a
  node certificate valid for a year. The policy still denies its kubelet's writes.
- A cluster-admin can edit or delete the ConfigMap and the policy with kubectl. Renewals follow
  the ConfigMap at once, and the policy follows it within about 10 seconds. An edited or deleted
  policy stays so until the ConfigMap changes or chalkd restarts. Deleting the ConfigMap unrevokes
  every node. Editing the ConfigMap by hand skips the checks of chalkctl node revoke and
  chalkctl node unrevoke, so use those commands.
- A control plane renews node certificates only while it can read the list from its API server.
  During a long outage of the API server, renewals stop, the control plane's own included.
  `chalkctl node renew --force` renews a node's certificate without asking the control planes.
- A control plane that still runs a chalkd from before node revocation renews a revoked node's
  node certificate until it is upgraded. The API server denies the node's writes all the same.
  `chalkctl node revoke` refuses while such a control plane runs, unless `--force` is given.
- The list has a size limit. The API server compiles no expression longer than 100,000
  characters, so a control plane refuses a revocation that would make the policy's map longer
  than 90,000: about 2,000 short names, or about 300 names of the longest length Kubernetes
  allows. Unrevoke names once the CAs were rotated, to keep the list short.
- A revoked node left in the inventory is skipped by every rotation, and other commands still
  reach it. Remove it from the inventory.

## How is a node in maintenance mode trusted?

A node that is not installed runs chalkd in maintenance
mode. It has no node certificate yet, so it serves a
self-signed one and prints the certificate's SHA-256 fingerprint and the node's addresses on its
console. chalkctl verifies the node in one of two ways:

```sh
chalkctl install <node> --secrets=secrets.age --fingerprint=<fingerprint>
chalkctl install <node> --secrets=secrets.age --insecure
```

With `--fingerprint`, chalkctl compares the certificate with the fingerprint read off the
console before it sends anything. With `--insecure`, it accepts whatever certificate the node
presents and prints its fingerprint, which the operator compares with the console afterwards: trust
on first use. chalkctl then opens a second connection pinned to that fingerprint and sends the
secrets over it, so the config, the node certificate and the Kubernetes share go to the machine
whose fingerprint was printed. Every command that reaches a node in maintenance mode accepts
`--insecure`.

The node in turn decides whom it accepts. The images and the
installer of an mkImages call with
[`osCA`](../../reference/options.md#osca) carry that OS CA and accept in maintenance mode only
clients with a certificate from it, so only the holder of the secrets file or a client file can
install them. An image built without one accepts any client as an admin until it is installed.
Use images with the OS CA on any network other people can reach.

## Can anyone log in to a node?

No. Nodes have no SSH server, no passwords and no interactive accounts. The root file system is a
tmpfs assembled at boot, `/etc` is a read-only overlay built from the image, and the store is
read-only and checked by dm-verity, so a node changes only through chalkd. Logs come through
[`chalkctl logs`](../../reference/cli/chalkctl_logs.md). A node with Kubernetes is debugged from a
privileged pod, started with `kubectl debug node/<node>`, which sees the node's file systems;
[`chalkos.debug.tools`](../../reference/options.md#chalkosdebugtools) adds the usual command-line
tools to an image for that.

The `kvm` platform's images run the QEMU guest agent, which a hypervisor reaches through a
virtio-serial channel. It answers only `guest-sync-delimited`, `guest-sync`, `guest-ping`,
`guest-info`, `guest-get-osinfo`, `guest-get-host-name`, `guest-get-time`, `guest-get-timezone`,
`guest-network-get-interfaces` and `guest-shutdown`. It never runs commands, or reads or writes
files, for the host, so the hypervisor's admin gets no shell on the node through it.

## Limits

- chalkos does not enrol Secure Boot keys. The operator enrols the db certificate in each
  machine's firmware, as the [signing guide](../../build/sign-images.md) shows; only
  chalklab enrols keys, its own, in its virtual machines.
  Installing does not check that Secure Boot is on.
- There is no certificate revocation list. A leaked client file or kubeconfig stays valid until it
  expires or its CA is rotated; rotation is how access is revoked, as
  [Certificates](certificates.md) explains. Revoking a node shuts out that node alone, with the
  gaps listed under
  [Revocation leaves reads and the certificates valid](#revocation-leaves-reads-and-the-certificates-valid).
- The TPM unseals without a PIN for every boot under the same Secure Boot state, so a machine
  decrypts its own disks whenever it boots. chalkos does not defend against an attacker with the
  machine in hand who attacks it while it runs, for example through its memory.
- PCR 7 does not tell one image signed with the db key from another. An older signed image with a
  known flaw unseals the keys too, until its signature or the key is revoked through dbx.
- The approver of kubelet serving certificates checks a request against the addresses the Node
  object lists. A kubelet reports those addresses itself, so the check stops a node from claiming
  another node's name, not from claiming an address it does not own.
- A `Registry`'s credentials serve the image pulls of every pod on the node. chalkd keeps them in
  a `credentials.json` that only root reads, but the kubelet hands them to containerd for any pod
  whose image names the `Registry`'s host, and containerd sends them to that host's mirror
  endpoints as well, as
  [Kubernetes](../kubernetes/kubernetes.md#how-do-pods-pull-images-from-a-private-registry) describes.
- A node's clock decides whether certificates are valid. A node whose clock is far off refuses
  valid certificates and accepts expired ones until chrony sets it.

## Related pages

- [Certificates](certificates.md): every CA and certificate, renewal, rotation and revocation.
- [Storage and encryption](storage.md): the encrypted volumes and their keyslots.
- [The image](image.md): the store, dm-verity and the UKI.
- [Sign images for Secure Boot](../../build/sign-images.md).
- [Manage access and certificates](../../operate/access.md).
- [Node API](../../reference/api.md), [`chalkctl config new`](../../reference/cli/chalkctl_config_new.md)
  and [`chalkctl gen secrets`](../../reference/cli/chalkctl_gen_secrets.md).
