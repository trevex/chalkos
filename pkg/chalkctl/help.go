package chalkctl

// The long descriptions and examples of chalkctl's commands, which its help and the generated
// reference pages show. They are kept together so they read as one text.

const rootLong = `chalkctl builds, signs, installs and operates the nodes of a chalkos cluster. It reads the
cluster definition from a flake, or from a manifest file with --manifest, and talks to chalkd on
each node over mutual TLS. Commands authenticate with the cluster's secrets file, which holds
every CA key, or with a client file (chalkctl config new), which holds a certificate of the
admin, operator or reader role. Each command's help says which of the two it needs. Commands that
accept both take --config, else --secrets, else the client file $CHALKOSCONFIG names, else a
secrets file in the flake directory, else ~/.config/chalkos/config.

A command's flags go after its name, anywhere among its arguments, and -- ends them: every
argument after it is read as a positional argument.`

const rootExample = `  # Generate the cluster's secrets, install and bootstrap a control plane, write a kubeconfig.
  chalkctl gen secrets --recipient age1...
  chalkctl install cp1 --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
  chalkctl bootstrap cp1
  chalkctl kubeconfig`

const genLong = `Generates the files a cluster starts from. Its command needs no secrets file or client file.`

const genExample = `  chalkctl gen secrets --plaintext`

const genSecretsLong = `Generates the cluster's secrets once: the OS CA, which every node and client trusts and which
issues client certificates and the node CA; the node CA; the Kubernetes CAs and keys; and the
secret that nodes' recovery keys derive from. They go to secrets.age, encrypted to the
age recipients given with --recipient (age public keys, SSH public keys or age plugin
recipients), or with --plaintext to secrets.json unencrypted, for files protected by other means.
The public half goes to secrets.pub.json, which the cluster definition's chalkos.cluster.osCA
names so images trust the OS CA. The command refuses to overwrite any of these files: new
secrets would lock out every installed node. It needs no secrets file or client file.`

const genSecretsExample = `  # Encrypt the secrets to an age key and an SSH key.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --recipient "$(cat ~/.ssh/id_ed25519.pub)"

  # Write secrets.json unencrypted, kept out of version control.
  chalkctl gen secrets --plaintext`

const configLong = `Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.`

const configExample = `  chalkctl config new --name alice --role operator`

const configNewLong = `Writes a client file: a new key with a certificate from the OS CA for --name and --role, the OS
CA that nodes' certificates chain to, and the nodes' addresses. A client file works for status,
logs, disks, reboot, storage reset, bootstrap, the etcd commands and upgrade; every other command
needs the secrets file. The role decides what chalkd allows: a reader may read nodes'
information, disks, status and logs and etcd's members; an operator may also reboot, upgrade,
drain and uncordon nodes; an admin may call every method of chalkd except RenewNodeCertificate,
which needs the node role (nodes call it to renew their own certificates). The file is written to
~/.config/chalkos/config unless --out names another; commands read it from there, or from the
path --config or $CHALKOSCONFIG names. The command needs the secrets file.`

const configNewExample = `  # A reader's client file for a dashboard.
  chalkctl config new --name grafana --role reader --ttl 720h --out grafana.json

  # The operator's own client file, at ~/.config/chalkos/config.
  chalkctl config new --name alice --role operator`

const storageLong = `Manages the volumes of an installed node. Its command needs an admin client file or the
secrets file.`

const storageExample = `  chalkctl storage reset w1 data`

const nodeLong = `Manages the node certificate of an installed node. Its command needs the secrets file.`

const nodeExample = `  chalkctl node renew w1`

const nodeRenewExample = `  chalkctl node renew w1`

const nodeCALong = `Manages the node CA, which issues the certificates nodes serve. Its command needs the secrets
file.`

const nodeCAExample = `  chalkctl node-ca rotate`

const nodeCARotateLong = `Issues a new node CA from the OS CA, writes it to the secrets file and delivers it to every
control plane; the control planes issue node certificates from it from then on. Node certificates
of the old node CA stay valid until they expire, because they chain to the same OS CA. The
secrets file is updated in place, keeping its previous version as <file>.prev, unless --out names
a new file. The command needs the secrets file.`

const nodeCARotateExample = `  chalkctl node-ca rotate

  # Write the changed secrets to a new file and keep the old one.
  chalkctl node-ca rotate --out secrets.new.age --public-out secrets.pub.new.json`

const etcdLong = `Manages the members of the cluster's etcd, which runs on the control planes. etcd members
needs a reader client file, and etcd remove-member and etcd leave need an admin client file; the
secrets file works for all three.`

const etcdExample = `  chalkctl etcd members`

const etcdMembersLong = `Lists etcd's members the way one control plane's own member sees them: name, ID, peer URLs,
whether a member votes or is a learner, and its health. The first control plane that answers is
asked, or --via. The command needs a reader client file or the secrets file.`

const etcdMembersExample = `  chalkctl etcd members --via cp2`

