package chalklab

// The long descriptions and examples of chalklab's commands, which its help and the generated
// reference pages show.

const rootLong = `chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine, each with
Secure Boot and a TPM. A lab file (chalklab.yaml) names the cluster's inventory and the nodes the
lab runs, each with the MAC address of its NIC on a private network between the VMs. Each node's
chalkd and each control plane's API server are forwarded to a port on 127.0.0.1. A supervisor
process keeps the VMs running in the background. A lab's state (its disks, keys, console logs,
kubeconfig, client file and the inventory that reaches the nodes at their forwarded ports) lives
in $XDG_STATE_HOME/chalklab/<cluster>, by default ~/.local/state/chalklab/<cluster>. chalklab
needs /dev/kvm, which most distributions grant to the kvm group. It never builds images. Only
create needs the cluster's secrets file; the other commands work on the lab's state alone.`

const rootExample = `  # In a directory made from the lab template, once its image is built.
  chalklab create --secrets secrets.json
  chalklab status
  chalklab destroy`

const createLong = `Creates the lab of a lab file and brings the cluster up. chalklab reads the lab file
(chalklab.yaml, or --lab) and the inventory it names; each node the lab file names runs the kvm
image the inventory names for it. chalklab creates the lab's Secure Boot keys and enrolls them in
the VMs' firmware, copies each node's image and signs the copy with them, gives each node a disk
backed by it and the empty disks the lab file adds, and starts the supervisor, which runs a TPM
and a VM per node. Each VM's NIC on the lab network gets the MAC address the lab file gives it,
which the node's Network document must match; chalklab does not read the Network documents.
A node's memory, CPUs and disks default by its Node's type: 3072 MiB for a control plane, 2048 MiB
for another node, 2 CPUs and one disk of 16G.

chalklab first writes an admin client file. Each node boots its image in maintenance mode;
chalklab reads chalkd's certificate fingerprint from the console and installs the node in place
with chalkctl install --fingerprint. When the lab has a control plane, chalklab bootstraps the
first one and writes a kubeconfig that reaches its API server through the forwarded port.
chalkctl reads the lab's inventory, chalkos.yaml in the lab's state directory: the lab file's
inventory with its nodes at their forwarded ports. It names no secrets file: chalklab passes
chalkctl --secrets, and --sign-cert with the lab's db certificate. The closing output exports
the client file as $CHALKOSCONFIG, which chalkctl reads when a command names no --config or
--secrets, so the commands it prints name neither.

The command needs the cluster's secrets file, --secrets, which chalkctl reads to install the
nodes and to write the kubeconfig and the client file.`

const createExample = `  chalklab create --secrets secrets.age

  # Make a registry on the host's port 5000 reachable at 10.0.2.100:5000 in the VMs.
  chalklab create --secrets secrets.age --guest-forward 10.0.2.100:5000=127.0.0.1:5000`

const statusLong = `Shows the lab's supervisor, each VM with its node's type, whether it runs, the forwarded ports
of its chalkd and API server and its console log, and the paths of the lab's kubeconfig,
inventory, client file and Secure Boot db key and certificate. The command needs no secrets file
or client file.`

const statusExample = `  chalklab status`

const startLong = `Starts a lab again from its state, with its disks, firmware variables, TPM state and ports. When
the supervisor runs, it starts the VMs that stopped (for example a VM whose node powered off).
When no supervisor runs (for example after a reboot of this machine), chalklab starts the
supervisor, which starts every VM. The command needs no secrets file or client file.`

const startExample = `  chalklab start`

const consoleLong = `Prints a node's serial console log and follows what the node writes until interrupted;
--follow=false prints the log and exits. The console is read-only. The command needs no
secrets file or client file.`

const consoleExample = `  chalklab console cp1

  # Print the log so far.
  chalklab console w1 -f=false`

const signLong = `Signs the boot loader and UKIs of an image with the lab's Secure Boot db key, so the lab's VMs
boot it; an upgrade with an image built elsewhere needs that. An image in the Nix store cannot be
changed, so --out copies the image into a directory and signs the copy. The command needs no
secrets file or client file.`

const signExample = `  # Sign a copy of an image in the Nix store and upgrade the lab's nodes of its variant with it.
  chalklab sign /nix/store/...-chalkos-node-image --out node-image
  chalkctl upgrade --image node-image --inventory ~/.local/state/chalklab/lab/chalkos.yaml \
    --config ~/.local/state/chalklab/lab/chalkctl.json \
    --sign-cert ~/.local/state/chalklab/lab/keys/db.crt`

const destroyLong = `Stops the lab's supervisor, VMs, TPMs and network switch and removes its state: disks,
firmware variables, TPM state, keys, console logs, kubeconfig, inventory and client file. The
state is kept when anything of the lab is still running. The command needs no secrets file or
client file.`

const destroyExample = `  chalklab destroy --cluster lab`
