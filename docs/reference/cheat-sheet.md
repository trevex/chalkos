---
title: "Command cheat sheet"
description: "The chalkctl and chalklab command for each common task, and the credential each one needs"
---

# Command cheat sheet

This page is for operators who know the task and want the command. Each row names the task, the
command and what the command needs. The [command line reference](cli/index.md) has every flag.

The Needs column names one of these:

| Needs | Meaning |
| --- | --- |
| nothing | The command reaches no node and reads no secrets. |
| secrets file | The cluster's secrets file, passed with `--secrets`. A client file does not work. |
| reader, operator, admin | A client file of that role or a higher one, or the secrets file. |

chalkctl finds a client file in `--config`, then `$CHALKOSCONFIG`, then `~/.config/chalkos/config`.
[Roles and credentials](access.md) lists the role of each API method. The commands below leave out
`--inventory` and `--secrets`; add them where your setup needs them. `<node>` is a node's name in
the inventory.

## Inspect a node

| Task | Command | Needs |
| --- | --- | --- |
| Show the image, config, outputs, certificates and layers a node runs | `chalkctl status <node>` | reader |
| Print a node's journal of the current boot | `chalkctl logs <node>` | reader |
| Follow one unit's journal | `chalkctl logs <node> --unit=<unit> -f` | reader |
| List a node's disks and partitions | `chalkctl disks <node>` | reader |
| List the diagnostics a node's image offers | `chalkctl node diag <node>` | reader |
| Run a diagnostic | `chalkctl node diag <node> <diagnostic> <parameter>=<value>` | the role the diagnostic lists |
| Show a router's routes | `chalkctl node routes <node>` | reader |

## Configure a node

| Task | Command | Needs |
| --- | --- | --- |
| Check every node's config against its image | `chalkctl validate` | nothing |
| Print what a delivery would change | `chalkctl config apply <node> --diff` | secrets file |
| Have the node check a config without applying it | `chalkctl config apply <node> --dry-run` | secrets file |
| Deliver a node's config | `chalkctl config apply <node>` or `chalkctl apply <node>` | secrets file |
| Apply a config that rolls back unless it is confirmed | `chalkctl config apply <node> --try 5m` | secrets file |
| Confirm a config on trial | `chalkctl config confirm <node>` | secrets file |
| Return to the config before | `chalkctl config rollback <node>` | secrets file |
| Hold a config for the staged image or the next upgrade | `chalkctl config apply <node> --staged` | secrets file |
| Drop a config held for the next upgrade | `chalkctl config discard <node>` | secrets file |
| Recreate a volume, deleting its data | `chalkctl storage reset <node> <volume>` | admin |

## Install a node

| Task | Command | Needs |
| --- | --- | --- |
| Generate a cluster's secrets, encrypted with age | `chalkctl gen secrets --recipient=<age-recipient>` | nothing |
| List the disks of a machine in maintenance mode | `chalkctl disks --address=<address> --fingerprint=<fingerprint>` | reader, when the image carries an OS CA |
| Create Secure Boot keys | `chalkctl secureboot new <dir>` | nothing |
| Enrol Secure Boot keys in a node in Setup Mode | `chalkctl secureboot enroll --keys=<dir> --address=<address> --fingerprint=<fingerprint>` | admin, when the image carries an OS CA |
| Install a node in maintenance mode | `chalkctl install <node> --fingerprint=<fingerprint>` | secrets file |
| Print the key that unlocks a node without its TPM | `chalkctl recovery-key <node>` | secrets file |

## Upgrade nodes

| Task | Command | Needs |
| --- | --- | --- |
| Upgrade the nodes that run an image of the image's name | `chalkctl upgrade --image=<dir>` | operator; admin to deliver config with it |
| Upgrade every node to its image in the inventory | `chalkctl upgrade` | operator; admin to deliver config with it |
| Print what each node's config changes with the new image | `chalkctl upgrade --diff` | admin |
| Install an image and leave it staged | `chalkctl upgrade --nodes <node> --until stage` | operator |
| Make a staged image boot next | `chalkctl node activate <node>` | operator |
| Reboot a node, drained first | `chalkctl node reboot <node>` | operator |
| Boot the image from before the last upgrade | `chalkctl upgrade --rollback --nodes <node>` | operator |

## Manage access and certificates

| Task | Command | Needs |
| --- | --- | --- |
| Issue a client file of a role | `chalkctl client new --name=<name> --role=reader` | secrets file |
| Issue a node a new node certificate | `chalkctl node renew <node>` | secrets file |
| Rotate a CA or key of the cluster | `chalkctl rotate <kind>` | secrets file |
| Cut a node off the cluster | `chalkctl node revoke <node>` | admin |
| Let a revoked node back in | `chalkctl node unrevoke <node>` | admin |

## Run Kubernetes

| Task | Command | Needs |
| --- | --- | --- |
| Bootstrap the first control plane | `chalkctl bootstrap <node>` | admin |
| Write a kubeconfig for the cluster's admins | `chalkctl kubeconfig --out=<file>` | secrets file |
| List etcd's members | `chalkctl etcd members` | reader |
| Take a control plane out of etcd | `chalkctl etcd leave <node>` | admin |
| Remove the etcd member of a node that is gone | `chalkctl etcd remove-member <node>` | admin |

## Run a lab

`chalklab create` reads the lab file `chalklab.yaml` in the current directory. The other commands work on
the lab's state alone.

| Task | Command | Needs |
| --- | --- | --- |
| Start the VMs, install the nodes and bootstrap the cluster | `chalklab create` | secrets file |
| Show the VMs, their forwarded ports and the lab's files | `chalklab status` | nothing |
| Print and follow a node's serial console | `chalklab console <node>` | nothing |
| Sign a copy of an image with the lab's db key | `chalklab sign <dir> --out=<signed-dir>` | nothing |
| Start a lab again after the host rebooted | `chalklab start` | nothing |
| Stop the VMs and remove the lab's state | `chalklab destroy` | nothing |

Sources: the help of each command in `pkg/chalkctl/help.go`, `pkg/chalkctl/layers/*/help.go` and
`pkg/chalklab/help.go`, the roles of `pkg/chalkd/server.go` and the layers' permissions, and
`pkg/chalkctl/credentials.go`.
