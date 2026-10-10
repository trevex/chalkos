---
title: "chalkctl rotate"
description: "Rotate os-ca, kubernetes-ca, service-account-key or encryption-key"
---

# chalkctl rotate

Rotate os-ca, kubernetes-ca, service-account-key or encryption-key

## Synopsis

Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept (every node trusts the new value besides the old one),
switch (the new value issues or signs), refresh (what the old value issued is issued again) and,
with --finish, finish (the old value is removed, and whatever it issued is refused from then on).
The secrets file records the phase reached and is updated in place, keeping its previous version
as &lt;file>.prev, unless --out names a new file; an encrypted file is encrypted again to the
recipients it records inside, which --recipient replaces. A rotation that stopped (at an
unreachable node, for example) or paused for the operator continues with --resume. One rotation
runs at a time, and one chalkctl command at a time changes the secrets file, holding &lt;file>.lock.
The command needs the secrets file.

```
chalkctl rotate <kind> [flags]
```

## Examples

```
  # Rotate the Kubernetes CA. It pauses after the accept phase, so kubeconfigs and workloads can
  # trust the new CA before it issues certificates. It pauses again after the refresh.
  chalkctl rotate kubernetes-ca
  chalkctl rotate kubernetes-ca --resume
  chalkctl rotate kubernetes-ca --finish
```

## Options

```
      --cluster string       cluster to use when the flake defines several
      --endpoint NODE=ADDR   address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated (default each node's first static address)
      --finish               remove the old value from every node and the secrets file
      --flake string         directory of the flake that defines the cluster (default ".")
      --force                with --finish, finish the service-account key's rotation within the hour after its switch
  -h, --help                 help for rotate
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string      read the cluster's manifest from this file instead of evaluating the flake
      --out string           write the changed secrets file to this new file instead of updating the secrets file in place
      --public-out string    write the public half to this new file instead of updating secrets.pub.json beside the secrets file in place
      --recipient strings    age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out
      --resume               continue the rotation the secrets file records
      --secrets string       secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --timeout duration     how long to wait for each node to apply a phase (default 10m0s)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

