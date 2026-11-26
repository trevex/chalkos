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
The table of shells says how to load it into the current session and into every new one. The
command needs no secrets file or client file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` | Complete commands and flags without their descriptions. |

## Shells

| Shell | Current session | Every new session |
| --- | --- | --- |
| bash | `source <(chalklab completion bash)` | With the bash-completion package installed, `chalklab completion bash > ~/.local/share/bash-completion/completions/chalklab` once. |
| fish | <code>chalklab completion fish &#124; source</code> | `chalklab completion fish > ~/.config/fish/completions/chalklab.fish` once. |
| powershell | <code>chalklab completion powershell &#124; Out&#45;String &#124; Invoke&#45;Expression</code> | <code>chalklab completion powershell &#124; Out&#45;String &#124; Invoke&#45;Expression</code> in the PowerShell profile, `$PROFILE`. |
| zsh | `source <(chalklab completion zsh)` | With completion enabled by `autoload -U compinit; compinit` in `~/.zshrc`, `chalklab completion zsh > "${fpath[1]}/_chalklab"` once. |

## Examples

```sh
# Complete chalklab's commands in every new bash session.
chalklab completion bash > ~/.local/share/bash-completion/completions/chalklab
```

## See also

- [chalklab](chalklab.md): Run a chalkos cluster's nodes as QEMU virtual machines on this machine
