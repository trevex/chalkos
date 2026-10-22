package chalkbuild

// The long descriptions and examples of chalkbuild's commands, which its help and the generated
// reference pages show.

const rootLong = `chalkbuild prepares the images Nix builds for a chalkos cluster's nodes, on the machine that
holds the Secure Boot db key. Nix builds images unsigned, so the key never enters the Nix store or
a binary cache; chalkbuild sign signs a copy of an image directory, which the inventory then names
and chalkctl installs and upgrades nodes with. chalkbuild never talks to nodes, and its commands
need no secrets file or client file.`

const rootExample = `  nix build .#node-metal -o build/node-metal
  chalkbuild sign --key db.key --cert db.crt build/node-metal images/node-metal`

const signLong = `Copies an image directory, as nix build makes it, to a new directory and signs the copy with a
Secure Boot db key: the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
ESP of its raw image and, in the installer's directory, of its ISO. No other file on the ESP gains
the signature. The raw image and the ISO are copied without their holes and the other entries
with symlinks resolved, so the copy holds no link into the Nix store and the input stays as it
is. chalkbuild then checks the UKI and the boot loader of each signed image against --cert, as
firmware with that certificate in db would. The output directory must not exist; when anything
fails, or Ctrl-C stops the command, it is removed again. The command needs no secrets file or
client file.`

const signExample = `  # Sign the build of the node-metal image into the directory the inventory names.
  chalkbuild sign --key db.key --cert db.crt build/node-metal images/node-metal

  # Sign the installer, raw image and ISO.
  chalkbuild sign --key db.key --cert db.crt installer installer-signed`
