---
title: "chalkctl version"
description: "Print the version and the git revision of the build"
---

# chalkctl version

```sh
chalkctl version
```

Prints the version of chalkos the program belongs to and the git revision it was built from.
A build from a working tree with uncommitted changes names the revision with -dirty after it, and
one that does not know its revision, such as a build outside a git checkout, names it unknown.
The command needs no secrets file or client file.

## Examples

```sh
chalkctl version
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
