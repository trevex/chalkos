---
title: "chalkctl node diag"
description: "List or run a node's diagnostics"
---

# chalkctl node diag

```sh
chalkctl node diag <node> [<diagnostic> [<parameter>=<value>...]] [flags]
```

Lists the diagnostics a node's image declares, or runs one. A diagnostic is a read-only command of
the image, such as ip route get, that chalkd runs on the node in a sandbox of its own. Without a
diagnostic, the command lists each one's role and description, and below it each parameter: in
brackets when it may be left out. With one, it passes the parameters given as
&lt;parameter>=&lt;value>, which the node checks against their types, and prints what the command
prints on standard output and standard error. It fails when the command fails, runs longer than
its timeout or prints more than 1 MiB.

Every image declares addresses, links, routes, route-get, neighbours, ping, dns-query,
tcp-connect and nft-ruleset; layers and extensions add more. Running a diagnostic needs the role
it lists. A node in maintenance mode is reached with --fingerprint or --insecure as well.

The command needs a reader client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the one node the command reaches, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--fingerprint`{ #flag-fingerprint } | string | — | SHA-256 fingerprint the node prints on its console in maintenance mode. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--insecure`{ #flag-insecure } | boolean | `false` | Accept any certificate and print the node's fingerprint. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--output`{ #flag-output }, `-o` | format | `text` | Format of the list of diagnostics: text, or json for programs; a diagnostic that runs prints what its command prints. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
# List w1's diagnostics.
chalkctl node diag w1

# The route w1 takes to an address.
chalkctl node diag w1 route-get address=2001:db8::1

# Whether w1 reaches a registry's port.
chalkctl node diag w1 tcp-connect address=2001:db8::5000 port=443
```

## See also

- [chalkctl node](chalkctl_node.md): Manage one node: access, image, reboot and diagnostics
