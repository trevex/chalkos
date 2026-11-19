# Test secrets

These keys and certificates are for the chalkos tests alone. They are public: anyone can read
them here and in any Nix store that holds the test images. Never trust them, or anything they
sign, outside the test VMs.

- `secrets.json`: cluster secrets in the format of `chalkctl gen secrets --plaintext`, with the
  OS CA, the node CA, the Kubernetes, front-proxy and etcd CAs, the service account key, and the
  recovery and encryption secrets. `nix/testing/secrets.nix` builds the package `test-secrets`
  from it.
- `secureboot/`: the Secure Boot keys PK, KEK and db with their certificates.
  `nix/testing/secureboot.nix` builds the package `test-secureboot` from them, with an OVMF
  variable store that has them enrolled.

The files are checked in so that every machine builds the same test images, and a binary cache
serves images that match the secrets beside them. `generate.sh` writes them anew; run it in the
development shell to rotate them.
