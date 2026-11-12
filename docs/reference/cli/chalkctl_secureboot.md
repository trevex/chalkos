---
title: "chalkctl secureboot"
description: "Create Secure Boot keys and enrol them in a node's firmware"
---

# chalkctl secureboot

Creates the Secure Boot keys of the machines an operator owns and enrols them in the firmware of a
node in maintenance mode whose firmware is in Setup Mode, without the firmware's setup screen. The
private keys stay on the workstation: chalkctl signs the updates of the firmware's variables, and
the node writes them. See [Sign images for Secure Boot](../../build/sign-images.md).

## Examples

```sh
chalkctl secureboot new keys
chalkctl secureboot enroll --keys keys --address 192.168.1.50 --insecure --secrets secrets.age
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl secureboot enroll](chalkctl_secureboot_enroll.md): Enrol Secure Boot keys in the firmware of a node in maintenance mode whose firmware is in Setup Mode
- [chalkctl secureboot new](chalkctl_secureboot_new.md): Create Secure Boot keys: PK, KEK and db
