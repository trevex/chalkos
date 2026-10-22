---
title: "chalkbuild completion bash"
description: "Generate the autocompletion script for bash"
---

# chalkbuild completion bash

Generate the autocompletion script for bash

## Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(chalkbuild completion bash)

To load completions for every new session, execute once:

#### Linux:

	chalkbuild completion bash > /etc/bash_completion.d/chalkbuild

#### macOS:

	chalkbuild completion bash > $(brew --prefix)/etc/bash_completion.d/chalkbuild

You will need to start a new shell for this setup to take effect.


```
chalkbuild completion bash
```

## Options

```
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
```

## SEE ALSO

* [chalkbuild completion](chalkbuild_completion.md)	 - Generate the autocompletion script for the specified shell

