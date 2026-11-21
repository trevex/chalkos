
## chalkosLib { #chalkoslib }

Every image's NixOS modules get `chalkosLib` as a module argument; add it to a module's
arguments, as in `{ chalkosLib, config, lib, ... }:`. Code inside the module, submodules
included, uses that argument. The flake's `lib` holds the same functions, beside `mkImages`, for
code outside an image.

`chalkosLib` holds the type and the `mkOption` of an extension kind's fields, the renderers of
`chalkos.outputs.<name>.render`, the checks of `chalkos.outputs.<name>.check`, the validators of
`chalkos.validators.<name>.program` and the common effects. `chalkosLib.internal` holds
chalkos's own helpers; it is not part of the extension API and changes without notice.

| Helper | Arguments | Result |
| --- | --- | --- |
| `chalkosLib.mkOption`{ #chalkoslibmkoption } | `lib.mkOption`'s; `rules`, a list of `{ rule; message or messageExpression; takes ? "refuse"; description ? null; }`; `unique`, `true` or one or a list of `{ across ? "kind" or "list"; by ? [ field or { field; as; } ]; as ? null; hint ? null; }`; and `maxItems`, a positive integer | An option for a field of a kind, with the field's change rules, the values that must not repeat and the most items a list holds. The build fails on a `unique` that does not fit the field's type and on a `maxItems` on a field that is no list. |
| `chalkosLib.types.ref`{ #chalkoslibtypesref } | a kind's name, or `{ kind; reserved ? [ ]; }` | Type of a field that holds the name of a document of the kind. The build fails when the image lacks the kind. chalkctl and chalkd refuse a config whose field names a document the config lacks. Names in `reserved` stand for something that is no document: the field takes them as they are, and no document of the kind may take them as its name. |
| `chalkosLib.types.secretRef`{ #chalkoslibtypessecretref } | — | Type of a field that holds a reference to a key of a Secret: `secretRef` with `name` and `key`. |
| `chalkosLib.types.address`{ #chalkoslibtypesaddress } | — | Type of a field that holds an IPv4 or IPv6 address. Its schema has `format: ip`: chalkctl and chalkd refuse an IPv4 address with leading zeros or a number above 255, and an IPv6 address with a zone. Rules read the value with `ip()`. |
| `chalkosLib.types.prefix`{ #chalkoslibtypesprefix } | — | Type of a field that holds an address range: an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6. Its schema has `format: cidr`, which chalkctl and chalkd check. Rules read the value with `cidr()`. |
| `chalkosLib.types.portRange`{ #chalkoslibtypesportrange } | — | Type of a field that holds a port from 1 to 65535, as a number, or a range of ports, as the string `"<first>-<last>"`. Its schema has `format: port-range` for the string: chalkctl and chalkd refuse a range whose first port is higher than its last. |
| `chalkosLib.types.imageReference`{ #chalkoslibtypesimagereference } | — | Type of a field that holds a container image reference: a registry, a repository and a tag, a digest or both, as `registry.example/app:v1.2.3@sha256:<digest>`. Its schema has `format: image-reference`, which chalkctl and chalkd check. |
| `chalkosLib.render.template`{ #chalkoslibrendertemplate } | a path ending in `.tmpl` | Renderer of a Go `text/template` into one file, named as the template less `.tmpl`; a missing or null key prints as nothing. |
| `chalkosLib.render.jq`{ #chalkoslibrenderjq } | a path ending in `.jq` | Renderer of a jq program into one file, named as the program less `.jq`; a string is written raw. |
| `chalkosLib.render.documents`{ #chalkoslibrenderdocuments } | a list of kind names | Renderer of the documents of those kinds as they are, each as `<Kind>/<name>.json`. |
| `chalkosLib.check.run`{ #chalkoslibcheckrun } | a list of a program and its arguments, none with white space | Check that runs the program once, with `$OUT` in the arguments replaced by the directory of the rendered files. It fails when the program exits with a status other than 0. |
| `chalkosLib.check.each`{ #chalkoslibcheckeach } | a glob below `$OUT`, then a list of a program and its arguments, none with white space | Check that runs the program once for each file the glob matches, in the order of their names, with `$FILE` in the arguments replaced by the file's path and `$OUT` by the directory. It passes when no file matches, and fails at the first file the program refuses. |
| `chalkosLib.validate.jq`{ #chalkoslibvalidatejq } | a path to a jq program | Validator that runs the jq program on its input, the documents by kind as a renderer reads them; what the program outputs is the report of refusals. |
| `chalkosLib.restartOn`{ #chalkoslibrestarton } | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that restarts the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
| `chalkosLib.reloadOn`{ #chalkoslibreloadon } | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that reloads the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
