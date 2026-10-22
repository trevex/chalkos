---
title: "chalkbuild sign"
description: "Sign a copy of an image directory for Secure Boot"
---

# chalkbuild sign

Sign a copy of an image directory for Secure Boot

## Synopsis

Copies an image directory, as nix build makes it, to a new directory and signs the copy with a
Secure Boot db key: the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
ESP of its raw image and, in the installer's directory, of its ISO. No other file on the ESP gains
the signature. The raw image and the ISO are copied without their holes and the other entries
with symlinks resolved, so the copy holds no link into the Nix store and the input stays as it
is. chalkbuild then checks the UKI and the boot loader of each signed image against --cert, as
firmware with that certificate in db would. The output directory must not exist; when anything
fails, or Ctrl-C stops the command, it is removed again. The command needs no secrets file or
client file.

```
chalkbuild sign <image dir> <out dir> [flags]
```

## Examples

```
  # Sign the build of the node-metal image into the directory the inventory names.
  chalkbuild sign --key db.key --cert db.crt build/node-metal images/node-metal

  # Sign the installer, raw image and ISO.
  chalkbuild sign --key db.key --cert db.crt installer installer-signed
```

## Options

```
      --cert string   PEM certificate of the Secure Boot db signer, which the signed copy is checked against
  -h, --help          help for sign
      --key string    PEM private key of the Secure Boot db signer
```

## SEE ALSO

* [chalkbuild](chalkbuild.md)	 - Prepare the images Nix builds for a chalkos cluster's nodes

