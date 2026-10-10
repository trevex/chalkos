---
title: "chalkctl sign"
description: "Sign the boot loader and UKIs of a disk image"
---

# chalkctl sign

Sign the boot loader and UKIs of a disk image

## Synopsis

Signs the boot loader and the UKIs on the EFI system partition of a raw disk image, in place,
with a Secure Boot db key, so firmware that trusts its certificate boots the image.
--repart-json is the repart-output.json that describes the image's partitions. Ctrl-C stops the
signing and can leave the image partly signed. The command needs no secrets file or client file.

```
chalkctl sign [flags]
```

## Examples

```
  chalkctl sign --image chalkos.raw --repart-json repart-output.json --key db.key --cert db.crt
```

## Options

```
      --cert string          PEM certificate of the Secure Boot db signer
  -h, --help                 help for sign
      --image string         raw disk image to sign in place
      --key string           PEM private key of the Secure Boot db signer
      --repart-json string   repart-output.json describing the image's partitions
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

