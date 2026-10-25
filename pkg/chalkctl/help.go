package chalkctl

// The long descriptions and examples of chalkctl's commands, which its help and the generated
// reference pages show. They are kept together so they read as one text: each command's long
// description and its examples, in the order of the command tree.

const rootLong = `chalkctl installs and operates the nodes of a chalkos cluster. It reads the cluster from an
inventory, chalkos.yaml in the current directory or --inventory, which names each node, its
address, its image and its config files. It talks to chalkd on each node over mutual TLS, and never
builds or signs images.

Commands authenticate with the cluster's secrets file, which holds every CA key and which only
--secrets names, or with a client file of the admin, operator or reader role, which chalkctl config
new writes. Each command's help says which it needs. A command that accepts both takes --config or
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

const genSecretsLong = `Generates the cluster's secrets once: the OS CA, the node CA, the Kubernetes CAs and keys, and
the secret that nodes' recovery keys derive from. They go to secrets.age, encrypted to the age
recipients --recipient gives (age public keys, SSH public keys or age plugin recipients), or with
--plaintext to secrets.json unencrypted. The public half goes to secrets.pub.json, or to
--public-out, which the osCA argument of mkImages names. See
https://trevex.github.io/chalkos/how-it-works/node/certificates/.

The command refuses to overwrite any of these files, because new secrets would lock out every
installed node. With --out - the secrets file goes to standard output, for a secret manager to
keep, the public half to --public-out, which --out - needs, and the command's messages to standard
error. The command needs no secrets file or client file.`

const genSecretsExample = `  # Encrypt the secrets to an age key and an SSH key.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --recipient "$(cat ~/.ssh/id_ed25519.pub)"

  # Write secrets.json unencrypted, kept out of version control.
  chalkctl gen secrets --plaintext

  # Hand the encrypted secrets to a secret manager, which supplies them later with --secrets -.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --out - --public-out secrets.pub.json | pass insert --multiline chalkos/secrets`

const nodeLong = `Manages a node's access to the cluster and shows its state: renews its node certificate, revokes
and unrevokes it by name, lists and runs the diagnostics its image declares, and shows a router's
routes. node renew needs the secrets file; node revoke and node unrevoke need an admin client
file or the secrets file; node diag and node routes need a reader client file or the secrets
file.`

const nodeExample = `  chalkctl node renew w1
  chalkctl node revoke w1
  chalkctl node diag w1 route-get address=2001:db8::1
  chalkctl node routes r1 ::/0`

const nodeDiagLong = `Lists the diagnostics a node's image declares, or runs one. A diagnostic is a read-only command of
the image, such as ip route get, that chalkd runs on the node in a sandbox of its own. Without a
diagnostic, the command lists each one's role and description, and below it each parameter: in
brackets when it may be left out. With one, it passes the parameters given as
<parameter>=<value>, which the node checks against their types, and prints what the command
prints on standard output and standard error. It fails when the command fails, runs longer than
its timeout or prints more than 1 MiB.

Every image declares addresses, links, routes, route-get, neighbours, ping, dns-query,
tcp-connect and nft-ruleset; layers and extensions add more. Listing needs a reader client file
or the secrets file, and running a diagnostic the role it lists. A node in maintenance mode is
reached with --fingerprint or --insecure as well.`

const nodeRoutesLong = `Shows the routes of a node of a router image: the routes in its kernel, of the main table unless
--table names another or all, and the paths its BGP instance selected, the best of a prefix
marked *. With a prefix, it shows the routes to that range and to ranges within it; with
--protocol, the kernel's routes of that protocol alone, and BGP's paths only for bgp. A node of
another image answers that the router layer is not in its image. The command needs a reader
client file or the secrets file.`

const nodeRoutesExample = `  # Every route of r1's main table and BGP's paths.
  chalkctl node routes r1

  # Every IPv6 route, in the kernel and in BGP.
  chalkctl node routes r1 ::/0

  # The routes zebra installed from BGP in every table, and BGP's paths.
  chalkctl node routes r1 --protocol bgp --table all`