const etcdRemoveMemberLong = `Removes another node's etcd member, named by its node or its member ID: for example the member
of a node that is gone. The removal is refused when the voters left would have fewer healthy
members than their quorum, unless --force is given. A control plane other than the removed one
does the removal: --via, or the first one that answers. The command needs an admin client file
or the secrets file.`

const etcdRemoveMemberExample = `  # Remove the member of cp3, whose machine failed.
  chalkctl etcd remove-member cp3

  # Remove a member by its ID.
  chalkctl etcd remove-member 8e9e05c52164694d --via cp1`

const etcdLeaveLong = `Takes a control plane out of etcd: the node releases its virtual IPs, removes its own member
with the same quorum guard as remove-member, stops its static pods (etcd and the Kubernetes
control-plane components), deletes its etcd data and unpins its addresses. It joins the cluster
again only once it is reinstalled. A node whose own member does not answer (for example after it
lost its pinned address) leaves only with --force, through the other members. The command needs
an admin client file or the secrets file.`

const etcdLeaveExample = `  chalkctl etcd leave cp3`

const bootstrapLong = `Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies the cluster's manifests. chalkctl waits until
they are applied, up to --timeout. The node refuses when it is bootstrapped already or holds etcd
data, so a second cluster is never initialised; the other control planes join the first. The
command needs an admin client file or the secrets file.`

const bootstrapExample = `  chalkctl bootstrap cp1`

const kubeconfigLong = `Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl. The file holds the certificate's private key and is written with mode
0600. --server points clients at another URL than the cluster endpoint (a forwarded port, for
example), while they still verify the API server's certificate for the endpoint. The command
needs the secrets file.`

const kubeconfigExample = `  chalkctl kubeconfig --out lab.kubeconfig

  # Through a port forwarded to the API server.
  chalkctl kubeconfig --server https://127.0.0.1:6443 --out -`

const rotateExample = `  # Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
  # trust the new CA before it issues certificates. It pauses again after the refresh.
  chalkctl rotate kubernetes-ca
  chalkctl rotate kubernetes-ca --resume
  chalkctl rotate kubernetes-ca --finish`

const upgradeExample = `  # Build each node's image from the flake and upgrade the whole cluster.
  chalkctl upgrade

  # Upgrade the workers of an image's role, two at a time, with a prebuilt image, which
  # chalkctl signs.
  chalkctl upgrade --image ./worker-image --max-unavailable 2 --sign-key db.key --sign-cert db.crt`

const signLong = `Signs the boot loader and the UKIs on the EFI system partition of a raw disk image, in place,
with a Secure Boot db key, so firmware that trusts its certificate boots the image.
--repart-json is the repart-output.json that describes the image's partitions. Ctrl-C stops the
signing and can leave the image partly signed. The command needs no secrets file or client file.`

const signExample = `  chalkctl sign --image chalkos.raw --repart-json repart-output.json --key db.key --cert db.crt`

const installLong = `Installs a node that waits in maintenance mode: a machine that booted the installer, or a role
image booted for the first time. The node serves a self-signed certificate in maintenance mode, so
chalkctl verifies it by the SHA-256 fingerprint the node prints on its console (--fingerprint),
or accepts any certificate with --insecure and prints the fingerprint it saw, which can be checked
against the console afterwards; the secrets go over a second connection pinned to that
fingerprint.

chalkctl sends the node its identity from the cluster definition, a node certificate, the OS CA,
the secret of its encrypted volumes' second keyslot (its recovery key, or a password) and, on a
Kubernetes role, its Kubernetes share. A node that runs its role image already installs in place.
A node that runs the installer gets the role image of its platform as well: --image, or the image
chalkctl builds from the flake, signed for Secure Boot with --sign-key and --sign-cert when
given. The installer writes it to the disk the node's identity names: a disk on which blkid finds
no signature, or one holding an unfinished install of the node's role, which it continues; any
other disk only with --wipe-disk. The node reboots into the installed image. The command needs
the secrets file.`

const installExample = `  # Install cp1, comparing the certificate with the fingerprint on its console.
  chalkctl install cp1 --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

  # Install w1 at an address of the network it booted into, with a prebuilt image, which
  # chalkctl signs.
  chalkctl install w1 --endpoint 192.168.1.50 --insecure --image ./worker-image \
    --sign-key db.key --sign-cert db.crt`

const disksLong = `Lists a node's disks with their use, the partitions on them, and the properties a storage
definition selects disks by: size, type, model, serial number and WWN. An installed node is
reached with a reader client file or the secrets file; a node in maintenance mode with
--fingerprint or --insecure as well. A node that the cluster definition does not name yet is
reached at --endpoint, in maintenance mode, and needs no secrets file or client file.`

const disksExample = `  # The disks of an installed node.
  chalkctl disks w1

  # The disks of a machine that booted the installer and is not in the cluster definition yet.
  chalkctl disks --endpoint 192.168.1.50 --insecure`

