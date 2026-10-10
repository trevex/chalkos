---
title: "chalklab completion powershell"
description: "Generate the autocompletion script for powershell"
---

# chalklab completion powershell

Generate the autocompletion script for powershell

## Synopsis

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	chalklab completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


```
chalklab completion powershell [flags]
```

## Options

```
  -h, --help              help for powershell
      --no-descriptions   disable completion descriptions
```

## SEE ALSO

* [chalklab completion](chalklab_completion.md)	 - Generate the autocompletion script for the specified shell

