package chalkbuild

// The long descriptions and examples of chalkbuild's commands, which its help and the generated
// reference pages show.

const rootLong = `chalkbuild prepares the images Nix builds for a chalkos cluster's nodes, on the machine that
holds the Secure Boot db key. Nix builds images unsigned, so the key never enters the Nix store or
a binary cache. chalkbuild sign signs a copy of an image directory or of an OCI image layout, which
chalkbuild oci and mkImages's oci outputs write, and chalkbuild push pushes a signed layout to a
registry. The inventory names a signed image directory, or the reference push prints. chalkbuild
never talks to nodes, and its commands need no secrets file or client file.`

const rootExample = `  nix build .#node-metal -o images/node-metal
  chalkbuild sign --key db.key --cert db.crt images/node-metal images/node-metal-signed

  # Publish the image to a registry.
  nix build .#node-metal-oci -o oci/node-metal
  chalkbuild sign --key db.key --cert db.crt oci/node-metal oci/node-metal-signed
  chalkbuild push oci/node-metal-signed ghcr.io/example/node-metal:v1`

const ociLong = `Writes an image directory, as nix build makes it, as an OCI image layout: one manifest of the
artifact type application/vnd.chalkos.image.v1. Its config is the image's header: its ID, root
hash, architecture, platform, revision and tag. Its layers, uncompressed, are the store data the
root hash covers, the hash tree, the UKI, the boot loader and the contract, a tar of metadata.json,
schemas/, repart.d/ and image.json. The same image directory always makes the same layout, and
mkImages's oci outputs run this command. The layout directory must not exist; when anything
fails, or Ctrl-C stops the command, it is removed again. The command needs no secrets file or
client file.`

const ociExample = `  chalkbuild oci images/node-metal oci/node-metal`

const pushLong = `Pushes the chalkos image of an OCI image layout, as chalkbuild oci or chalkbuild sign writes
it, to a registry, and prints the reference of its manifest by digest,
<repository>@sha256:<digest>, which an inventory names as the image's oci source. The reference to
push to names its registry and a tag or a digest. chalkbuild authenticates with the credentials
docker login stores in docker's config, or with its credential helpers. It reaches a registry over
plain HTTP at localhost with a port, at a name ending in .localhost, at 127.0.0.1, at ::1, or at a
private (RFC 1918) address, and any other registry over HTTPS. Some registries, Quay among them,
reject a manifest of an artifact type they do not recognize unless an operator allow-lists it; the
registry's own documentation names the setting. The command needs no secrets file or client file.`

const pushExample = `  chalkbuild push oci/node-metal ghcr.io/example/node-metal:v1.5.0`

const signLong = `Copies an image directory, as nix build makes it, to a new directory and signs the copy with a
Secure Boot db key: the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
ESP of its raw image and, in the installer's directory, of its ISO. No other file on the ESP gains
the signature. The raw image and the ISO are copied without their holes and the other entries
with symlinks resolved, so the copy holds no link into the Nix store and the input stays as it
is. chalkbuild then checks the UKI and the boot loader of each signed image against --cert, as
firmware with that certificate in db would. The output directory must not exist; when anything
fails, or Ctrl-C stops the command, it is removed again. The command needs no secrets file or
client file.

Given an OCI image layout, as chalkbuild oci writes it, it signs copies of the UKI and boot-loader
layers, checks them against --cert, and writes a new layout with them in place of the old layers
and a new manifest; the store, hash tree and contract layers are copied as they are. It prints the
new manifest's digest.`

const signExample = `  # Sign the build of the node-metal image into the directory the inventory names.
  chalkbuild sign --key db.key --cert db.crt images/node-metal images/node-metal-signed

  # Sign the installer, raw image and ISO.
  chalkbuild sign --key db.key --cert db.crt installer installer-signed

  # Sign an OCI image layout.
  chalkbuild sign --key db.key --cert db.crt oci/node-metal oci/node-metal-signed`
