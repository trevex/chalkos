# A chalkos lab

A cluster of one control plane and one worker, run by `chalklab` as QEMU virtual machines on this
machine, with Secure Boot and a TPM each. The getting-started tutorial of the chalkos
documentation, https://trevex.github.io/chalkos/, walks through it.

You need Nix with flakes, and `/dev/kvm`; not root.

```sh
# The cluster's secrets: secrets.json, which stays out of git, and secrets.pub.json.
nix run github:trevex/chalkos#chalkctl -- gen secrets --plaintext
# The flake sees the files git tracks.
git init && git add .
# Build the images, start the VMs, install and bootstrap the cluster.
nix run github:trevex/chalkos#chalklab -- create
```

`chalklab create` prints the next commands: the kubeconfig to use, `chalkctl status` with the
lab's client file, `chalklab status` and `chalklab destroy`.

To upgrade, edit `cluster.nix`, build a role's image, sign a copy with the lab's keys and install
it with the lab's client file:

```sh
nix build .#chalkos.lab.roles.worker.images.kvm
nix run github:trevex/chalkos#chalklab -- sign result --out worker-image
nix run github:trevex/chalkos#chalkctl -- upgrade --image worker-image --config ~/.local/state/chalklab/lab/chalkctl.json
```
