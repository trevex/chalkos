package chalkctl

// The long descriptions and examples of chalkctl's commands, which its help and the generated
// reference pages show. They are kept together so they read as one text.

const rootLong = `chalkctl installs and operates the nodes of a chalkos cluster. It reads the cluster from an
inventory (chalkos.yaml in the current directory, or --inventory), which names each node, the
address chalkctl reaches it at, the image it runs and the config files it gets, and talks to chalkd
on each node over mutual TLS. chalkctl never builds or signs images: the inventory names the
directory of each image, for Secure Boot a copy chalkbuild sign signed, or its reference in a
registry, which chalkbuild push prints. Commands authenticate with the cluster's secrets file,
which holds every CA key, or with a client file (chalkctl config new), which holds a certificate
of the admin, operator or reader role. Each command's help says which of the two it needs. The
secrets file is read only where --secrets names it, as a path or as - for standard input,
encrypted with age or not; the inventory names none. Commands that accept both take --config or
--secrets, not both, else the client file $CHALKOSCONFIG names, else ~/.config/chalkos/config.

A command's flags go after its name, anywhere among its arguments, and -- ends them: every
argument after it is read as a positional argument.`

const rootExample = `  # Generate the cluster's secrets, install and bootstrap a control plane, write a kubeconfig.
  chalkctl gen secrets --recipient age1...
  chalkctl install cp1 --secrets secrets.age \
    --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
  chalkctl bootstrap cp1 --secrets secrets.age
  chalkctl kubeconfig --secrets secrets.age`

const genLong = `Generates the files a cluster starts from. Its command needs no secrets file or client file.`

const genExample = `  chalkctl gen secrets --plaintext`

const genSecretsLong = `Generates the cluster's secrets once: the OS CA, which every node and client trusts and which
issues client certificates and the node CA; the node CA; the Kubernetes CAs and keys; and the
secret that nodes' recovery keys derive from. They go to secrets.age, encrypted to the
age recipients given with --recipient (age public keys, SSH public keys or age plugin
recipients), or with --plaintext to secrets.json unencrypted, for files protected by other means.
The public half goes to secrets.pub.json, or to --public-out, which the osCA argument of
mkImages names so images trust the OS CA in maintenance mode. The command refuses to
overwrite any of these files: new secrets would lock out every installed node. With --out - the
secrets file goes to standard output, for a secret manager to keep, the public half to
--public-out, which --out - needs, and what the command says to standard error. It needs no
secrets file or client file.`

const genSecretsExample = `  # Encrypt the secrets to an age key and an SSH key.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --recipient "$(cat ~/.ssh/id_ed25519.pub)"

  # Write secrets.json unencrypted, kept out of version control.
  chalkctl gen secrets --plaintext

  # Hand the encrypted secrets to a secret manager, which supplies them later with --secrets -.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --out - --public-out secrets.pub.json | pass insert --multiline chalkos/secrets`

const configLong = `Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.`

const configExample = `  chalkctl config new --name alice --role operator --secrets secrets.age`

const configNewLong = `Writes a client file: a new key with a certificate from the OS CA for --name and --role, and
the OS CA that nodes' certificates chain to. A client file works for status, logs, disks,
reboot, storage reset, bootstrap, the etcd commands and upgrade; every other command needs the
secrets file. The role decides what chalkd allows: a reader may read nodes' information, disks,
status and logs and etcd's members; an operator may also reboot, upgrade, drain and uncordon
nodes; an admin may call every method of chalkd except RenewNodeCertificate, which needs the
node role (nodes call it to renew their own certificates). The file is written to
~/.config/chalkos/config unless --out names another; commands read it from there, or from the
path --config or $CHALKOSCONFIG names. The command needs the secrets file and no inventory. A
client file names no cluster: its OS CA ties it to one, and a node of another cluster fails the
TLS handshake, which chalkctl reports as a node that does not accept the client file.`

const configNewExample = `  # A reader's client file for a dashboard.
  chalkctl config new --name grafana --role reader --ttl 720h --out grafana.json --secrets secrets.age

  # The operator's own client file, at ~/.config/chalkos/config.
  chalkctl config new --name alice --role operator --secrets secrets.age`

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
  chalkctl node-ca rotate --secrets secrets.age --out secrets.new.age --public-out secrets.pub.new.json`

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
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The node refuses when it is
bootstrapped already or holds etcd data, so a second cluster is never initialised; the other
control planes join the first. The command needs an admin client file or the secrets file.`

const bootstrapExample = `  chalkctl bootstrap cp1`

const kubeconfigLong = `Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl. The file holds the certificate's private key and is written with mode
0600. --server points clients at another URL than the cluster endpoint (a forwarded port, for
example), while they still verify the API server's certificate for the endpoint. The
kubeconfig's cluster, user and context are named after the inventory's metadata.name, or after
--context. The command needs the secrets file.`

const kubeconfigExample = `  chalkctl kubeconfig --secrets secrets.age --out lab.kubeconfig

  # With the context home, to merge into a kubeconfig that holds other clusters.
  chalkctl kubeconfig --secrets secrets.age --context home --out home.kubeconfig

  # Through a port forwarded to the API server.
  chalkctl kubeconfig --secrets secrets.age --server https://127.0.0.1:6443 --out -`