const nodeDiagExample = `  # List w1's diagnostics.
  chalkctl node diag w1

  # The route w1 takes to an address.
  chalkctl node diag w1 route-get address=2001:db8::1

  # Whether w1 reaches a registry's port.
  chalkctl node diag w1 tcp-connect address=2001:db8::5000 port=443`

const nodeRenewLong = `Issues the node a new node certificate and key from the node CA and delivers them, also once the
node's own certificate expired. Such a node is verified by the OS CA as of its certificate's
start, so chalkctl trusts the node's old key: someone holding a leaked, expired key of the node
and sitting in its network path could receive the new certificate in its place. That is inherent
to recovering a node; renewing node certificates before they expire avoids it.

A node revoked with chalkctl node revoke is refused, as is any node while no control plane says
which nodes are revoked: the control planes are asked first. A control plane whose own
certificate expired cannot answer; --force renews without asking. The command needs the secrets
file.`

const nodeRenewExample = `  chalkctl node renew w1`

const nodeRevokeLong = `Cuts a node off the cluster by name, at once and without a rotation. A control plane lists the
node, with the time of its first revocation, in the ConfigMap kube-system/chalkos-revoked-nodes and
deletes its Node object. From then on the control planes renew none of its certificates, the API
server denies every write by its kubelet, and chalkctl rotate skips it. Its certificates stay valid
until they expire, so its kubelet can still read what every kubelet may. See
https://trevex.github.io/chalkos/how-it-works/kubernetes/certificates/#revoking-a-node-cuts-off-its-writes-and-renewals.

Revoking a control plane does not cut it off, because its disk holds the node CA's and the
Kubernetes CA's keys. The command refuses a node that is still an etcd member, and the control
plane --via names: take it out with chalkctl etcd leave, or chalkctl etcd remove-member once it is
gone, then rotate os-ca and kubernetes-ca. A cluster without Kubernetes keeps no revoked nodes, and
there chalkctl rotate os-ca cuts a node off.

The command first asks every other control plane whether it reads the revoked nodes, and refuses
while one does not answer or does not say it read them. --force revokes the node despite either
refusal. A control plane revokes the node: --via, or the first other one that answers. The command
needs an admin client file or the secrets file.`

const nodeRevokeExample = `  # Cut off w1, which was removed from the cluster.
  chalkctl node revoke w1`

const nodeUnrevokeLong = `Removes a node from the revoked nodes, so a node of that name may join the cluster again. The old
node's certificates would work again too, so the control plane refuses while the OS CA or the
Kubernetes CA predates the revocation, and names the rotations missing. To reuse the name, run
chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish, first; a node
installed afresh under the name then joins as usual. See
https://trevex.github.io/chalkos/how-it-works/kubernetes/certificates/#revoking-a-node-cuts-off-its-writes-and-renewals.

--force unrevokes the node anyway, and its old certificates work again until they expire. The old
node does not come back by itself: its kubelet registers its Node once as it starts, so reboot it
with chalkctl reboot, or restart its kubelet.

A control plane unrevokes the node: --via, or the first one that answers. The command needs an
admin client file or the secrets file.`

const nodeUnrevokeExample = `  # Reuse the name w1 once the rotations of os-ca and kubernetes-ca have finished.
  chalkctl node unrevoke w1

  # Let the old w1 back in, certificates and all.
  chalkctl node unrevoke w1 --force
  chalkctl reboot w1`

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

const configLong = `Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.`

const configExample = `  chalkctl config new --name alice --role operator --secrets secrets.age`

const configNewLong = `Writes a client file: a new key with a certificate from the OS CA for --name and --role, and
the OS CA that nodes' certificates chain to. The file goes to ~/.config/chalkos/config unless --out
names another; commands read it from there, or from the path --config or $CHALKOSCONFIG names. A
client file names no cluster: its OS CA ties it to one, and a node of another cluster fails the TLS
handshake, which chalkctl reports as a node that does not accept the client file.

A client file works for status, logs, disks, reboot, storage reset, bootstrap, the etcd commands
and upgrade; every other command needs the secrets file. A reader may read nodes' information,
disks, status and logs and etcd's members; an operator may also reboot, upgrade, drain and
uncordon nodes; an admin may call every method of chalkd but the node's own renewal. See
https://trevex.github.io/chalkos/how-it-works/node/security/#chalkd-checks-the-role-of-every-request.

The command needs the secrets file and no inventory.`

