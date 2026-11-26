---
title: "chalkctl status"
description: "Show an installed node's status, or a line for each node of the cluster"
---

# chalkctl status

```sh
chalkctl status [<node>] [flags]
```

Shows an installed node's status. Its first line sums it up: healthy, or the problems the lines
below show, such as a boot not found healthy yet, a volume missing or a unit that failed. Then
come the config stored for the running image and whether it is the inventory's, a live activation,
a trial, a staged or pending config, the platform, the image the node booted and whether that boot
was found healthy, an upgrade staged, active or failed, the previous config and image a rollback
restores, whether the store is the inventory's image, the module policy, the volumes, the outputs,
each layer's section, the certificates, the CAs the node trusts, its clock and the failed units.
See [Status, logs and the console](../../operate/status-and-logs.md).

Without a node, the command reads every node of the inventory at once, each within 15 seconds, and
prints a line of each: its image, whether its boot was found healthy, how its config compares with
the inventory's or until when it is on trial, and how many problems it shows. A node that does not
answer is unreachable and stops none of the others.

-o json prints the same for programs: each node's name, whether it is healthy, its problems, how it
compares with the inventory, and its answers to the node API's Info and Status and the layers'
statuses in protojson. See
[Gate a pipeline on chalkctl status](../../operate/status-and-logs.md#gate-a-pipeline-on-chalkctl-status).

status decrypts no sops:// or age:// source of a Secret document, so it asks for no key. When the
config the inventory gives the node takes values from such sources, status does not know its
version: it says so in place of comparing it, and chalkctl config apply --diff &lt;node> compares it.

The command needs a reader client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the one node the command reaches, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--output`{ #flag-output }, `-o` | format | `text` | Format to print: text, or json for programs. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl status cp1

# A line of each node of the cluster.
chalkctl status

# Fail unless every node is healthy.
chalkctl status -o json | jq -e '.nodes | all(.healthy)'

# With the inventory and the client file a lab writes.
export CHALKOSINVENTORY=~/.local/state/chalklab/lab/chalkos.yaml
export CHALKOSCONFIG=~/.local/state/chalklab/lab/chalkctl.json
chalkctl status cp1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
