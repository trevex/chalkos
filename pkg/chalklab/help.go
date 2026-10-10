package chalklab

// The long descriptions and examples of chalklab's commands, which its help and the generated
// reference pages show.

const rootLong = `chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine, each with
Secure Boot and a TPM. A lab is built from the cluster definition in a flake: its kvm nodes run
on a private network that matches their static addresses, and each node's chalkd and each
control plane's API server are forwarded to a port on 127.0.0.1. A supervisor process keeps the
VMs running in the background. A lab's state (its disks, keys, console logs, kubeconfig and
client file) lives in $XDG_STATE_HOME/chalklab/<cluster>, by default
~/.local/state/chalklab/<cluster>. chalklab needs /dev/kvm, which most distributions grant to the
kvm group. Only create needs the cluster's secrets file; the other commands work on the lab's
state alone.`

const rootExample = `  # In the directory of a flake made from the lab template.
  chalklab create
  chalklab status
  chalklab destroy`

const createLong = `Creates the lab of a cluster and brings the cluster up. chalklab builds the kvm image of each
role, creates the lab's Secure Boot keys and enrolls them in the VMs' firmware, signs copies of
the images with them, gives each node a disk backed by its role's image and starts the
supervisor, which runs a TPM and a VM per node. Each node boots its image in maintenance mode;
chalklab reads chalkd's certificate fingerprint from the console and installs the node in place
with chalkctl install --fingerprint. When the lab has a control plane, chalklab bootstraps the
first one and writes a kubeconfig that reaches its API server through the forwarded port. Last,
it writes an admin client file for chalkctl that reaches the nodes through their forwarded ports.

The command needs the cluster's secrets file, which chalkctl reads to install the nodes and to
write the kubeconfig and the client file: --secrets, else secrets.age or secrets.json in the
flake directory.

Only nodes on the kvm platform run in a lab, and each needs exactly one MAC address in its
network definition, which the lab's network interface gets. --nodes runs some of them. With
--manifest the cluster's manifest comes from a file, so every role's image must be given with
--image.`

const createExample = `  chalklab create

  # A smaller lab of two nodes of a flake that defines several clusters.
  chalklab create --cluster lab --nodes cp1,w1 --controlplane-memory 2048 --memory 1024

  # Make a registry on the host's port 5000 reachable at 10.0.2.100:5000 in the VMs.
  chalklab create --guest-forward 10.0.2.100:5000=127.0.0.1:5000`

const statusLong = `Shows the lab's supervisor, each VM with its role, whether it runs, the forwarded ports of its
chalkd and API server and its console log, and the paths of the lab's kubeconfig, client file
and Secure Boot db key and certificate. The command needs no secrets file or client file.`

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

const signExample = `  # Sign a copy of an image in the Nix store and upgrade the lab's nodes of its role with it.
  chalklab sign /nix/store/...-chalkos-worker-image --out worker-image
  chalkctl upgrade --image worker-image --config ~/.local/state/chalklab/lab/chalkctl.json`

const destroyLong = `Stops the lab's supervisor, VMs, TPMs and network switch and removes its state: disks,
firmware variables, TPM state, keys, console logs, kubeconfig and client file. The state is kept
when anything of the lab is still running. The command needs no secrets file or client file.`

const destroyExample = `  chalklab destroy --cluster lab`
