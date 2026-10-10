---
title: "chalkctl gen secrets"
description: "Generate the cluster's secrets file"
---

# chalkctl gen secrets

Generate the cluster's secrets file

## Synopsis

Generates the cluster's secrets once: the OS CA, which every node and client trusts and which
issues client certificates and the node CA; the node CA; the Kubernetes CAs and keys; and the
secret that nodes' recovery keys derive from. They go to secrets.age, encrypted to the
age recipients given with --recipient (age public keys, SSH public keys or age plugin
recipients), or with --plaintext to secrets.json unencrypted, for files protected by other means.
The public half goes to secrets.pub.json, which the cluster definition's chalkos.cluster.osCA
names so images trust the OS CA. The command refuses to overwrite any of these files: new
secrets would lock out every installed node. It needs no secrets file or client file.

```
chalkctl gen secrets [flags]
```

## Examples

```
  # Encrypt the secrets to an age key and an SSH key.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --recipient "$(cat ~/.ssh/id_ed25519.pub)"

  # Write secrets.json unencrypted, kept out of version control.
  chalkctl gen secrets --plaintext
```

## Options

```
  -h, --help                help for secrets
      --out string          directory to write the files to (default ".")
      --plaintext           write secrets.json unencrypted, for files protected by other means
      --recipient strings   age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated
```

## SEE ALSO

* [chalkctl gen](chalkctl_gen.md)	 - Generate files of a cluster

