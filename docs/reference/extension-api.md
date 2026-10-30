---
title: "Extension API"
description: "The options and lib.chalkos helpers that an extension declares kinds, facts, outputs, effects and diagnostics with"
---

# Extension API

This page is for authors of extensions, the NixOS modules that declare config kinds of their
own, read a node's documents in a service, react when they change, and give operators
diagnostics. It lists the options the node modules give an extension and the helpers of
`lib.chalkos`. The Build pages show them at work, from
[Declare a config kind](../build/declare-a-kind.md) to
[Add a diagnostic](../build/add-a-diagnostic.md).

## Kinds { #kinds }

The config kinds an image accepts, and the kinds a unit reads.

### `chalkos.kinds` { #chalkoskinds }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/kinds/default.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/default.nix)

Config kinds the image accepts, by kind name. A node's config is a set of documents of
these kinds; chalkctl and chalkd validate each against the kind's schema.

The options within each, by their path below `chalkos.kinds`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.description`{ #chalkoskindsdescription } | string or null | `null` | Sentence that says what a document of the kind is for, which the kind's schema carries as its description. Example: `"The rack the node is mounted in."` |
| `<name>.dir`{ #chalkoskindsdir } | string, read-only | `"/run/chalkos/config/${group}/<kind>"` | Directory on the running node that holds the node's documents of the kind, one JSON file per document. |
| `<name>.get`{ #chalkoskindsget } | function returning string, read-only | a function of `{ name ? null, path ? null, output ? null }` | Builds a shell command that prints the node's documents of the kind with chalkcfg. `name` picks one document, and must be a document name such as `rack`; without it the command prints every document of the kind, one line each. `path` prints one field, such as `spec.location`: fields separated by dots, `*` or an index for the elements of a list, and a key or `*` for the values of an attribute set. The path must follow the kind's options, or the evaluation fails and lists the fields where it went wrong. `output` passes `json`, `yaml`, `jsonpath=<expression>` or `go-template=<template>` instead of a path. Run the command from a shell script, such as `pkgs.writeShellScript`, since systemd expands `$` and `%` in `ExecStart=` itself. A path is checked against the kind's options, not against the documents. Where a document leaves the field out, or its list is shorter than the index, the command exits with status 1; without `name`, one such document fails the whole command. A field set to null prints `null`. `*` over an attribute set prints the values in no particular order; for a stable order, use an `output` such as `go-template={{range $k, $v := .spec.labels}}{{$k}}={{$v}} {{end}}`, which goes through the keys sorted. A path cannot address a key that contains `.`, whitespace or one of `'`, `\`, `@`, `$`, `,`, `[`, `]`, `{` and `}`; the evaluation fails on such a key, and a go-template `output` reads it instead. |
| `<name>.group`{ #chalkoskindsgroup } | string matching `[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+` | required | API group of the kind: a DNS name its author controls, as for Kubernetes custom resources. Example: `"bgp.example.org"` |
| `<name>.multiple`{ #chalkoskindsmultiple } | boolean | `false` | Whether a node's config may hold more than one document of the kind. |
| `<name>.rules`{ #chalkoskindsrules } | list of submodule | `[ ]` | Change rules over the node's documents of the kind, CEL expressions that are true for every config the node accepts. `documents` and `oldDocuments` map document names to the documents of the kind in the delivered config and in the running one; `config`, `oldConfig`, `node.name` and `facts` are as for every rule. A rule that is false refuses the config, or says it takes a reset or a reinstall. Example: `[ { rule = "size(documents) <= 1"; message = "a node is in one rack"; } ]` |
| `<name>.rules.*.description`{ #chalkoskindsrulesdescription } | string or null | `null` | One sentence on what the rule guards, which the reference pages show; the node never reads it. Example: `"A Peer keeps its AS number."` |
| `<name>.rules.*.message`{ #chalkoskindsrulesmessage } | string or null | `null` | Why a change is refused and what to do instead. |
| `<name>.rules.*.messageExpression`{ #chalkoskindsrulesmessageexpression } | string or null | `null` | CEL expression that yields the message, with the rule's variables; `message` is used when it fails. Example: `"\"the rack moves from \" + oldSelf + \" to \" + self"` |
| `<name>.rules.*.rule`{ #chalkoskindsrulesrule } | non-empty string | required | CEL expression that is true for every change the node accepts. Example: `"self == oldSelf"` |
| `<name>.rules.*.takes`{ #chalkoskindsrulestakes } | one of `refuse`, `reset`, `reinstall` | `"refuse"` | What a change the rule refuses needs: `refuse` cannot be made, `reset` takes `chalkctl storage reset`, `reinstall` a reinstall of the node. |
| `<name>.spec`{ #chalkoskindsspec } | map of any | `{ }` | Option declarations of the document's `spec`, as under a module's `options`. They are converted into the kind's JSON Schema: an option without a default is required, and `description`, `default` and `example` carry over. A field declares change rules with `lib.chalkos.mkOption { …; rules = [ { rule = "self == oldSelf"; message = "…"; } ]; }`, in the shape of `rules`, where `self` is the field's new value and `oldSelf` its value in the running config. |
| `<name>.version`{ #chalkoskindsversion } | string matching `v[0-9]+((alpha|beta)[0-9]+)?` | required | API version of the kind. An incompatible change to the spec is a new version. Example: `"v1alpha1"` |

Example of `<name>.get`:

```nix
config.chalkos.kinds.Rack.get { name = "rack"; path = "spec.location"; }
# "/nix/store/…-chalkcfg-0.1.0/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'"
```

Example of `<name>.spec`:

```nix
{
  asn = lib.mkOption { type = lib.types.ints.u32; description = "Local AS number."; };
}
```

### `systemd.services.<name>.chalkos.kinds` { #systemdserviceschalkoskinds }

Type: list of string · Default: `[ ]` · Declared in: [modules/node/runtime/grants.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/grants.nix)

Config kinds the unit reads, by name: attributes of `chalkos.kinds`, or networkd's
`Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are in its
`dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`, and `chalkcfg` is
on the unit's `PATH`. The values their `secretRef` fields name are the unit's
credentials `secret_<secret>.<key>`. A grant never restarts the unit: an effect of
`chalkos.effects` does.

Example: `[ "Peer" ]`

## Facts { #facts }

The values a node reads at a delivery, which change rules and effects compare.

### `chalkos.facts` { #chalkosfacts }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/kinds/facts.nix](https://github.com/trevex/chalkos/blob/main/modules/node/kinds/facts.nix)

Facts about the node that change rules read as `facts.<name>`, each from a file or a
command. chalkd loads a fact when a rule first reads it and keeps it for the delivery: a
file is read once, and a command runs once for each config it reads; chalkctl
runs no rule that reads one.

The options within each, by their path below `chalkos.facts`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.command`{ #chalkosfactscommand } | string or null | `null` | Program of the image's store, with its arguments, that prints the fact as JSON. It reads `{"config": …, "oldConfig": …}` and its inputs as JSON on standard input, runs through `systemd-run` with the system read-only and no network, and must finish within 30 seconds with at most 1 MiB of output. Example: `"${pkgs.my-tool}/bin/my-tool facts"` |
| `<name>.fields`{ #chalkosfactsfields } | list of non-empty string | `[ ]` | Keys of the file's JSON object the fact keeps, so rules and their messages see nothing else of the file; empty keeps the whole content, unless `hashOthers` is set. Example: `[ "kind" ]` |
| `<name>.file`{ #chalkosfactsfile } | string or null | `null` | File whose JSON content is the fact: null while the file is missing. A file that cannot be read, or holds no JSON, refuses the delivery. Example: `"/state/kubernetes/share.json"` |
| `<name>.hashOthers`{ #chalkosfactshashothers } | boolean | `false` | Keep the other keys of the file's JSON object too, each as the hex SHA-256 of its value, so rules see whether a value changed but not what it holds, as of a file of private keys. |
| `<name>.inputs`{ #chalkosfactsinputs } | list of `fallbackSecret` | `[ ]` | Delivery inputs the command receives besides the config: `fallbackSecret`, the fallback secret delivered with it. |

## Outputs { #outputs }

The files a node renders from its documents at each delivery.

### `chalkos.outputs` { #chalkosoutputs }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/runtime/outputs.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/outputs.nix)

Config files of the node's services, by a name of the output's own, each rendered from
documents by a program of the image's store. chalkd renders and checks every output
before a delivery changes anything, and refuses the delivery when one fails; once the
delivery is stored it swaps the files in, and the effects of `chalkos.effects` that name
a changed output fire. `chalkos-config.service` renders them at boot, before services
start, and fails when one fails.

The options within each, by their path below `chalkos.outputs`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.check`{ #chalkosoutputscheck } | string or null | `null` | Shell commands, run by bash with `set -e` in the renderer's sandbox, with coreutils, grep and sed on the `PATH`, that check the rendered files in `$OUT`; they read the renderer's input on standard input and may not write `$OUT`. Unlike the renderer they may open netlink sockets, so `unshare --map-root-user --net nft --check --file "$OUT/…"` checks an nftables ruleset in a user and network namespace of its own. The files are as the renderer wrote them, before they get the output's owner and mode. A failure refuses the delivery before it changes anything, and at boot fails `chalkos-config.service`. Example: `"${pkgs.bird}/bin/bird -p -c $OUT/bird.conf"` |
| `<name>.dir`{ #chalkosoutputsdir } | string | `"/run/chalkos/outputs/<name>"` | Where services read the output's files: a link that chalkd replaces in one rename, so a service reads the files of one render, never a mix of two. Refer to it as `config.chalkos.outputs.<name>.dir`, so a misspelled name fails the evaluation. A directory at the path is removed when the link first replaces it, so the path is a clean path below `/run` whose components start with no dot, outside `/run/chalkos` but for `/run/chalkos/outputs/<name>` and the paths below it, and neither another output's dir nor inside one. |
| `<name>.examples`{ #chalkosoutputsexamples } | path or null | `null` | Directory of config files, YAML or JSON, each one example of a node's documents. The image's build renders and checks the output from each, as the node `example`, and fails when one fails. Example: `./examples` |
| `<name>.group`{ #chalkosoutputsgroup } | string | `"root"` | Group of the output's files and directories. |
| `<name>.inputs`{ #chalkosoutputsinputs } | list of string | `[ ]` | Config kinds, by name as for a grant, whose documents the renderer reads. Example: `[ "Peer" ]` |
| `<name>.mode`{ #chalkosoutputsmode } | string matching `0?[0-7]{3}` | `"0644"` | Mode of the output's files; its directories are 0755. |
| `<name>.render`{ #chalkosoutputsrender } | command, package or lib.chalkos.render value | required | Program of the image's store, with its arguments, that renders the output. It reads `{"node": {"name": …}, "documents": {"<Kind>": [<document>, …]}}` on standard input, the documents of each kind of `inputs` sorted by name, with their schema defaults and without any Secret's values, and writes the output's files into `$OUT`, the only path it may write. It runs as the unprivileged user `chalkos-render`, without capabilities, devices or network, on a read-only system without STATE, `/var`, or the config, credentials and Kubernetes keys chalkd writes below `/run/chalkos`, and must finish within 30 seconds; the files may take 16 MiB, and hold no links. The same documents must give the same files: chalkd renders every output again at boot. `lib.chalkos.render.template`, `.jq` and `.documents` build the common renderers; `lib.chalkos.render.template` renders a Go template, in which a key a document lacks, or that holds null, prints as nothing. Example: `lib.chalkos.render.template ./bird.conf.tmpl` |
| `<name>.user`{ #chalkosoutputsuser } | string | `"root"` | Owner of the output's files and directories. |

## Effects { #effects }

What a delivery does to units when outputs, documents or facts change.

### `chalkos.effects` { #chalkoseffects }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/runtime/effects.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/effects.nix)

What a delivery does to the node's services, by a name of the effect's own. Once a
delivery is applied, chalkd takes every effect one of whose `outputs` changed, the
documents of one of whose `kinds` did, or whose `when` holds: it reloads the units
of their `reload`, runs their `run`, restarts the units of their `restart`, and runs the
layer actions their `chalkd` names. An effect exists only when it is declared.

The options within each, by their path below `chalkos.effects`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.chalkd`{ #chalkoseffectschalkd } | string matching `[a-z][a-z0-9-]*:[a-z][a-z0-9-]*` or null | `null` | An action of a layer on what only the running chalkd holds, as `<layer>:<action>`, such as `kubernetes:restart`, which starts chalkd's Kubernetes loops again, or `kubernetes:reload`, which starts the control plane's loop again. chalkd runs the actions of the due effects once each, after the restarts. The layer must be one of the image's. Example: `"kubernetes:reload"` |
| `<name>.hint`{ #chalkoseffectshint } | submodule or null | `null` | Why the effect's units may fail to restart, for the delivery's error: when a unit of `restart` fails, chalkd fails the delivery as a failed precondition that names `text` and the content of `file`, rather than systemctl's error alone. |
| `<name>.hint.file`{ #chalkoseffectshintfile } | path or null | `null` | A file whose text follows `text`; the hint is shown only while the file holds text. |
| `<name>.hint.text`{ #chalkoseffectshinttext } | non-empty string | required | What the delivery's error says after the units' failure. |
| `<name>.kinds`{ #chalkoseffectskinds } | list of string | `[ ]` | Config kinds, by name as for a grant. The effect fires when the documents of one of them changed: one was added, removed or changed, or a value of a Secret one of them references changed. A service that reads its kinds' documents when it starts needs no output in between. Example: `[ "Peer" ]` |
| `<name>.outputs`{ #chalkoseffectsoutputs } | list of string | `[ ]` | Names of outputs of `chalkos.outputs`. The effect fires when one of them changed: when the hash of its files differs from the one before the delivery, or the values of the Secrets its input documents reference do, so a rotated password fires the effect even though the rendered files stay the same. Example: `[ "bird-conf" ]` |
| `<name>.reload`{ #chalkoseffectsreload } | list of string | `[ ]` | Units the effect reloads with `systemctl reload`, before chalkd restarts any. chalkd reloads only a unit that is active, and not one it restarts. A unit that failed it restarts with `reset-failed` and `restart`, as a failed unit of `restart`, since systemd reloads no failed unit. Example: `[ "systemd-networkd.service" ]` |
| `<name>.restart`{ #chalkoseffectsrestart } | list of string | `[ ]` | Units the effect restarts, by their full names. chalkd restarts the units of every due effect in one `systemctl try-restart`, those of an effect with `start` in one `systemctl restart`, and a unit that failed with `reset-failed` and `restart`. Example: `[ "bird.service" ]` |
| `<name>.run`{ #chalkoseffectsrun } | non-empty string or null | `null` | Command, with its arguments, the effect runs after the reloads and before the restarts. chalkd splits it on spaces, with no shell quoting, and runs the first word: a bare program name of chalkd's PATH, or an absolute path in the image's store. chalkd runs it whether or not any unit runs, and a command that fails fails the delivery, so one that talks to a service that may be stopped checks first that the service runs. Example: `"chronyc reload sources"` |
| `<name>.start`{ #chalkoseffectsstart } | boolean | `false` | Start the units of `restart` that do not run, with `systemctl restart` instead of `try-restart`, as for a unit whose condition only the delivery meets. |
| `<name>.when`{ #chalkoseffectswhen } | non-empty string or null | `null` | CEL expression that is true when a delivery takes the effect, for a trigger that no output renders; the effect fires when `when` holds, one of `outputs` changed or the documents of one of `kinds` did. It reads `config` and `oldConfig`, the node's config after and before the delivery, each by apiVersion, kind and name with every Secret value replaced by its SHA-256, the node's name as `node.name`, and `facts.<name>` after the delivery and `oldFacts.<name>` before it. Example: `"facts.share != oldFacts.share"` |

## Diagnostics { #diagnostics }

The read-only commands a client of the node API runs on a node with chalkctl node diag.

### `chalkos.diagnostics` { #chalkosdiagnostics }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/diagnostics.nix](https://github.com/trevex/chalkos/blob/main/modules/node/diagnostics.nix)

Read-only commands of the image that a client of the node API runs with
`chalkctl node diag`, by names of the form `[a-z][a-z0-9-]*`. chalkd checks the caller's
role and each argument against its parameter's type, then runs the command as a transient
systemd unit, in the host's network or in the network namespace that `networkNamespace`
names. The unit runs under a dynamic user, without new privileges, with only the
`capabilities` and `groups` given. It sees a read-only file system with a private `/tmp`,
without STATE, `/var/lib`, or the config and credentials chalkd writes, and runs for at
most `timeout`. chalkd sends at most 1 MiB of what the command prints. Every image declares
the base diagnostics, whose names are taken.

The options within each, by their path below `chalkos.diagnostics`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.capabilities`{ #chalkosdiagnosticscapabilities } | list of string matching `CAP_[A-Z_]+` | `[ ]` | Capabilities of the run's process, as its bounding and ambient sets; it has no other. Example: `[ "CAP_NET_RAW" ]` |
| `<name>.command`{ #chalkosdiagnosticscommand } | non-empty (list of string) | required | The command chalkd runs, as a list of arguments that no shell reads. The first is a program of the image's store. `{<parameter>}` in an argument becomes the parameter's value within that argument alone, and an argument that names an optional parameter a call leaves out is dropped. Every placeholder names a parameter, and every parameter has a placeholder. Example: `[ "${pkgs.iproute2}/bin/ip" "-json" "route" "get" "{address}" ]` |
| `<name>.description`{ #chalkosdiagnosticsdescription } | non-empty string | required | One sentence on what the diagnostic shows, which `chalkctl node diag` lists. Example: `"The route the kernel takes to an address."` |
| `<name>.groups`{ #chalkosdiagnosticsgroups } | list of non-empty string | `[ ]` | Groups of the image the run's process belongs to, as for a daemon's control socket. Example: `[ "bird" ]` |
| `<name>.networkNamespace`{ #chalkosdiagnosticsnetworknamespace } | string matching `([A-Za-z0-9_]|[{]([a-z][a-z0-9-]*)[}])([A-Za-z0-9_.-]|[{]([a-z][a-z0-9-]*)[}])*` or null | `null` | Network namespace the run joins, by its name below `/run/netns`, which may name parameters as `command` does; null runs it in the host's network. The name holds letters, digits, `_`, `.` and `-`, starts with neither `.` nor `-`, and fills to 255 characters at most; the build counts each parameter as one character, and chalkd refuses a call whose values fill it to anything else, and fails one whose namespace does not exist before the command runs. A parameter it names is not `optional`. Example: `"blue-{name}"` |
| `<name>.parameters`{ #chalkosdiagnosticsparameters } | map of submodule | `{ }` | The values a call passes, by names of the form `[a-z][a-z0-9-]*`. |
| `<name>.parameters.<name>.default`{ #chalkosdiagnosticsparametersdefault } | non-empty string or null | `null` | Value the parameter takes when a call leaves it out, of the parameter's type; null for none. A parameter with a default is not `optional` as well. Example: `"main"` |
| `<name>.parameters.<name>.description`{ #chalkosdiagnosticsparametersdescription } | non-empty string | required | One sentence on the value, which `chalkctl node diag` lists. Example: `"IPv4 or IPv6 address."` |
| `<name>.parameters.<name>.max`{ #chalkosdiagnosticsparametersmax } | integer or null | `null` | Greatest value of an `int` parameter; null for none. |
| `<name>.parameters.<name>.min`{ #chalkosdiagnosticsparametersmin } | integer or null | `null` | Least value of an `int` parameter; null for none. |
| `<name>.parameters.<name>.optional`{ #chalkosdiagnosticsparametersoptional } | boolean | `false` | Whether a call may leave the parameter out, which drops every argument of `command` that names it. |
| `<name>.parameters.<name>.regex`{ #chalkosdiagnosticsparametersregex } | non-empty string or null | `null` | Regular expression in Go's RE2 syntax, from `^` to `$`, that the value of a `pattern` parameter matches whole. Example: `"^[a-z0-9]+$"` |
| `<name>.parameters.<name>.type`{ #chalkosdiagnosticsparameterstype } | one of `address`, `prefix`, `hostname`, `interface`, `int`, `enum`, `pattern` | required | What the value is. chalkd refuses a value of another type, and passes the command its canonical form: `address` is an IPv4 or IPv6 address, with a zone for a link-local one; `prefix` an address with a prefix length; `hostname` a DNS name, which the command gets fully qualified, with a trailing dot; `interface` a network interface's name; `int` a decimal integer from `min` to `max`; `enum` one of `values`; `pattern` a string that `regex` matches whole. chalkd refuses any value that starts with `-` or holds a control character. Example: `"address"` |
| `<name>.parameters.<name>.values`{ #chalkosdiagnosticsparametersvalues } | list of non-empty string | `[ ]` | Values of an `enum` parameter. Example: `[ "inet" "inet6" ]` |
| `<name>.role`{ #chalkosdiagnosticsrole } | one of `reader`, `operator`, `admin` | `"operator"` | Least client role that runs the diagnostic, which chalkd checks on top of the reader role that RunDiagnostic needs. |
| `<name>.timeout`{ #chalkosdiagnosticstimeout } | string matching `[1-9][0-9]*[sm]` | `"30s"` | How long a run may take, in seconds or minutes, from `1s` to `10m`; systemd stops the run then. Example: `"10s"` |

## lib.chalkos { #lib-chalkos }

Every image's NixOS modules get `lib.chalkos` as part of `lib`. It holds the type and the
`mkOption` of an extension kind's fields, the renderers of `chalkos.outputs.<name>.render`
and the common effects.

| Helper | Arguments | Result |
| --- | --- | --- |
| `lib.chalkos.mkOption` | `lib.mkOption`'s, and `rules`, a list of `{ rule; message or messageExpression; takes ? "refuse"; description ? null; }` | An option for a field of a kind, with the field's change rules. |
| `lib.chalkos.types.secretRef` | — | Type of a field that holds a reference to a key of a Secret: `secretRef` with `name` and `key`. |
| `lib.chalkos.render.template` | a path ending in `.tmpl` | Renderer of a Go `text/template` into one file, named as the template less `.tmpl`; a missing or null key prints as nothing. |
| `lib.chalkos.render.jq` | a path ending in `.jq` | Renderer of a jq program into one file, named as the program less `.jq`; a string is written raw. |
| `lib.chalkos.render.documents` | a list of kind names | Renderer of the documents of those kinds as they are, each as `<Kind>/<name>.json`. |
| `lib.chalkos.restartOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that restarts the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
| `lib.chalkos.reloadOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that reloads the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |

Sources: the option declarations of the node modules in `modules/node/`, through
nixosOptionsDoc, and `lib/chalkos.nix`.
