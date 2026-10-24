---
title: "chalkctl gen secrets"
description: "Generate the cluster's secrets file"
---

# chalkctl gen secrets

```sh
chalkctl gen secrets [flags]
```

Generates the cluster's secrets once: the OS CA, the node CA, the Kubernetes CAs and keys, and
the secret that nodes' recovery keys derive from. They go to secrets.age, encrypted to the age
recipients --recipient gives (age public keys, SSH public keys or age plugin recipients), or with
--plaintext to secrets.json unencrypted. The public half goes to secrets.pub.json, or to
--public-out, which the osCA argument of mkImages names. How chalkos works describes the CAs:
[https://trevex.github.io/chalkos/how-it-works/node/certificates/](../../how-it-works/node/certificates.md)

The command refuses to overwrite any of these files, because new secrets would lock out every
installed node. With --out - the secrets file goes to standard output, for a secret manager to
keep, the public half to --public-out, which --out - needs, and the command's messages to standard
error. The command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--out`{ #flag-out } | string | `.` | Directory to write the files to, or - to write the secrets file alone to standard output. |
| `--plaintext`{ #flag-plaintext } | boolean | `false` | Write secrets.json unencrypted, for files protected by other means. |
| `--public-out`{ #flag-public-out } | string | secrets.pub.json in --out; required with --out - | New file to write the public half to. |
| `--recipient`{ #flag-recipient } | list of string | — | Age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated. |

## Examples

```sh
# Encrypt the secrets to an age key and an SSH key.
chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
  --recipient "$(cat ~/.ssh/id_ed25519.pub)"

# Write secrets.json unencrypted, kept out of version control.
chalkctl gen secrets --plaintext

# Hand the encrypted secrets to a secret manager, which supplies them later with --secrets -.
chalkctl gen secrets --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p \
  --out - --public-out secrets.pub.json | pass insert --multiline chalkos/secrets
```

## See also

- [chalkctl gen](chalkctl_gen.md): Generate files of a cluster