const rotateExample = `  # Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
  # trust the new CA before it issues certificates. It pauses again after the refresh.
  chalkctl rotate kubernetes-ca --secrets secrets.age
  chalkctl rotate kubernetes-ca --secrets secrets.age --resume
  chalkctl rotate kubernetes-ca --secrets secrets.age --finish`

const upgradeExample = `  # Upgrade the whole cluster, each node to the image the inventory names for it.
  chalkctl upgrade

  # Upgrade the nodes that run an image's variant, two at a time, with an image chalkbuild signed.
  chalkctl upgrade --image ./worker-image --max-unavailable 2`

const installLong = `Installs a node that waits in maintenance mode: a machine that booted the installer, or a
node's image booted for the first time. The node serves a self-signed certificate in maintenance
mode, so chalkctl verifies it by the SHA-256 fingerprint the node prints on its console
(--fingerprint), or accepts any certificate with --insecure and prints the fingerprint it saw,
which can be checked against the console afterwards; the secrets go over a second connection
pinned to that fingerprint.

chalkctl sends the node its config, the documents of the files the inventory gives it, checked
against the schemas of the image it installs, a node certificate, the OS CA, the secret of its
encrypted volumes' second keyslot (its recovery key, or a password) and, on a Kubernetes node, its
Kubernetes share. A node that runs its image already installs in place. A node that runs the
installer gets the image as well: --image, or the directory of the node's image in the inventory,
signed with chalkbuild sign when the machine enforces Secure Boot. An image the inventory names in
a registry is refused: install sends only an image from a directory. With Secure Boot on, the
installer checks the UKI and the boot loader against the firmware's db and dbx. It writes the
image to the disk the node's SystemDisk names: a disk on which blkid finds no signature, or one
holding an unfinished install of the node, which it continues; any other disk only with
--wipe-disk. The node reboots into the installed image. The node certificate carries the node's
name in the inventory, which stays the node's name until it is reinstalled. The command needs the
secrets file.`

const installExample = `  # Install cp1, comparing the certificate with the fingerprint on its console.
  chalkctl install cp1 --secrets secrets.age \
    --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

  # Install w1 at an address of the network it booted into, with an image chalkbuild signed.
  chalkctl install w1 --secrets secrets.age --address 192.168.1.50 --insecure \
    --image ./worker-image`

const disksLong = `Lists a node's disks with their use, the partitions on them, and the properties a storage
definition selects disks by: size, type, model, serial number and WWN. An installed node is
reached with a reader client file or the secrets file; a node in maintenance mode with
--fingerprint or --insecure as well. A node that the inventory does not name yet is reached at
--address, in maintenance mode: an image or installer built with an OS CA accepts only a client
with the secrets file or a client file, one built without accepts any client.`

const disksExample = `  # The disks of an installed node.
  chalkctl disks w1

  # The disks of a machine that booted the installer and is not in the inventory yet.
  chalkctl disks --address 192.168.1.50 --insecure --secrets secrets.age`

const applyConfigLong = `Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl first asks the node for the contract of the image it runs: its platform, schemas and
image.json. It refuses an image of another platform than the node's image in the inventory, and
checks the documents as the node does, with the image's change rules that read no facts about the
node against the config the node serves. The node replaces its config as a whole, and applies
additive storage changes (a new volume, for example) while it runs. When the image's rules refuse a
change, the node refuses the whole config, applies none of it, and names the remedy: chalkctl
storage reset recreates a volume in place, once the disk it lives on is plugged in; a change that
would recreate VAR, a removed volume or a move to another disk takes a reinstall; a changed disk
reference that finds no disk needs fixing or its disk plugged in, and one that cannot otherwise be
resolved, as it finds more than one disk or names something other than a whole disk, needs fixing.
Otherwise the node restarts the units that read what changed, and chalkctl prints the changes and
the units. --kubernetes-share also delivers a new Kubernetes share: for example a worker's new
kubelet certificate.

Each image on a node keeps its own config. --staged delivers the config for the image staged to
boot next, as an upgrade with --no-reboot leaves it, instead of the running image: chalkctl checks
it against the node's image in the inventory, and the node checks it against the staged image and
keeps it for that image. Its storage changes count against the node's storage as the running
image's do, so a new volume is created at once and every image's boot mounts it, also the running
image's, whose config must name it the next time it is delivered; nothing restarts. Without
--staged, chalkctl warns of a staged image, whose config stays as it is. A config for the running
image that adds storage the staged image's config lacks is refused: the staged image's config
would then count as removing it, and the node could not write that config again. Deliver the
config with --staged first. The command needs the secrets file.`

const applyConfigExample = `  # Apply w1's changed network and labels.
  chalkctl apply-config w1 --secrets secrets.age

  # Give w1 a new kubelet certificate.
  chalkctl apply-config w1 --secrets secrets.age --kubernetes-share

  # Show what w1's config in the inventory changes on the node, changing nothing.
  chalkctl apply-config w1 --secrets secrets.age --diff

  # Deliver w1's config for the image an upgrade with --no-reboot staged.
  chalkctl apply-config w1 --secrets secrets.age --staged`

