---
title: "chalkctl config"
description: "Deliver a node's config and act on its trial or pending config"
---

# chalkctl config

Delivers a node's config from the inventory and acts on its trial or pending config. config
apply delivers the config, and chalkctl apply is its short form. config confirm confirms a config
on trial, config rollback delivers a node's previous config again, and config discard drops the
config a node holds pending for its next upgrade. The commands need the secrets file.

## Examples

```sh
# Deliver w1's config on trial for five minutes, or, with the short form:
chalkctl config apply w1 --secrets secrets.age --try 5m
chalkctl apply w1 --secrets secrets.age --try 5m

# Undo w1's last config delivery.
chalkctl config rollback w1 --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl config apply](chalkctl_config_apply.md): Deliver a node's config from the inventory
- [chalkctl config confirm](chalkctl_config_confirm.md): Confirm a node's config on trial
- [chalkctl config discard](chalkctl_config_discard.md): Drop the config a node holds pending for its next upgrade
- [chalkctl config rollback](chalkctl_config_rollback.md): Deliver a node's previous config again
