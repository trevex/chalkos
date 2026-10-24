---
title: "chalkctl completion"
description: "Write a shell completion script"
---

# chalkctl completion

```sh
chalkctl completion bash [flags]
chalkctl completion fish [flags]
chalkctl completion powershell [flags]
chalkctl completion zsh [flags]
```

Writes a script that completes chalkctl's commands and flags in bash, fish, PowerShell or zsh.
Each shell's command says in its help how to load the script into every new session. The command
needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Command that loads the script into the current session |
| --- | --- |
| bash | `source <(chalkctl completion bash)` |
| fish | `chalkctl completion fish | source` |
| powershell | `chalkctl completion powershell | Out-String | Invoke-Expression` |
| zsh | `source <(chalkctl completion zsh)` |

## Examples

```sh
# Complete chalkctl's commands in every new bash session.
chalkctl completion bash > ~/.local/share/bash-completion/completions/chalkctl
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
