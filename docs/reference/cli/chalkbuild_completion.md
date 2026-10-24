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
The table of shells says how to load it into the current session and into every new one. The
command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Current session | Every new session |
| --- | --- | --- |
| bash | `source <(chalkbuild completion bash)` | With the bash-completion package installed, `chalkbuild completion bash > ~/.local/share/bash-completion/completions/chalkbuild` once. |
| fish | `chalkbuild completion fish | source` | `chalkbuild completion fish > ~/.config/fish/completions/chalkbuild.fish` once. |
| powershell | `chalkbuild completion powershell | Out-String | Invoke-Expression` | `chalkbuild completion powershell | Out-String | Invoke-Expression` in the PowerShell profile, `$PROFILE`. |
| zsh | `source <(chalkbuild completion zsh)` | With completion enabled by `autoload -U compinit; compinit` in `~/.zshrc`, `chalkbuild completion zsh > "${fpath[1]}/_chalkbuild"` once. |

## Examples

```sh
# Complete chalkbuild's commands in every new bash session.
chalkbuild completion bash > ~/.local/share/bash-completion/completions/chalkbuild
```

## See also

- [chalkbuild](chalkbuild.md): Prepare the images Nix builds for a chalkos cluster's nodes
