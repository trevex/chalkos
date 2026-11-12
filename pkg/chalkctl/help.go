package chalkctl

// The long descriptions and examples of chalkctl's commands, which its help and the generated
// reference pages show. They are kept together so they read as one text: each command's long
// description and its examples, in the order of the command tree.

const rootLong = `chalkctl installs and operates the nodes of a chalkos cluster. It reads the cluster from an
inventory, chalkos.yaml in the current directory or --inventory, which names each node, its
address, its image and its config files. It talks to chalkd on each node over mutual TLS, and never
builds or signs images.

Commands authenticate with the cluster's secrets file, which holds every CA key and which only
--secrets names, or with a client file of the admin, operator or reader role, which chalkctl client
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

const nodeLong = `Manages a node's access to the cluster and its boot and shows its state: renews its node
certificate, revokes and unrevokes it by name, makes the image staged on it boot next, reboots it
held by its layers, lists and runs the diagnostics its image declares, and shows a router's routes.
node renew needs the secrets file; node revoke and node unrevoke need an admin client file or the
secrets file; node activate and node reboot need an operator client file or the secrets file;
node diag and node routes need a reader client file or the secrets file.`

const nodeExample = `  chalkctl node renew w1
  chalkctl node revoke w1
  chalkctl node activate w1
  chalkctl node reboot w1
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

A node revoked with chalkctl node revoke is refused, as is any node while a layer cannot say
which nodes are revoked: in a Kubernetes cluster the control planes are asked first, and one
whose own certificate expired cannot answer. --force renews without asking. The command needs
the secrets file.`

const nodeRenewExample = `  chalkctl node renew w1`

const configLong = `Delivers a node's config from the inventory and acts on its trial or pending config. config
apply delivers the config, and chalkctl apply is its short form. config confirm confirms a config
on trial, config rollback delivers a node's previous config again, and config discard drops the
config a node holds pending for its next upgrade. The commands need the secrets file.`

const configExample = `  # Deliver w1's config on trial for five minutes, or, with the short form:
  chalkctl config apply w1 --secrets secrets.age --try 5m
  chalkctl apply w1 --secrets secrets.age --try 5m

  # Undo w1's last config delivery.
  chalkctl config rollback w1 --secrets secrets.age`

const clientLong = `Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.`

const clientExample = `  chalkctl client new --name alice --role operator --secrets secrets.age`

const clientNewLong = `Writes a client file: a new key with a certificate from the OS CA for --name and --role, and
the OS CA that nodes' certificates chain to. The file goes to ~/.config/chalkos/config unless --out
names another; commands read it from there, or from the path --config or $CHALKOSCONFIG names. A
client file names no cluster: its OS CA ties it to one, and a node of another cluster fails the TLS
handshake, which chalkctl reports as a node that does not accept the client file.

A client file works for status, logs, disks, node activate, node reboot, storage reset,
bootstrap, the etcd commands and upgrade; every other command needs the secrets file. A reader may
read nodes' information, disks, status and logs and etcd's members; an operator may also upgrade,
activate staged images, reboot, drain and uncordon nodes; an admin may call every method of chalkd
but the node's own renewal. See
https://trevex.github.io/chalkos/how-it-works/node/security/#chalkd-checks-the-role-of-every-request.

The command needs the secrets file and no inventory.`

const clientNewExample = `  # A reader's client file for a dashboard.
  chalkctl client new --name grafana --role reader --ttl 720h --out grafana.json --secrets secrets.age

  # The operator's own client file, at ~/.config/chalkos/config.
  chalkctl client new --name alice --role operator --secrets secrets.age`

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
keyslot and the payloads of its layers, such as a Kubernetes node's share. A node that runs its
image already installs in place. A node that runs the installer also gets the image --image names,
or the node's image in the inventory, and writes it to the disk its SystemDisk names: a disk on
which blkid finds no signature, one holding an unfinished install of the node, or any disk with
--wipe-disk. Once the image's store is on the disk, the image's own checker checks the config with
its rules, validators and renderers; a config it refuses leaves a disk that boots nothing, and the
next install of the image continues there. The node then reboots into the image. See
https://trevex.github.io/chalkos/how-it-works/node/overview/#an-install-turns-a-machine-in-maintenance-mode-into-a-node.

A name revoked with chalkctl node revoke is refused, as is any node while a layer cannot say which
names are revoked, as when a bootstrapped Kubernetes cluster's control planes do not answer;
--force installs without asking. chalkctl warns of each image reference in the config that names a
tag but no digest. The command needs the secrets file.`

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

