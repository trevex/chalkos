---
title: "chalkctl client"
description: "Manage client files"
---

# chalkctl client

Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.

## Examples

```sh
chalkctl client new --name alice --role operator --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl client new](chalkctl_client_new.md): Write a client file, which operates the cluster without the secrets file