const applyIdentityLong = `Delivers a node's identity from the cluster definition to the installed node: its hostname,
networks, labels, taints, storage, time servers, Kubernetes settings (node name, addresses and
subnets) and extensions. The node applies additive storage changes (a new volume, for example)
while it runs. When the identity changes a volume destructively, the node refuses the whole
identity and applies none of it; chalkctl storage reset recreates such a volume. Otherwise the
node restarts the units that read what changed, and chalkctl prints the changes and the units.
--kubernetes-share also delivers a new Kubernetes share: for example a worker's new kubelet
certificate. The command needs the secrets file.`

const applyIdentityExample = `  # Apply w1's changed network and labels.
  chalkctl apply-identity w1

  # Give w1 a new kubelet certificate.
  chalkctl apply-identity w1 --kubernetes-share`

const resetVolumeLong = `Wipes one volume of a node and creates it again, empty, the way the node's identity defines it.
Its data is lost. A volume the identity encrypts is sealed to the TPM again and, unless the
node's fallback is none, gets the second keyslot back: the node's recovery key or its password.
A volume the identity leaves unencrypted is created without encryption. The command needs the
cluster definition, which defines the volume, and an admin client file or the secrets file. A
node whose fallback is its recovery key needs the secrets file, from which the key is derived.`

const resetVolumeExample = `  # Recreate the data volume of w1.
  chalkctl storage reset w1 data`

const statusLong = `Shows an installed node's status: the identity it runs and whether it is the one the cluster
definition gives; its platform; the image version it booted and whether that boot was found
healthy; an upgrade that is installed and boots next; an upgrade that failed and was rolled
back, with the journal of the boot that failed; its volumes and their state; Kubernetes; the
certificates it serves and when they expire; the CAs it trusts; its clock; and the units that
failed. The command needs a reader client file or the secrets file.`

const statusExample = `  chalkctl status cp1

  # With the client file a lab writes.
  chalkctl status cp1 --config ~/.local/state/chalklab/lab/chalkctl.json`

const logsLong = `Prints a node's journal of the current boot, of every unit or of --unit alone; with --follow it
keeps printing new entries until interrupted. The command needs a reader client file or the
secrets file. A node in maintenance mode is reached with --fingerprint or --insecure as well.`

const logsExample = `  # Follow chalkd's log on cp1.
  chalkctl logs cp1 --unit chalkd.service -f`

const rebootLong = `Reboots a node, installed or in maintenance mode. An installed node does not drain its
Kubernetes pods first. The command needs an operator client file or the secrets file; a node in
maintenance mode is reached with --fingerprint or --insecure as well.`

const rebootExample = `  chalkctl reboot w1`

const recoveryKeyLong = `Prints the recovery key of a node, which unlocks its encrypted volumes when the TPM does not:
for example after a change of the Secure Boot keys or state, which the TPM measures in PCR 7. It
is derived from the secrets file's recovery secret, the cluster's name and the node's name, so no
file stores it. The command needs the secrets file.`

const recoveryKeyExample = `  chalkctl recovery-key w1`

const upgradeLong = `Installs on each node the image of its role and platform: control planes one at a time, each
only while etcd keeps its quorum without it, then workers and nodes without Kubernetes in batches
of --max-unavailable. Without --image the nodes (every node of the cluster, or those --nodes
names) are grouped by the role and platform they run, and each group's image is built from the
cluster definition; with --image the nodes of the image's role and platform get it, and those of
its role on another platform are skipped and named. A node that runs on another platform than
the cluster definition declares stops the run before any node is sent anything.

A node gets the image in its inactive slot, is cordoned and drained within its pods'
PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found healthy
and the node is Ready. A node whose image never becomes healthy falls back to the image before,
and the run stops there, showing what that boot logged; with --retry-failed a node that fell
back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. The command needs an operator client file
or the secrets file. Without --image it builds the images from the flake, so it runs in the
flake's directory or with --flake.`

const rotateLong = `Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept (every node trusts the new value besides the old one),
switch (the new value issues or signs), refresh (what the old value issued is issued again) and,
with --finish, finish (the old value is removed, and whatever it issued is refused from then on).
The secrets file records the phase reached and is updated in place, keeping its previous version
as <file>.prev, unless --out names a new file; an encrypted file is encrypted again to the
recipients it records inside, which --recipient replaces. A rotation that stopped (at an
unreachable node, for example) or paused for the operator continues with --resume. One rotation
runs at a time, and one chalkctl command at a time changes the secrets file, holding <file>.lock.
The command needs the secrets file.`

const nodeRenewLong = `Issues the node a new node certificate and key from the node CA and delivers them, also once the
node's own certificate expired. Such a node is verified by the OS CA as of its certificate's
start, so chalkctl trusts the node's old key: someone holding a leaked, expired key of the node
and sitting in its network path could receive the new certificate in its place. That is inherent
to recovering a node; renewing node certificates before they expire avoids it. The command needs
the secrets file.`