// applyShortLong opens the long description of chalkctl apply, which applyConfigLong continues.
const applyShortLong = `chalkctl apply is the short form of chalkctl config apply: it takes the same nodes and flags and
does the same.`

const applyConfigLong = `Delivers a node's config to the installed node: the documents of the files the inventory gives
it. chalkctl checks them against the contract of the image the node runs, with the image's change
rules that read no facts, and the node checks them again. The node replaces its config as a whole,
applies additive storage changes such as a new volume while it runs, and restarts the units that
read what changed; chalkctl prints the changes and the units. When the image's rules refuse a
change, the node refuses the whole config and names the remedy. chalkctl warns of each image
reference in the config that names a tag but no digest, and delivers the config all the same. See
https://trevex.github.io/chalkos/how-it-works/node/config/#change-rules-decide-which-changes-a-node-accepts.

With several nodes, config apply delivers to them one after the other and stops at the first that
fails; a node named twice gets the config once. --address gives each node's address as NODE=ADDR
where it is not the inventory's. chalkctl reads each node's status and checks its config against
its image before the first node gets anything, and leaves a node that runs the config already
alone, so the same command run again continues a run that stopped. --staged, --diff and
--no-confirm take one node.

When the config changes a version that a layer of the nodes' images bounds across the cluster,
such as the Kubernetes API server's, against the config a node runs, the run is ordered, with one
node or several. A run that changes no such version asks no layer anything. An ordered run reads
the versions every node of the cluster runs, and the layer refuses a config that would break its
bounds, as Kubernetes's version skew policy does. The nodes that act for the others, such as
Kubernetes control planes, go first, one at a time. Its layers hold each first, as Kubernetes
waits for etcd to keep its quorum; then it gets the config on trial, for --try or else 10 minutes,
until its layers find it serving with the config, as Kubernetes finds a control plane's static
pods running the new images. Then the other nodes get the config as in any run. The first failure
rolls that node's trial back at once, and chalkctl prints which nodes run which version. --until
deliver delivers on trial to the first node that does not run the config and leaves its trial open
for config confirm. An ordered run goes without the layers' payload flags. See
https://trevex.github.io/chalkos/kubernetes/upgrade-kubernetes/.

--diff prints what the config changes in the node's config, document by document, and checks nothing
on the node. --dry-run has the node check the config as it checks a delivery, with its facts, the
image's renderers and their checks, and changes nothing. It prints the diff, then the config the
node would run, the outputs that would change, the effects the delivery would take with what fires
each, and the storage changes. A refusal is the delivery's, and the command exits 1. --dry-run takes
several nodes and checks them one after the other; its error names the nodes whose config is refused
apart from those it could not check, such as a node on trial. It goes without --diff, --try,
--no-confirm, --ignore-unit, --force and the layers' payload flags. An effect whose rule compares
the node's facts before and after the delivery is planned with the facts as they are. The plan ends
with the staged image's config or the pending config, when the delivery writes either. With
--staged, the staged image checks the config with its own chalkd and renderers, and the plan has no
outputs and no effects: that image renders its outputs and takes its effects when it boots.

Each image on a node keeps its own config. The image in the node's inactive slot, staged by
upgrade --until stage or active after upgrade --until activate, boots with the config it checked
itself. A delivery to the running image goes to that image too: it checks the config with its own
chalkd and renderers, against the storage the running image's change leaves, before anything
changes, and its refusal refuses the delivery. Once the running image applied the config, the node
writes the staged image's config and says so. When that write fails, the staged image keeps its
earlier config, the command fails naming it, and running it again writes it. The image a rollback
staged while the running image's boot is not found healthy keeps the config it last ran, and the
node warns of it. --staged delivers the config to the staged image alone, and nothing restarts.

On a node without a staged image, --staged holds the config pending for the next upgrade, in place
of any config held before. chalkctl checks it against the node's image in the inventory; the node
only parses it, as no image is there to check it against. The next upgrade that carries no config
of its own, such as an operator's, gives the image the pending config in place of the running one,
and the image checks it before the node installs anything. An upgrade that carries config replaces
it. Either way the node drops the pending config once the image is staged; an upgrade
that fails keeps it, and an image's refusal names it. A pending config is kept without the fallback
secret, so an upgrade with one that adds an encrypted volume needs the config from the inventory.
A delivery to the running image replaces a pending config too, once the running image applied it,
so the next upgrade boots the config delivered last. --staged --dry-run says the config would be
held pending and keeps nothing. config discard drops the pending config.

config rollback delivers the node's previous config again instead of the inventory's.

--try applies the config on trial for the duration given, from 10s to 24h. Unless chalkctl confirms
the config within it, the node rolls the config back by itself through the same steps as
config rollback. chalkctl confirms it over a new connection, which proves that the config left
the node reachable, once the node is healthy as an upgrade's boot must be: chalkd serves, no unit of
the boot failed, and the image's layers find their services healthy. The check leaves out the units
the image ignores and those --ignore-unit names, one unit per flag. Before it confirms, chalkctl
also waits for the image's layers to find the node serving with the config, as Kubernetes finds its
Node Ready and a control plane's static pods on the images its config names; when they do not by the
deadline, chalkctl rolls the config back at once. chalkctl warns of units that failed before the
trial, which keep the check from passing unless they recover. When the config is not confirmed, or
the answer to its delivery is lost, as when the config cuts chalkctl off, chalkctl waits up to two
minutes after the deadline for the node's rollback, reports it and exits non-zero. Interrupting
chalkctl leaves the trial open. A trial delivers config alone and is refused for a config that
changes the node's storage. While it is open, the node refuses other deliveries, upgrades and image
rollbacks; config rollback rolls the config back at once and ends the trial, and a reboot
boots the config from before it. --no-confirm leaves the trial open, and config confirm
confirms it later. A delivery on trial that fails after it changed the config leaves the trial open,
and chalkctl does not confirm it. When an attempt to roll the config back fails, the node tries
again each minute and config confirm still keeps the config, unless the failed attempt changed
the node; config rollback then finishes the rollback. The staged image checks config on trial
as any delivery, but its config, or the pending config, is written only once the trial is confirmed,
so a rollback leaves it as it was.

--kubernetes-share also delivers a new Kubernetes share, such as a worker's new kubelet
certificate. It is refused to a node revoked with chalkctl node revoke, and while no control plane
says which nodes are revoked; --force delivers it without asking. The command needs the secrets
file.`