const configNewExample = `  # A reader's client file for a dashboard.
  chalkctl config new --name grafana --role reader --ttl 720h --out grafana.json --secrets secrets.age

  # The operator's own client file, at ~/.config/chalkos/config.
  chalkctl config new --name alice --role operator --secrets secrets.age`

const storageLong = `Manages the volumes of an installed node. Its command needs an admin client file or the
secrets file.`

const storageExample = `  chalkctl storage reset w1 data`

const resetVolumeLong = `Wipes one volume of a node and creates it again, empty, the way the node's config defines it.
Its data is lost. A volume the config encrypts is sealed to the TPM again and, unless the
node's fallback is none, gets the second keyslot back: the node's recovery key or its password.
A volume the config leaves unencrypted is created without encryption. The command needs the
inventory, which defines the volume, and an admin client file or the secrets file. A
node whose fallback is its recovery key needs the secrets file, from which the key is derived.`

const resetVolumeExample = `  # Recreate the data volume of w1.
  chalkctl storage reset w1 data --secrets secrets.age`

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

const recoveryKeyLong = `Prints the recovery key of a node, which unlocks its encrypted volumes when the TPM does not:
for example after a change of the Secure Boot keys or state, which the TPM measures in PCR 7. It
is derived from the secrets file's recovery secret and the node's name, so no file stores it. The
command needs the secrets file.`

const recoveryKeyExample = `  chalkctl recovery-key w1`

const installLong = `Installs a node that waits in maintenance mode: a machine that booted the installer, or a
node's image booted for the first time. The node serves a self-signed certificate in maintenance
mode, so chalkctl verifies it by the SHA-256 fingerprint the node prints on its console
(--fingerprint), or accepts any certificate with --insecure and prints the fingerprint it saw,
which can be checked against the console afterwards.

chalkctl sends the node its config, checked against the schemas of the image it installs, a node
certificate with the node's name in the inventory, the OS CA, the secret of its volumes' second
keyslot and, on a Kubernetes node, its Kubernetes share. A node that runs its image already
installs in place. A node that runs the installer also gets the image --image names, or the node's
image in the inventory, and writes it to the disk its SystemDisk names: a disk on which blkid finds
no signature, one holding an unfinished install of the node, or any disk with --wipe-disk. The node
then reboots into the image. See
https://trevex.github.io/chalkos/how-it-works/node/overview/#an-install-turns-a-machine-in-maintenance-mode-into-a-node.

A name revoked with chalkctl node revoke is refused, as is any node while control planes were
bootstrapped and none says which nodes are revoked; --force installs without asking. The command
needs the secrets file.`

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
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. See
https://trevex.github.io/chalkos/how-it-works/node/config/#change-rules-decide-which-changes-a-node-accepts.

Each image on a node keeps its own config. --staged delivers the config for the image staged to
boot next, as an upgrade with --no-reboot leaves it, and nothing restarts. Without --staged,
chalkctl warns of a staged image, whose config stays as it is. A config for the running image that
adds storage the staged image's config lacks is refused: deliver the config with --staged first.

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; --force delivers it without asking. The command needs the secrets
file.`

const applyConfigExample = `  # Apply w1's changed network and labels.
  chalkctl apply-config w1 --secrets secrets.age

  # Give w1 a new kubelet certificate.
  chalkctl apply-config w1 --secrets secrets.age --kubernetes-share

  # Show what w1's config in the inventory changes on the node, changing nothing.
  chalkctl apply-config w1 --secrets secrets.age --diff

  # Deliver w1's config for the image an upgrade with --no-reboot staged.
  chalkctl apply-config w1 --secrets secrets.age --staged`

const statusLong = `Shows an installed node's status: the config it runs and whether it is the one the inventory
gives; its platform; the image it booted, by its store's root hash and the tag and revision it
was built from as far as the node knows them, the boot entry and whether that boot was found
healthy; an upgrade that is installed and boots next; an upgrade that failed and was rolled back,
with the journal of the boot that failed; whether its store is the image the inventory names for
it; its volumes and their state; Kubernetes, with the nodes revoked in the cluster on a control
plane; a router's BGP sessions; the certificates it serves and when they expire; the CAs it
trusts; its clock; and the units that failed. The command needs a reader client file or the
secrets file.`

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

const rotateLong = `Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept, switch, refresh and, with --finish, finish. A rotation
that stopped, at an unreachable node for example, or paused for the operator continues with
--resume. See
https://trevex.github.io/chalkos/how-it-works/node/certificates/#a-rotation-replaces-a-ca-in-four-phases.

