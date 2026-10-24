---
title: "chalkctl config"
description: "Manage client files"
---

# chalkctl config

Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.

## Examples

```sh
chalkctl config new --name alice --role operator --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl config new](chalkctl_config_new.md): Write a client file, which operates the cluster without the secrets file
