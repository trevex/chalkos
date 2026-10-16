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
The public half goes to secrets.pub.json, or to --public-out, which the cluster definition's
chalkos.osCA names so images trust the OS CA in maintenance mode. The command refuses to
overwrite any of these files: new secrets would lock out every installed node. With --out - the
secrets file goes to standard output, for a secret manager to keep, the public half to
--public-out, which --out - needs, and what the command says to standard error. It needs no
secrets file or client file.

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

  # Hand the encrypted secrets to a secret manager, which supplies them later with --secrets -.
  chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
    --out - --public-out secrets.pub.json | pass insert --multiline chalkos/secrets
```

## Options

```
  -h, --help                help for secrets
      --out string          directory to write the files to, or - to write the secrets file alone to standard output (default ".")
      --plaintext           write secrets.json unencrypted, for files protected by other means
      --public-out string   new file to write the public half to (default secrets.pub.json in --out; required with --out -)
      --recipient strings   age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated
```

## SEE ALSO

* [chalkctl gen](chalkctl_gen.md)	 - Generate files of a cluster

