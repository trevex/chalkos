---
title: "chalkctl completion zsh"
description: "Generate the autocompletion script for zsh"
---

# chalkctl completion zsh

Generate the autocompletion script for zsh

## Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(chalkctl completion zsh)

To load completions for every new session, execute once:

#### Linux:

	chalkctl completion zsh > "${fpath[1]}/_chalkctl"

#### macOS:

	chalkctl completion zsh > $(brew --prefix)/share/zsh/site-functions/_chalkctl

You will need to start a new shell for this setup to take effect.


```
chalkctl completion zsh [flags]
```

## Options

```
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
```

## SEE ALSO

* [chalkctl completion](chalkctl_completion.md)	 - Generate the autocompletion script for the specified shell