const applyConfigExample = `  # Apply w1's changed network and labels.
  chalkctl config apply w1 --secrets secrets.age

  # Give w1 a new kubelet certificate.
  chalkctl config apply w1 --secrets secrets.age --kubernetes-share

  # Show what w1's config in the inventory changes on the node, changing nothing.
  chalkctl config apply w1 --secrets secrets.age --diff

  # Have w1 and w2 check their config, renderers included, and show what a delivery would do.
  chalkctl config apply w1 w2 --secrets secrets.age --dry-run

  # Deliver w1's config for the image an upgrade staged, or without one hold it pending for w1's
  # next upgrade.
  chalkctl config apply w1 --secrets secrets.age --staged

  # Drop the config w1 holds pending.
  chalkctl config discard w1 --secrets secrets.age

  # Undo w1's last config delivery.
  chalkctl config rollback w1 --secrets secrets.age

  # Try w1's changed network for five minutes; w1 rolls it back unless chalkctl reaches it again.
  chalkctl config apply w1 --secrets secrets.age --try 5m

  # Keep a trial open while you test by hand, then keep the config.
  chalkctl config apply w1 --secrets secrets.age --try 30m --no-confirm
  chalkctl config confirm w1 --secrets secrets.age

  # Deliver the cluster's config to every node. When it names a new API server, the control
  # planes get it first, one at a time, each on trial until its static pods run the new images.
  chalkctl config apply cp1 cp2 cp3 w1 w2 --secrets secrets.age

  # Walk the control planes by hand: deliver to the first that lacks the config, leave its trial
  # open, check it, confirm it, and run the first command again for the next.
  chalkctl config apply cp1 cp2 cp3 --secrets secrets.age --until deliver
  chalkctl config confirm cp1 --secrets secrets.age`

