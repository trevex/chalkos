---
title: "chalkctl config"
description: "Manage client files"
---

# chalkctl config

Manage client files

## Synopsis

Manages client files, which let a person operate the cluster without the secrets file. Writing
a client file needs the secrets file.

## Examples

```
  chalkctl config new --name alice --role operator
```

## Options

```
  -h, --help   help for config
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters
* [chalkctl config new](chalkctl_config_new.md)	 - Write a client file, which operates the cluster without the secrets file

