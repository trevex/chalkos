---
title: "chalklab completion"
description: "Generate the autocompletion script for the specified shell"
---

# chalklab completion

Generate the autocompletion script for the specified shell

## Synopsis

Writes a script that completes chalklab's commands and flags in a shell. Each shell's command
says how to load the script. The command needs no secrets file or client file.

## Examples

```
  # Complete chalklab's commands in every new bash session.
  chalklab completion bash > ~/.local/share/bash-completion/completions/chalklab
```

## Options

```
  -h, --help   help for completion
```

## SEE ALSO

* [chalklab](chalklab.md)	 - Run a chalkos cluster's nodes as QEMU virtual machines on this machine
* [chalklab completion bash](chalklab_completion_bash.md)	 - Generate the autocompletion script for bash
* [chalklab completion fish](chalklab_completion_fish.md)	 - Generate the autocompletion script for fish
* [chalklab completion powershell](chalklab_completion_powershell.md)	 - Generate the autocompletion script for powershell
* [chalklab completion zsh](chalklab_completion_zsh.md)	 - Generate the autocompletion script for zsh