const confirmConfigLong = `Confirms the open trial of a node's config, as config apply --try does without --no-confirm, so
the node keeps the config. A trial stays open after config apply --try --no-confirm, after
config apply --until deliver, and when chalkctl was interrupted.

chalkctl first waits for the image's layers to find the node serving with the config, as
Kubernetes finds its Node Ready and a control plane's static pods on the images its config names;
when they do not by the deadline, chalkctl rolls the config back at once. It then confirms over a
new connection once the node is healthy, trying until the trial's deadline while it is not, or for
two minutes past it after a failed rollback, which the node still lets a confirmation end. A
rollback that failed after it changed the node can no longer be confirmed: config rollback
finishes it.

The node keeps the config on trial, so the command needs no inventory. It needs the secrets file.`

const confirmConfigExample = `  # Keep a trial open while you test by hand, then keep the config.
  chalkctl config apply w1 --secrets secrets.age --try 30m --no-confirm
  chalkctl config confirm w1 --secrets secrets.age`

const rollbackConfigLong = `Delivers a node's previous config again: the config the running image ran before the last
delivery, which the node keeps. It goes through the same checks, storage changes and restarts as
any delivery, so the image's rules may refuse it, as when the config it replaces added a volume.
The config it replaces becomes the previous one, so a second rollback undoes the first. It restores
the config documents alone: the OS CAs, the node certificate and layer payloads such as a
Kubernetes share stay as they are. An upgrade's image starts without a previous config. --diff
prints what the rollback changes and changes nothing.

During a trial, the rollback goes to the config from before the trial at once and ends the trial.
It also finishes a rollback at a trial's deadline that failed after it changed the node. After a
trial's rollback, the config on trial is the node's previous config, which the command delivers
again without a trial.

The node keeps the config it rolls back to, so the command needs no inventory. It needs the
secrets file.`

const rollbackConfigExample = `  # Show what undoing w1's last config delivery changes, then undo it.
  chalkctl config rollback w1 --secrets secrets.age --diff
  chalkctl config rollback w1 --secrets secrets.age`

const discardPendingLong = `Drops the config a node holds pending for its next upgrade, which config apply --staged leaves on
a node without a staged image, and names the config it dropped. The node holds the config, so the
command needs no inventory. It needs the secrets file.`

const discardPendingExample = `  # Drop the config w1 holds pending.
  chalkctl config discard w1 --secrets secrets.age`

const statusLong = `Shows an installed node's status: the config it runs and whether it is the one the inventory
gives; its platform; the image it booted, by its store's root hash and the tag and revision it
was built from as far as the node knows them, the boot entry and whether that boot was found
healthy; an upgrade that is staged, or active and boots next; an upgrade that failed and was
rolled back,
with the journal of the boot that failed; a config on trial and when it rolls back; the config it
holds pending for its next upgrade, unchecked, and since when; the previous config and image a
rollback restores; whether its store is the image the inventory names for it; its volumes and
their state; Kubernetes, with the nodes revoked in the cluster on a control plane; a router's BGP
sessions; the certificates it serves and when they expire; the CAs it trusts; its clock; and the
units that failed. The command needs a reader client file or the secrets file.`

const statusExample = `  chalkctl status cp1

  # With the inventory and the client file a lab writes.
  chalkctl status cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml \
    --config ~/.local/state/chalklab/lab/chalkctl.json`

