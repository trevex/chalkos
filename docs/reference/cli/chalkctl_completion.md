---
title: "chalkctl completion"
description: "Generate the autocompletion script for the specified shell"
---

# chalkctl completion

Generate the autocompletion script for the specified shell

## Synopsis

Writes a script that completes chalkctl's commands and flags in a shell. Each shell's command
says how to load the script. The command needs no secrets file or client file.

## Examples

```
  # Complete chalkctl's commands in every new bash session.
  chalkctl completion bash > ~/.local/share/bash-completion/completions/chalkctl
```

## Options

```
  -h, --help   help for completion
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters
* [chalkctl completion bash](chalkctl_completion_bash.md)	 - Generate the autocompletion script for bash
* [chalkctl completion fish](chalkctl_completion_fish.md)	 - Generate the autocompletion script for fish
* [chalkctl completion powershell](chalkctl_completion_powershell.md)	 - Generate the autocompletion script for powershell
* [chalkctl completion zsh](chalkctl_completion_zsh.md)	 - Generate the autocompletion script for zsh

