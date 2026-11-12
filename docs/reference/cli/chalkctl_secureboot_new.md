---
title: "chalkctl secureboot new"
description: "Create Secure Boot keys: PK, KEK and db"
---

# chalkctl secureboot new

```sh
chalkctl secureboot new <dir> [flags]
```

Creates a directory of Secure Boot keys: the platform key PK, the key exchange key KEK and the
db key, which signs images. Each is an RSA 2048 key in &lt;name>.key, readable by its owner alone, and
a self-signed certificate in &lt;name>.crt, valid for 20 years; firmware does not check the validity.
owner.txt holds a random GUID, the owner of the entries chalkctl secureboot enroll writes. The
directory must not exist or be empty.

chalkbuild sign signs images with db.key and db.crt. Whoever holds PK.key or KEK.key can change
which images the firmware starts, so keep the directory offline and backed up.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--name`{ #flag-name } | string | the directory's base name | Name the certificates' common names begin with. |

## Examples

```sh
chalkctl secureboot new keys
chalkbuild sign --key keys/db.key --cert keys/db.crt images/node-metal images/node-metal-signed
```

## See also

- [chalkctl secureboot](chalkctl_secureboot.md): Create Secure Boot keys and enrol them in a node's firmware
