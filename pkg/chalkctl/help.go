package chalkctl

// The long descriptions and examples of chalkctl's commands, which its help and the generated
// reference pages show. They are kept together so they read as one text.

const installLong = `Installs a node that waits in maintenance mode, as the installer and a role image booted
for the first time do. The node serves a self-signed certificate in maintenance mode, so chalkctl
verifies it by the SHA-256 fingerprint the node prints on its console (--fingerprint), or accepts
any certificate with --insecure and prints the fingerprint it saw, which can be checked against
the console afterwards; the secrets go over a second connection pinned to that fingerprint.

chalkctl sends the node its identity from the cluster definition, a node certificate, the OS CA,
the secret of its encrypted volumes' second keyslot (its recovery key, or a password) and, on a
Kubernetes role, its Kubernetes share. A node that runs its role image already installs in place.
A node that runs the installer gets the role image of its platform as well: --image, or the image
chalkctl builds from the flake, signed for Secure Boot with --sign-key and --sign-cert when
given. The installer writes it to the disk the node's identity names: a disk on which blkid finds
no signature, or one holding an unfinished install of the node's role, which it continues; any
other disk only with --wipe-disk. The node reboots into the installed image.`

const installExample = `  # Install cp1, comparing the certificate with the fingerprint on its console.
  chalkctl install cp1 --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08

  # Install w1 at an address of the network it booted into, with a prebuilt, signed image.
  chalkctl install w1 --endpoint 192.168.1.50 --insecure --image ./w1-image \
    --sign-key db.key --sign-cert db.crt`

const disksLong = `Lists the disks of a node with their size, type, model, serial number, WWN and use, and the
partitions on them, as a node's storage definition names its disks. An installed node is reached
with the cluster's credentials; a node in maintenance mode with --fingerprint or --insecure. A
node that the cluster definition does not name yet is reached at --endpoint, in maintenance mode.`

const disksExample = `  # The disks of an installed node.
  chalkctl disks w1

  # The disks of a machine that booted the installer and is not in the cluster definition yet.
  chalkctl disks --endpoint 192.168.1.50 --insecure`

const applyIdentityLong = `Delivers a node's identity from the cluster definition to the installed node: its hostname,
networks, labels, taints, storage and extensions. The node applies additive storage changes,
such as a new volume, while it runs and refuses destructive ones (chalkctl storage reset
recreates a volume); it restarts the units that read what changed, and chalkctl prints the
changes and the units. --kubernetes-share also delivers a new Kubernetes share, such as a
worker's new kubelet certificate.`

const applyIdentityExample = `  # Apply w1's changed network and labels.
  chalkctl apply-identity w1

  # Give w1 a new kubelet certificate.
  chalkctl apply-identity w1 --kubernetes-share`

const resetVolumeLong = `Wipes one volume of a node and creates it again as the node's identity defines it, empty. Its
data is lost. The volume is unlocked as before: by the TPM, and by the second keyslot's recovery
key or password. The command needs the cluster definition, which defines the volume.`

const resetVolumeExample = `  # Recreate the data volume of w1.
  chalkctl storage reset w1 data`

const statusLong = `Shows an installed node's status: the identity it runs and whether it is the one the cluster
definition gives, its platform, its volumes and their state, Kubernetes, the certificates it
serves and when they expire, the CAs it trusts, its clock and the units that failed.`

const statusExample = `  chalkctl status cp1

  # With a client file, as a lab writes.
  chalkctl status cp1 --config ~/.local/state/chalklab/lab/chalkctl.json`

const logsLong = `Prints a node's journal, of every unit or of --unit alone; with --follow it keeps printing new
entries until interrupted. A node in maintenance mode is reached with --fingerprint or
--insecure.`

const logsExample = `  # Follow chalkd's log on cp1.
  chalkctl logs cp1 --unit chalkd.service -f`

const rebootLong = `Reboots a node, installed or in maintenance mode. An installed node does not drain its
Kubernetes pods first.`

const rebootExample = `  chalkctl reboot w1`

const recoveryKeyLong = `Prints the recovery key of a node, which unlocks its encrypted volumes when the TPM does not,
as after a change of the Secure Boot keys or state, which the TPM measures in PCR 7. It is
derived from the secrets file's recovery secret, the cluster's name and the node's name, so no
file stores it.`

const recoveryKeyExample = `  chalkctl recovery-key w1`
