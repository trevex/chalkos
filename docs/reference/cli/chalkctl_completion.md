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
The table of shells says how to load it into the current session and into every new one. The
command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Current session | Every new session |
| --- | --- | --- |
| bash | `source <(chalkctl completion bash)` | With the bash-completion package installed, `chalkctl completion bash > ~/.local/share/bash-completion/completions/chalkctl` once. |
| fish | `chalkctl completion fish | source` | `chalkctl completion fish > ~/.config/fish/completions/chalkctl.fish` once. |
| powershell | `chalkctl completion powershell | Out-String | Invoke-Expression` | `chalkctl completion powershell | Out-String | Invoke-Expression` in the PowerShell profile, `$PROFILE`. |
| zsh | `source <(chalkctl completion zsh)` | With completion enabled by `autoload -U compinit; compinit` in `~/.zshrc`, `chalkctl completion zsh > "${fpath[1]}/_chalkctl"` once. |

## Examples

```sh
# Complete chalkctl's commands in every new bash session.
chalkctl completion bash > ~/.local/share/bash-completion/completions/chalkctl
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