const logsLong = `Prints a node's journal of the current boot, of every unit or of --unit alone; with --follow it
keeps printing new entries until interrupted. The command needs a reader client file or the
secrets file. A node in maintenance mode is reached with --fingerprint or --insecure as well.`

const logsExample = `  # Follow chalkd's log on cp1.
  chalkctl logs cp1 --unit chalkd.service -f`

const nodeActivateLong = `Makes the image staged on a node boot next, as chalkctl upgrade --until stage leaves it:
the node renames the image's UKI to its boot entry's name with the tries the image asks for, so
boot counting falls back to the running image should its boots fail. chalkctl names the image the
inventory gives the node, or with --image the image upgrade --image staged, by its root hash, and
the node refuses when it has no image staged or another one. With --image the command needs no
inventory. The node checks the UKI again, and not the config: the image's own checker passed
the config before the image was staged. Asked again, the command says that the image boots next
already. The image boots with the node's next reboot, which chalkctl node reboot runs. The command
needs an operator client file or the secrets file.`

const nodeActivateExample = `  chalkctl upgrade --nodes w1 --until stage
  chalkctl node activate w1
  chalkctl node reboot w1

  # Activate the image upgrade --image staged.
  chalkctl upgrade --image ./worker-image --nodes w1 --until stage
  chalkctl node activate w1 --image ./worker-image`

const nodeRebootLong = `Reboots a node as chalkctl upgrade does once the node's image is active: its layers hold it
first, as Kubernetes keeps etcd's quorum while a control plane reboots and drains the node; then
the node reboots, and chalkctl waits until it runs the image it boots next, an image activated by
chalkctl node activate or the one it ran, and its boot was found healthy, and its layers release
it, as Kubernetes uncordons it once its Node is Ready. The node checks the health of the new boot
itself, also of a boot of an entry blessed before, which the boot loader does not count. A node
that falls back from the image stops the command and stays held, and so does a node whose boot
settled and stays unhealthy for a minute: the boot settled once systemd finished starting it and
the image's health timeout, 300 seconds by default, passed since. A check that fails once, such as
etcd answering late, does not stop the command, and a healthy look starts the minute over. When
the node's status gave no ID of the boot before the reboot, chalkctl cannot tell the new boot from
that one and waits for --timeout instead. A boot not found healthy within --timeout stops the
command too. --no-drain reboots the node without its layers holding or releasing it, and still
waits. Draining a node goes through the cluster's control planes, which the inventory names.

A node in maintenance mode, reached with --fingerprint or --insecure, reboots without more. The
command needs an operator client file or the secrets file.`

const nodeRebootExample = `  # Drain w1, reboot it into the image it boots next and uncordon it once it is Ready.
  chalkctl node reboot w1

  # Reboot w1 without draining it first.
  chalkctl node reboot w1 --no-drain`

const rotateLong = `Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept, switch, refresh and, with --finish, finish. A rotation
that stopped, at an unreachable node for example, or paused for the operator continues with
--resume. See
https://trevex.github.io/chalkos/how-it-works/node/certificates/#a-rotation-replaces-a-ca-in-four-phases.

The secrets file records the phase reached and is updated in place, keeping its previous version
as <file>.prev, unless --out names a new file. An encrypted file is encrypted again to the
recipients it records, which --recipient replaces. One rotation runs at a time, and one chalkctl
command at a time changes the secrets file, holding <file>.lock.

Nodes revoked with chalkctl node revoke are skipped and named; rotate stops while a layer cannot
say which nodes are revoked, as when no Kubernetes control plane answers, unless none was
bootstrapped yet. --ignore-revoked rotates without the lists, and every node of the inventory
takes part, so remove revoked nodes from the inventory first. The command needs the secrets file.`

const rotateExample = `  # Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
  # trust the new CA before it issues certificates. It pauses again after the refresh.
  chalkctl rotate kubernetes-ca --secrets secrets.age
  chalkctl rotate kubernetes-ca --secrets secrets.age --resume
  chalkctl rotate kubernetes-ca --secrets secrets.age --finish

  # Rotate the node CA. It runs in one go: every node gets a certificate of the new one.
  chalkctl rotate node-ca --secrets secrets.age`

