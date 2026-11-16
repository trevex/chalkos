---
title: "Config redaction in code"
description: "How the type config.Redacted keeps secret values away from the programs that read a node's config"
---

# Config redaction in code

[Secrets reach only the units granted their kind](../../how-it-works/node/config.md#secrets-reach-only-the-units-granted-their-kind) explains who sees secret values; this page shows how the code enforces it.

## Programs take config.Redacted

Rules, renderers, validators, command facts and an image's checker read a node's config as
`config.Redacted`. It holds the documents with each value of a `Secret` replaced by its SHA-256.
The same goes for each Secret object a document embeds, such as a Kubernetes Secret in an `Addon`.

The packages that hand documents to programs are `rules`, `outputs`, `check` and `imagecheck`.
They take `config.Redacted` and no plain documents, so the compiler refuses a call that would pass
secret values to a program.

## Every source of redacted documents is one call

Only these functions return a `config.Redacted`:

- `config.Redact` hides the values of documents that hold them.
- `config.RedactedObjects` and `config.ParseRedacted` read documents that `Redact` hid where they
  were written. They serve chalkd's baseline of the effects, the input chalkd gives an image's
  checker, and the config GetConfig serves.
- `Redacted.Defaulted` and `config.TranslateRedacted` fill in the defaults of an image's schemas.

`RedactedObjects` and `ParseRedacted` check nothing. They trust their callers to pass documents
that were redacted when they were written. `Redact` takes plain documents, and a
`config.Redacted` does not convert to them, so no value is hashed twice.

## Hashes read the redacted form

The config's version, `config.Hash`, is the SHA-256 of the redacted documents in canonical JSON.
`config.KindHashes`, which the effects compare, hashes each kind's redacted documents the same
way. The code is in `pkg/config/redact.go` and `pkg/config/hash.go`.
