---
title: "chalkbuild completion"
description: "Generate the autocompletion script for the specified shell"
---

# chalkbuild completion

Generate the autocompletion script for the specified shell

## Synopsis

Writes a script that completes chalkbuild's commands and flags in a shell. Each shell's command
says how to load the script. The command needs no secrets file or client file.

## Examples

```
  # Complete chalkbuild's commands in every new bash session.
  chalkbuild completion bash > ~/.local/share/bash-completion/completions/chalkbuild
```

## Options

```
  -h, --help   help for completion
```

## SEE ALSO

* [chalkbuild](chalkbuild.md)	 - Prepare the images Nix builds for a chalkos cluster's nodes
* [chalkbuild completion bash](chalkbuild_completion_bash.md)	 - Generate the autocompletion script for bash
* [chalkbuild completion fish](chalkbuild_completion_fish.md)	 - Generate the autocompletion script for fish
* [chalkbuild completion powershell](chalkbuild_completion_powershell.md)	 - Generate the autocompletion script for powershell
* [chalkbuild completion zsh](chalkbuild_completion_zsh.md)	 - Generate the autocompletion script for zsh

