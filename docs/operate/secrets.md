---
title: "Manage secrets"
description: "Generate and rotate the cluster secrets file and source service credentials from local files, age, or SOPS"
---

# Manage secrets

The cluster secrets file holds the CAs, private keys, and recovery material that chalkctl
uses to administer the cluster. A `Secret` document supplies credentials to node services,
such as a BGP password. Keep the cluster bundle in a dedicated file and use `Secret`
documents for service credentials.

## Generate the cluster's secrets with age

The secrets file holds every key of the cluster: the OS CA and the node CA with their keys, the
Kubernetes CAs and keys, and the secret every node's recovery key derives from. Whoever holds it
can install a node into the cluster, issue themselves any client certificate and decrypt every
node's disks with physical access to them.

Generate it once, in the repository's root, encrypted to each administrator's key and to a
backup key that you keep offline:

```console
$ chalkctl gen secrets --recipient "$(cat ~/.ssh/id_ed25519.pub)" \
    --recipient age1nmrqj76cqyf4ft8xtczufd3mtdk0xf2j7tevgwnzjp3zq9tffu3slx5eux
encrypted secrets.age to ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKMcR1DCFPGSdWXphQ3We8UmkMp2KdufUzVxxC6Hvo9k alice@workstation, age1nmrqj76cqyf4ft8xtczufd3mtdk0xf2j7tevgwnzjp3zq9tffu3slx5eux
wrote secrets.age and secrets.pub.json
```

A recipient is an age public key, an SSH public key or an age plugin recipient. A hardware key
works through a plugin such as age-plugin-yubikey, whose binary must be on the `PATH` of whoever
runs chalkctl.

The command writes two files:

- `secrets.age` is the secrets file, encrypted to the recipients.
- `secrets.pub.json` is its public half, with certificates and no keys.

`gen secrets` refuses to overwrite either file, because new secrets would lock you out of every
installed node:

```console
$ chalkctl gen secrets --recipient age1nmrqj76cqyf4ft8xtczufd3mtdk0xf2j7tevgwnzjp3zq9tffu3slx5eux
chalkctl: secrets.age exists; the secrets are generated once per cluster
```

To decrypt the file, chalkctl tries the identities `--identity` names. Without `--identity`, it
tries `~/.config/chalkos/age.key`, `~/.ssh/id_ed25519` and `~/.ssh/id_rsa`, skipping those that
do not exist or that age cannot use. It asks on the terminal for an SSH key's passphrase.

Use `--plaintext` in place of `--recipient` to generate `secrets.json` for a location protected
by other means. Keep it out of version control. Committing `secrets.age` puts its encrypted
contents under the same history as the cluster. Protect the recipients' keys and keep an
offline backup.