const upgradeLong = `Installs a new image on each node: first the nodes that act for the others, such as Kubernetes
control planes, one at a time, then the other nodes in batches of --max-unavailable. Without
--image, each node, or each node --nodes names, gets the image the inventory names for it; with
--image, the nodes that run the image's variant get it, or those --nodes names. Each node goes
through the steps the step-by-step path runs one at a time: the node stages the image in its
inactive slot once the image's own checker accepted its config, as upgrade --until stage stops
there; it activates the image, which then boots next, as chalkctl node activate does; and it
reboots into the image as chalkctl node reboot does. Its layers hold it before the reboot and
release it once its boot was found healthy: Kubernetes keeps etcd's quorum, drains the node and
uncordons it once it is Ready. A node that falls back to the image before stops the run. --until
stage or --until activate stops each node after that step and holds no node.

Before any node gets its image, the image's layers check the versions it brings against what the
cluster runs and refuse an image that would break their bounds, as Kubernetes refuses a kubelet
newer than the oldest API server the control planes run. An upgrade also refuses a config that
changes a version the layers bound against the config a node runs, such as the Kubernetes API
server's: chalkctl config apply delivers that change first, in the order the layers need.

Each node gets its config with the image, checked against it before any node is drained, and
chalkctl prints a line of what it changes and warns of each image reference in the config that
names a tag but no digest; --diff prints the whole diff and changes nothing.
Delivering config needs the admin role: with an operator client file, each node gives the image
its pending config, else carries its own config over. A new volume is enrolled with the recovery
key, which only the secrets file gives, or the password --password-file holds.

--rollback boots the image the nodes ran before their last upgrade again instead of installing
one, which their inactive slots still hold with its config until the next upgrade writes over
them. chalkctl takes that image from the nodes' status: every node named, or every node of the
cluster, must roll back to the same image or run it already, or the command refuses and asks for
--nodes. Boot counting protects the boot as it protects an upgrade's, except on a node whose
running image's boot was not found healthy: the image before, found healthy once, then boots
uncounted, as the node's known-good image, and is not fallen back from. The run holds and releases
each node as an upgrade does; it stages nothing, so it takes --until activate and reboot alone.
Run again, it continues with the same image and leaves the nodes that run it alone. Once the nodes
run the image before, an upgrade to the inventory's image undoes the rollback.

Run again, an upgrade continues where it stopped, and --retry-failed installs the image once more
on nodes that fell back from it. See
https://trevex.github.io/chalkos/how-it-works/node/upgrades/ and
https://trevex.github.io/chalkos/how-it-works/kubernetes/upgrades/. The command needs an
operator client file or the secrets file.`

const upgradeExample = `  # Upgrade the whole cluster, each node to the image the inventory names for it.
  chalkctl upgrade

  # Upgrade the nodes that run an image's variant, two at a time, with an image chalkbuild signed.
  chalkctl upgrade --image ./worker-image --max-unavailable 2

  # Upgrade w1 step by step: stage its image, make it boot next, reboot into it.
  chalkctl upgrade --nodes w1 --until stage
  chalkctl node activate w1
  chalkctl node reboot w1

  # Boot the image w1 and w2 ran before their last upgrade again.
  chalkctl upgrade --rollback --nodes w1,w2`