The secrets file records the phase reached and is updated in place, keeping its previous version
as <file>.prev, unless --out names a new file. An encrypted file is encrypted again to the
recipients it records, which --recipient replaces. One rotation runs at a time, and one chalkctl
command at a time changes the secrets file, holding <file>.lock.

Nodes revoked with chalkctl node revoke are skipped and named; rotate stops while no control plane
says which nodes are revoked, unless none was bootstrapped yet. --ignore-revoked rotates without
the list, and every node of the inventory takes part, so remove revoked nodes from the inventory
first. The command needs the secrets file.`

const rotateExample = `  # Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
  # trust the new CA before it issues certificates. It pauses again after the refresh.
  chalkctl rotate kubernetes-ca --secrets secrets.age
  chalkctl rotate kubernetes-ca --secrets secrets.age --resume
  chalkctl rotate kubernetes-ca --secrets secrets.age --finish`

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

const upgradeLong = `Installs a new image on each node: control planes one at a time, each only while etcd keeps its
quorum without it, then workers and nodes without Kubernetes in batches of --max-unavailable.
Without --image, each node, or each node --nodes names, gets the image the inventory names for it;
with --image, the nodes that run the image's variant get it, or those --nodes names. A node gets
the image in its inactive slot, is cordoned and drained, reboots into the image, and is uncordoned
once its boot was found healthy and it is Ready. A node that falls back to the image before stops
the run.

Each node gets its config with the image, checked against it before any node is drained, and
chalkctl prints a line of what it changes; --diff prints the whole diff and changes nothing.
Delivering config needs the admin role: with an operator client file, each node carries its own
config over. A new volume is enrolled with the recovery key, which only the secrets file gives, or
the password --password-file holds.

Run again, the command continues where it stopped, and --retry-failed installs the image once more
on nodes that fell back from it. See
https://trevex.github.io/chalkos/how-it-works/node/upgrades/ and
https://trevex.github.io/chalkos/how-it-works/kubernetes/upgrades/. The command needs an
operator client file or the secrets file.`

const upgradeExample = `  # Upgrade the whole cluster, each node to the image the inventory names for it.
  chalkctl upgrade

  # Upgrade the nodes that run an image's variant, two at a time, with an image chalkbuild signed.
  chalkctl upgrade --image ./worker-image --max-unavailable 2`

const validateLong = `Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the set the inventory gives the node, every document against the schemas of
the node's image in the inventory, and the set's translation. The image's change rules that read no
facts and compare with no running value run too. Every node whose files hold a Kubernetes Cluster
document must hold the same Cluster spec, and the same Flannel spec or none. See
https://trevex.github.io/chalkos/how-it-works/node/config/#chalkctl-and-chalkd-both-check-the-config.

The command prints a line for each node whose config is valid, with the digest of an image in a
registry, names each node whose config is not, and warns of a node whose SystemDisk leaves STATE,
which holds its keys and secrets, unencrypted. It reaches no node and needs no secrets file or
client file.`

const validateExample = `  # Check every node of chalkos.yaml.
  chalkctl validate

  # Check two nodes of another inventory.
  chalkctl validate cp1 w1 --inventory lab/chalkos.yaml`