!!! danger "A lost secrets file cannot be generated again"

    New secrets do not match the installed nodes. Without the file you cannot install or
    reinstall a node, deliver config, issue client files or derive a recovery key. Back it up to
    a second place, as
    [Back up what you cannot rebuild](recurring-tasks.md#back-up-what-you-cannot-rebuild) lists.
    [Recover a node](recover-node.md#the-secrets-file-is-lost) lists what still works without it.

## Generate the cluster's secrets with SOPS

Configure a SOPS creation rule that encrypts every value in `secrets.enc.json` and make
SOPS and its key provider available on the workstation. For a new cluster, stream the
generated JSON bundle directly to SOPS. Run this from the cluster repository with Bash.

```sh
(
  set -o pipefail -o noclobber
  chalkctl gen secrets --plaintext --out - --public-out secrets.pub.json |
    sops encrypt --filename-override secrets.enc.json --input-type json \
      --output-type json /dev/stdin > secrets.enc.json
)
```

Keep `secrets.pub.json` alongside the cluster configuration for the image build. The private
bundle goes into `secrets.enc.json`, and the plaintext travels through the pipe.
See the [SOPS documentation](https://github.com/getsops/sops).

## Read the cluster secrets bundle

Pass `--secrets` the whole cluster bundle. The decrypted file has the generated bundle's
fields at the top level. Keep service secrets in their own files.

| `--secrets` argument | How chalkctl reads it |
| --- | --- |
| `secrets.age` | Decrypts the local file with age. |
| `secrets.enc.json` or `secrets.enc.yaml` | Decrypts the local file with SOPS. The filename contains `.enc.` followed by an extension. |
| `secrets.json` | Reads the local file as plaintext, or decrypts it with age when it holds an age file. |
| `-` | Reads a native JSON or age bundle from standard input. |
| `sops://./cluster.enc.yaml` | Uses the named provider and reads the full file. |

An explicit provider takes precedence over the filename. For a bare path matching both
encrypted filename patterns, `.age` takes precedence. URI fragments are for service values in
`Secret` documents. In a bare path, `#` is a literal filename character.

## Rotate the cluster secrets file in place

`chalkctl rotate` saves the CA or key material and the current phase in the cluster bundle.
When a rotation pauses or is interrupted, `--resume` and `--finish` read that state from
the same file. A dedicated file gives the rotation one place to lock and replace atomically
and keeps service credentials outside that write. The `.prev` file holds the bundle from
before the rotation. Keep `.prev` and `.lock` out of version control.

```sh
chalkctl rotate kubernetes-ca --secrets=secrets.enc.json
```

Follow the command's instructions at each pause. For SOPS, use version 3.11 or newer: chalkctl
updates the file in place, using its existing SOPS keys and encryption settings.
Native JSON and age bundles also support the existing `--out` workflow, which is required
when using stdin for a rotation. See the
[Kubernetes CA rotation guide](../kubernetes/rotate-kubernetes-cas.md) and
[OS CA rotation guide](access.md#rotate-the-os-ca).

## Keep the services' secrets in Secret documents

A `Secret` document is a config document, unrelated to the secrets file. It holds values that
the node's services read, such as a password for a BGP peer, and it reaches the node with the
rest of the node's config. The homelab's `bgp/secrets.yaml` gives the three nodes one, which the
`BGPPeer` documents `router-enp1s0` and `router-enp2s0` in `bgp/fabric.yaml` name in a
`secretRef`:

```yaml title="bgp/secrets.yaml"
--8<-- "examples/homelab/bgp/secrets.yaml"
```

Use `spec.data` for literal values or `spec.sources` to read values from other files.
Each key in `spec.sources` requires a [provider URI](#secret-source-uris). For example,
this `Secret` takes its password from the `bgp.password` field of a SOPS file.

```text title="bgp/secrets.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: bgp}
spec:
  sources:
    password: "sops://./fabric.enc.yaml#/bgp/password"
```

The path is relative to `bgp/secrets.yaml`, so this reads `bgp/fabric.enc.yaml`. Name the
`Secret` document in the inventory and keep the provider file outside its config file globs.
Other documents still use `secretRef: {name: bgp, key: password}`. A document can mix literal
and sourced keys. Defining the same key in both maps is an error.

The commands that deliver or compare the config, such as `config apply` with or without `--diff`
and `--dry-run`, `upgrade` and `install`, resolve the sources. They read and decrypt each source
file once per command and put the values in `spec.data` before checking and delivering the
config. `chalkctl validate` and `chalkctl status` decrypt no `sops://` or `age://` source, so they
ask for no key: `validate` checks a placeholder in place of each such value and prints no config
version, and `status` does not compare the node's config with the inventory's.
[Change a node's config](change-config.md#change-the-files-and-validate-them) and
[Read the config line](status-and-logs.md#read-the-config-line) show their output. Run
`chalkctl config apply` after changing a provider value.
Use inventories you trust, since their sources can read files available to the user running
chalkctl. On the node, chalkd keeps the delivered `Secret` documents readable by root alone and
hands each value to units granted the kind that references it.

## Secret source URIs

Both `Secret.spec.sources` and `--secrets` use explicit provider URIs. A URI fragment selects a service value in `Secret.spec.sources`.

| Provider | Example | Reads |
| --- | --- | --- |
| `file://` | `file://./password.txt` | A plaintext local file. |
| `sops://` | `sops://./fabric.enc.yaml#/bgp/password` | A local file decrypted by the `sops` executable on `PATH`, using its configured keys. |
| `age://` | `age://./vault.yaml.age#/bgp/password` | A local file decrypted by chalkctl using `--identity` or the default age and SSH identities. |

The part after `://` is a local path. Use `file:///etc/chalkos/password.txt` for an absolute
path. Relative paths start at the declaring YAML file for `spec.sources` and in the working
directory for `--secrets`. Escape a literal `#` in a URI path as `%23`. Quote URIs in shell
commands.

The optional fragment is a JSON Pointer into a single YAML or JSON document. For example,
`#/bgp/password` selects the password field under `bgp`, `#/peers/0` selects the first item
in `peers`, and `~1` and `~0` escape a slash and a tilde in a key. A source without a
fragment returns the whole file after decryption. A service key selects a string or reads
the full UTF-8 text, including any trailing newline.
