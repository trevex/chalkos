---
title: "chalkctl config"
description: "Deliver nodes' config and manage client files"
---

# chalkctl config

Delivers nodes' config from the inventory and manages client files. config apply delivers the
config, and chalkctl apply is its short form. config confirm keeps a config on trial, config
rollback delivers a node's previous config again, and config discard drops the config a node holds
pending for its next upgrade. These commands need the secrets file.

config new writes a client file, which lets a person operate the cluster without the secrets file.
Writing a client file needs the secrets file.

## Examples

```sh
# Deliver w1's config on trial for five minutes, then the same with the short form.
chalkctl config apply w1 --secrets secrets.age --try 5m
chalkctl apply w1 --secrets secrets.age --try 5m

# Undo w1's last config delivery.
chalkctl config rollback w1 --secrets secrets.age

# Write the operator's own client file.
chalkctl config new --name alice --role operator --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl config apply](chalkctl_config_apply.md): Deliver a node's config from the inventory
- [chalkctl config confirm](chalkctl_config_confirm.md): Confirm a node's config on trial
- [chalkctl config discard](chalkctl_config_discard.md): Drop the config a node holds pending for its next upgrade
- [chalkctl config new](chalkctl_config_new.md): Write a client file, which operates the cluster without the secrets file
- [chalkctl config rollback](chalkctl_config_rollback.md): Deliver a node's previous config again
