---
title: "chalkbuild completion powershell"
description: "Generate the autocompletion script for powershell"
---

# chalkbuild completion powershell

Generate the autocompletion script for powershell

## Synopsis

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	chalkbuild completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


```
chalkbuild completion powershell [flags]
```

## Options

```
  -h, --help              help for powershell
      --no-descriptions   disable completion descriptions
```

## SEE ALSO

* [chalkbuild completion](chalkbuild_completion.md)	 - Generate the autocompletion script for the specified shell

