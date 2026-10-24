---
title: "chalklab completion"
description: "Write a shell completion script"
---

# chalklab completion

```sh
chalklab completion bash [flags]
chalklab completion fish [flags]
chalklab completion powershell [flags]
chalklab completion zsh [flags]
```

Writes a script that completes chalklab's commands and flags in bash, fish, PowerShell or zsh.
Each shell's command says in its help how to load the script into every new session. The command
needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Command that loads the script into the current session |
| --- | --- |
| bash | `source <(chalklab completion bash)` |
| fish | `chalklab completion fish | source` |
| powershell | `chalklab completion powershell | Out-String | Invoke-Expression` |
| zsh | `source <(chalklab completion zsh)` |

## Examples

```sh
# Complete chalklab's commands in every new bash session.
chalklab completion bash > ~/.local/share/bash-completion/completions/chalklab
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