const resetVolumeLong = `Wipes one volume of a node and creates it again, empty, the way the node's config defines it.
Its data is lost. A volume the config encrypts is sealed to the TPM again and, unless the
node's fallback is none, gets the second keyslot back: the node's recovery key or its password.
A volume the config leaves unencrypted is created without encryption. The command needs the
inventory, which defines the volume, and an admin client file or the secrets file. A
node whose fallback is its recovery key needs the secrets file, from which the key is derived.`

const resetVolumeExample = `  # Recreate the data volume of w1.
  chalkctl storage reset w1 data --secrets secrets.age`

const statusLong = `Shows an installed node's status: the config it runs and whether it is the one the inventory
gives; its platform; the image it booted, by its store's root hash and the tag and revision it
was built from as far as the node knows them, the boot entry and whether that boot was found
healthy; an upgrade that is installed and boots next; an upgrade that failed and was rolled back,
with the journal of the boot that failed; whether its store is the image the inventory names for
it; its volumes and their state; Kubernetes; the certificates it serves and when they expire; the
CAs it trusts; its clock; and the units that failed. The command needs a reader client file or
the secrets file.`

const statusExample = `  chalkctl status cp1

  # With the inventory and the client file a lab writes.
  chalkctl status cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml \
    --config ~/.local/state/chalklab/lab/chalkctl.json`

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
is derived from the secrets file's recovery secret and the node's name, so no file stores it. The
command needs the secrets file.`

const recoveryKeyExample = `  chalkctl recovery-key w1`

const upgradeLong = `Installs on each node a new image: control planes one at a time, each only while etcd keeps its
quorum without it, then workers and nodes without Kubernetes in batches of --max-unavailable.
Without --image each node (every node of the inventory, or those --nodes names) gets the image the
inventory names for it, which must be the build whose root hash its directory's metadata.json
names, as status compares nodes with it: rebuild the image or link its directory again
otherwise. An image the inventory names in a registry is refused before any node is drained:
upgrade sends only an image from a directory. A node that runs on another platform than its image
stops the run before any node is sent anything. With --image the nodes that run the image's variant get it, or those --nodes
names whatever they run, and nodes on another platform are skipped and named. A node may get an
image of another variant than the one it runs, which chalkctl prints.

Each node gets its config with the image: the documents of the files the inventory gives it,
checked against the image before any node is drained. The node checks them against the image
again and keeps them for it, so a fallback boots the image before with its own config. Storage
changes count against the node's storage as with apply-config: a destructive one is refused, a new
volume is created before the image boots, enrolled with the recovery key, which only the secrets
file gives, or the password --password-file holds. chalkctl asks for each node's password before
it drains the first node, one node after the other. Without an inventory a node carries the config
of the image it runs over, checked against the new image. A node that has the image staged already
gets it again only when the config kept for it is not the inventory's. Before it sends a node the
image, chalkctl prints a line of what the node's config changes against the config of the image
it runs, as apply-config does; --diff prints the whole diff for every node and changes nothing.

Delivering config with the upgrade needs the admin role, which the secrets file and an admin
client file grant. With an operator client file chalkctl sends no config, says so once when there
is an inventory, and each node carries its own over; --diff needs the admin role.

A node gets the image in its inactive slot, is cordoned and drained within its pods'
PodDisruptionBudgets, reboots into the image, and is uncordoned once the boot was found healthy
and the node is Ready. A node whose image never becomes healthy falls back to the image before,
and the run stops there, showing what that boot logged; with --retry-failed a node that fell
back from the image before gets it once more.

Run again, the command skips nodes that run the image and continues one it stopped at; it
uncordons only nodes it cordoned itself. etcd of one or two control planes loses its quorum while
one reboots, and the API server is down, which --allow-downtime accepts. Pods with emptyDir
volumes are evicted only with --delete-emptydir-data. Without an inventory, --address NODE=ADDR
names the nodes, and --image is needed. The command needs an operator client file or the secrets
file.`

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

const validateLong = `Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the files the inventory gives the node form its set, every document is
checked against the schemas of the node's image in the inventory, its directory's or those of the
contract layer of its image in a registry, whose tag chalkctl resolves to a digest once per
command, and the set is translated as the node translates it. The image's change rules that read no facts about a node
run too, without a running config to compare with: the rules of kinds and the rules of fields
that do not compare with the running value. Before any node, it checks that every node whose
files hold a Kubernetes Cluster document holds the same Cluster spec and the same Flannel spec or
none, as every command that reads the inventory does. It prints a line for each node whose config
is valid and names each node whose config is not, and warns of a node whose SystemDisk leaves
STATE, which holds its keys and secrets, unencrypted. It reaches no node and needs no secrets
file or client file.`

const validateExample = `  # Check every node of chalkos.yaml.
  chalkctl validate

  # Check two nodes of another inventory.
  chalkctl validate cp1 w1 --inventory lab/chalkos.yaml`
