
## lib.chalkos { #lib-chalkos }

Every image's NixOS modules get `lib.chalkos` as part of `lib`. It holds the type and the
`mkOption` of an extension kind's fields, the renderers of `chalkos.outputs.<name>.render`
and the common effects.

| Helper | Arguments | Result |
| --- | --- | --- |
| `lib.chalkos.mkOption` | `lib.mkOption`'s; `rules`, a list of `{ rule; message or messageExpression; takes ? "refuse"; description ? null; }`; and `unique`, `true` or one or a list of `{ across ? "kind" or "list"; by ? [ field or { field; as; } ]; as ? null; hint ? null; }` | An option for a field of a kind, with the field's change rules and the values that must not repeat. The build fails on a `unique` that does not fit the field's type. |
| `lib.chalkos.types.ref` | a kind's name, or `{ kind; reserved ? [ ]; }` | Type of a field that holds the name of a document of the kind. The build fails when the image lacks the kind. chalkctl and chalkd refuse a config whose field names a document the config lacks. Names in `reserved` stand for something that is no document: the field takes them as they are, and no document of the kind may take them as its name. |
| `lib.chalkos.types.secretRef` | — | Type of a field that holds a reference to a key of a Secret: `secretRef` with `name` and `key`. |
| `lib.chalkos.render.template` | a path ending in `.tmpl` | Renderer of a Go `text/template` into one file, named as the template less `.tmpl`; a missing or null key prints as nothing. |
| `lib.chalkos.render.jq` | a path ending in `.jq` | Renderer of a jq program into one file, named as the program less `.jq`; a string is written raw. |
| `lib.chalkos.render.documents` | a list of kind names | Renderer of the documents of those kinds as they are, each as `<Kind>/<name>.json`. |
| `lib.chalkos.restartOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that restarts the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
| `lib.chalkos.reloadOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that reloads the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
