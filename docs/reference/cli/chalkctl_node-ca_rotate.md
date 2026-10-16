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
      --address string      address of the node's chalkd, host or host:port (default the node's address in the inventory)
  -h, --help                help for rotate
      --identity strings    age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string    inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string     read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --out string          write the changed secrets file to this new file instead of updating the secrets file in place
      --public-out string   write the public half to this new file instead of updating secrets.pub.json beside the secrets file in place
      --recipient strings   age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out
      --secrets string      secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
```

## SEE ALSO

* [chalkctl node-ca](chalkctl_node-ca.md)	 - Manage the node CA

