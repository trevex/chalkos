---
title: "chalkbuild completion fish"
description: "Generate the autocompletion script for fish"
---

# chalkbuild completion fish

Generate the autocompletion script for fish

## Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	chalkbuild completion fish | source

To load completions for every new session, execute once:

	chalkbuild completion fish > ~/.config/fish/completions/chalkbuild.fish

You will need to start a new shell for this setup to take effect.


```
chalkbuild completion fish [flags]
```

## Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

## SEE ALSO

* [chalkbuild completion](chalkbuild_completion.md)	 - Generate the autocompletion script for the specified shell

