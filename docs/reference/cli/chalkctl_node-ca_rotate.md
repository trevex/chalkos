---
title: "chalkctl node-ca rotate"
description: "Issue a new node CA and deliver it to the control planes"
---

# chalkctl node-ca rotate

Issue a new node CA and deliver it to the control planes

## Synopsis

Issues a new node CA from the OS CA, writes it to the secrets file and delivers it to every
control plane; the control planes issue node certificates from it from then on. Node certificates
of the old node CA stay valid until they expire, because they chain to the same OS CA. The
secrets file is updated in place, keeping its previous version as &lt;file>.prev, unless --out names
a new file. The command needs the secrets file.

```
chalkctl node-ca rotate [flags]
```

## Examples

```
  chalkctl node-ca rotate

  # Write the changed secrets to a new file and keep the old one.
  chalkctl node-ca rotate --out secrets.new.age --public-out secrets.pub.new.json
```

## Options

```
      --cluster string      cluster to use when the flake defines several
      --endpoint string     address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string        directory of the flake that defines the cluster (default ".")
  -h, --help                help for rotate
      --identity strings    age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string     read the cluster's manifest from this file instead of evaluating the flake
      --out string          write the changed secrets file to this new file instead of updating the secrets file in place
      --public-out string   write the public half to this new file instead of updating secrets.pub.json beside the secrets file in place
      --recipient strings   age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out
      --secrets string      secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl node-ca](chalkctl_node-ca.md)	 - Manage the node CA

