---
title: "chalkbuild completion"
description: "Write a shell completion script"
---

# chalkbuild completion

```sh
chalkbuild completion bash [flags]
chalkbuild completion fish [flags]
chalkbuild completion powershell [flags]
chalkbuild completion zsh [flags]
```

Writes a script that completes chalkbuild's commands and flags in bash, fish, PowerShell or zsh.
Each shell's command says in its help how to load the script into every new session. The command
needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Command that loads the script into the current session |
| --- | --- |
| bash | `source <(chalkbuild completion bash)` |
| fish | `chalkbuild completion fish | source` |
| powershell | `chalkbuild completion powershell | Out-String | Invoke-Expression` |
| zsh | `source <(chalkbuild completion zsh)` |

## Examples

```sh
# Complete chalkbuild's commands in every new bash session.
chalkbuild completion bash > ~/.local/share/bash-completion/completions/chalkbuild
```

## See also

- [chalkbuild](chalkbuild.md): Prepare the images Nix builds for a chalkos cluster's nodes