const validateLong = `Checks the config of the nodes named, or of every node of the inventory, as each node would
check it on its image: the set the inventory gives the node, every document against the schemas of
the node's image in the inventory, and the set's translation. The image's change rules that read no
facts and compare with no running value run too, and so does the validator router of a router
image, which chalkctl links. Every node whose files hold a Kubernetes Cluster document must hold
the same Cluster spec, and the same Flannel spec or none. See
https://trevex.github.io/chalkos/how-it-works/node/config/#chalkctl-and-chalkd-both-check-the-config.

The command prints a line for each node whose config is valid, with the digest of an image in a
registry, names each node whose config is not, and warns of a node whose SystemDisk leaves STATE,
which holds its keys and secrets, unencrypted, and of each image reference that names a tag but no
digest, which nodes pull by whatever the tag points to when they pull. It reaches no node and
needs no secrets file or client file.

A config validate passes can still be refused on delivery: validate runs no rule that reads the
node's facts or its running config, none of the image's other validators, and none of its
renderers or their checks. chalkctl config apply --dry-run has the node run all of them, and
changes nothing.`

const validateExample = `  # Check every node of chalkos.yaml.
  chalkctl validate

  # Check two nodes of another inventory.
  chalkctl validate cp1 w1 --inventory lab/chalkos.yaml`

const securebootLong = `Creates the Secure Boot keys of the machines an operator owns and enrols them in the firmware of a
node in maintenance mode whose firmware is in Setup Mode, without the firmware's setup screen. The
private keys stay on the workstation: chalkctl signs the updates of the firmware's variables, and
the node writes them. See https://trevex.github.io/chalkos/build/sign-images/.`

const securebootExample = `  chalkctl secureboot new keys
  chalkctl secureboot enroll --keys keys --address 192.168.1.50 --insecure --secrets secrets.age`

const securebootNewLong = `Creates a directory of Secure Boot keys: the platform key PK, the key exchange key KEK and the
db key, which signs images. Each is an RSA 2048 key in <name>.key, readable by its owner alone, and
a self-signed certificate in <name>.crt, valid for 20 years; firmware does not check the validity.
owner.txt holds a random GUID, the owner of the entries chalkctl secureboot enroll writes. The
directory must not exist or be empty.

chalkbuild sign signs images with db.key and db.crt. Whoever holds PK.key or KEK.key can change
which images the firmware starts, so keep the directory offline and backed up.`

const securebootNewExample = `  chalkctl secureboot new keys
  chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed`

const securebootEnrollLong = `Enrols the Secure Boot keys of --keys in the firmware of a node in maintenance mode, which must be
in Setup Mode, as firmware is once its platform key is cleared in its setup. chalkctl signs three
updates of the firmware's variables: db and KEK, appended to what the firmware holds, and PK, which
ends Setup Mode. It reads PK.key, PK.crt, KEK.key, KEK.crt, db.crt and owner.txt from --keys;
db.key, which signs images, may stay on the machine that signs them. With --microsoft, db also
gets Microsoft's UEFI CAs of 2011 and 2023 and its option ROM CA of 2023, and KEK Microsoft's KEK
CAs of 2011 and 2023; firmware needs them to start the option ROMs of many graphics and network
cards and boot loaders Microsoft signed.

The node refuses the keys unless the boot loader and UKI it booted verify against db with the new
keys, so sign the installer or image with the db key first (chalkbuild sign) and boot it; --force
enrols the keys all the same. A node that runs its image from its own disk deletes STATE, which
holds nothing before the install and which the TPM no longer unseals once the keys change, and
creates it again when it boots. The node writes PK last: when a write fails before it, the firmware
stays in Setup Mode and enroll can run again.

The node then reboots, and chalkctl waits until it answers with Secure Boot on. It makes a new
certificate in maintenance mode when it boots, which chalkctl accepts without verifying and prints
the fingerprint of; compare it with the node's console before the install. Like chalkctl disks, the
command reaches the node by --address, with --fingerprint or --insecure, and with the secrets file
or an admin's client file when its image carries an OS CA.`

const securebootEnrollExample = `  # Enrol the keys in a machine that booted the signed installer, then install it.
  chalkctl secureboot enroll --keys keys --address 192.168.1.50 --insecure --secrets secrets.age
  chalkctl install w1 --secrets secrets.age --address 192.168.1.50 --fingerprint <fingerprint>

  # Enrol Microsoft's CAs too, for a graphics card's option ROM.
  chalkctl secureboot enroll --keys keys --microsoft --address 192.168.1.50 \
    --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08`
