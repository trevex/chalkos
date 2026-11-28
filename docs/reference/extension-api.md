---
title: "Extension API"
description: "The options and chalkosLib helpers that an extension declares kinds, facts, outputs, validators, effects and diagnostics with"
---

# Extension API

This page is for authors of extensions, the NixOS modules that declare config kinds of their
own, read a node's documents in a service, check them with programs, react when they change, and
give operators diagnostics. It lists the options the node modules give an extension and the
helpers of `chalkosLib`. The Build pages show them at work, from
[Declare a config kind](../build/declare-a-kind.md) to
[Add a diagnostic](../build/add-a-diagnostic.md). [Extension runtime](extension-runtime.md) lists
what an extension's rules, templates and programs read, and the limits they run within.

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
| `<name>.get`{ #chalkoskindsget } | function returning string, read-only | a function of `{ name ? null, path ? null, output ? null }` | Builds a command that prints the node's documents of the kind with chalkcfg: every document, one a line, or the one `name` names, such as `rack`. `path`, such as `spec.location`, prints one field and must follow the kind's options, or the evaluation fails and lists the fields where it went wrong. `output` passes `json`, `yaml`, `jsonpath=<expression>` or `go-template=<template>` instead of a path. A document without the field fails the command with status 1. A unit's `ExecStart=` takes the command as it is; systemd expands `$` and `%` there, so a command that holds either needs each written twice. [chalkcfg prints one field](../build/read-config.md#chalkcfg-prints-one-field) gives the path's syntax, its order over attribute sets and the keys it cannot address. |
| `<name>.group`{ #chalkoskindsgroup } | string matching `[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+` | required | API group of the kind: a DNS name its author controls, as for Kubernetes custom resources. Example: `"bgp.example.org"` |
| `<name>.multiple`{ #chalkoskindsmultiple } | boolean | `false` | Whether a node's config may hold more than one document of the kind. |
| `<name>.rules`{ #chalkoskindsrules } | list of submodule | `[ ]` | Change rules over the node's documents of the kind, CEL expressions that are true for every config the node accepts. `documents` and `oldDocuments` map document names to the documents of the kind in the delivered config and in the running one; `config`, `oldConfig`, `node` and `facts` are as for every rule. A rule that is false refuses the config, or says it takes a reset or a reinstall. Example: `[ { rule = "size(documents) <= 1"; message = "a node is in one rack"; } ]` |
| `<name>.rules.*.description`{ #chalkoskindsrulesdescription } | string or null | `null` | One sentence on what the rule guards, which the reference pages show and which names the rule when it cannot be evaluated or refuses without a message. Example: `"A Peer keeps its AS number."` |
| `<name>.rules.*.message`{ #chalkoskindsrulesmessage } | string or null | `null` | Why a change is refused and what to do instead. |
| `<name>.rules.*.messageExpression`{ #chalkoskindsrulesmessageexpression } | string or null | `null` | CEL expression that yields the message, with the rule's variables; `message` is used when it fails. Example: `"\"the rack moves from \" + oldSelf + \" to \" + self"` |
| `<name>.rules.*.reason`{ #chalkoskindsrulesreason } | `NO_FALLBACK_SECRET` or null | `null` | The node API's `ErrorReason`, without its `ERROR_REASON_` prefix, that a refusal by the rule carries to the client, for a refusal chalkd or chalkctl acts on. Built-in rules alone name one. |
| `<name>.rules.*.rule`{ #chalkoskindsrulesrule } | non-empty string | required | CEL expression that is true for every change the node accepts. Example: `"self == oldSelf"` |
| `<name>.rules.*.takes`{ #chalkoskindsrulestakes } | one of `refuse`, `reset`, `reinstall` | `"refuse"` | What a change the rule refuses needs: `refuse` cannot be made, `reset` takes `chalkctl storage reset`, `reinstall` a reinstall of the node. |
| `<name>.spec`{ #chalkoskindsspec } | map of any | `{ }` | Option declarations of the document's `spec`, as under a module's `options`. They are converted into the kind's JSON Schema: an option without a default is required, and `description`, `default` and `example` carry over. A field declares change rules with `chalkosLib.mkOption { …; rules = [ { rule = "self == oldSelf"; message = "…"; } ]; }`, in the shape of `rules`, where `self` is the field's new value and `oldSelf` its value in the running config. `chalkosLib.mkOption`'s `unique` declares values that must not repeat, among the node's documents of the kind or within one list, and its `maxItems` the most items a list holds. |
| `<name>.specRules`{ #chalkoskindsspecrules } | list of submodule | `[ ]` | Change rules of each document's spec, in the shape of `rules`. `self` is the spec of one delivered document and `oldSelf` the spec of the running config's document of the same name, as for a field's rules; `config`, `oldConfig`, `node` and `facts` are as for every rule. A rule here compares the fields of one document, a list with another list among them, at the cost of a loop less than the same rule over `documents`. Its message starts with the kind, the document's name and `spec`. |
| `<name>.specRules.*.description`{ #chalkoskindsspecrulesdescription } | string or null | `null` | One sentence on what the rule guards, which the reference pages show and which names the rule when it cannot be evaluated or refuses without a message. Example: `"A Peer keeps its AS number."` |
| `<name>.specRules.*.message`{ #chalkoskindsspecrulesmessage } | string or null | `null` | Why a change is refused and what to do instead. |
| `<name>.specRules.*.messageExpression`{ #chalkoskindsspecrulesmessageexpression } | string or null | `null` | CEL expression that yields the message, with the rule's variables; `message` is used when it fails. Example: `"\"the rack moves from \" + oldSelf + \" to \" + self"` |
| `<name>.specRules.*.reason`{ #chalkoskindsspecrulesreason } | `NO_FALLBACK_SECRET` or null | `null` | The node API's `ErrorReason`, without its `ERROR_REASON_` prefix, that a refusal by the rule carries to the client, for a refusal chalkd or chalkctl acts on. Built-in rules alone name one. |
| `<name>.specRules.*.rule`{ #chalkoskindsspecrulesrule } | non-empty string | required | CEL expression that is true for every change the node accepts. Example: `"self == oldSelf"` |
| `<name>.specRules.*.takes`{ #chalkoskindsspecrulestakes } | one of `refuse`, `reset`, `reinstall` | `"refuse"` | What a change the rule refuses needs: `refuse` cannot be made, `reset` takes `chalkctl storage reset`, `reinstall` a reinstall of the node. |
| `<name>.state`{ #chalkoskindsstate } | submodule or null | `null` | State of the kind's layer on a node, from which the fields that `chalkosLib.mkOption` marks `fixed = "node"` keep their value: each such field gets the rule <code>facts&#46;&#60;fact&#62; &#61;&#61; null &#124;&#124; self &#61;&#61; oldSelf</code>, whose message names the field. A field marked `fixed = "node"` in a kind without a state fails the evaluation. |
| `<name>.state.description`{ #chalkoskindsstatedescription } | string matching the pattern [^"\\
]+ | required | What holding the state means, as the generated rules' messages say it after "fixed once"; without quotes, backslashes or line breaks. Example: `"the node holds a Kubernetes share"` |
| `<name>.state.fact`{ #chalkoskindsstatefact } | string matching `[a-z][A-Za-z0-9]*` | required | Fact of `chalkos.facts` that is null until the node holds the state of the kind's layer, such as the cluster it joined. Example: `"share"` |
| `<name>.state.remedy`{ #chalkoskindsstateremedy } | string matching the pattern [^"\\
]+ | `"reinstall the node"` | What the user does instead of keeping the value, as the generated rules' messages say it after "so keep it, or"; without quotes, backslashes or line breaks. Example: `"reinstall the node into a new cluster"` |
| `<name>.unique`{ #chalkoskindsunique } | list of submodule | `[ ]` | Tuples of the spec's fields that no two of the node's documents of the kind share. chalkctl and chalkd refuse a config in which two documents hold the same values in all of `by`'s fields. A field of `by` is a field name or `{ field; as; }`, where `as` normalises the value before the comparison: `ip`, `cidr`, `host` or `lower`. `hint` ends the refusal with what to do instead. A single field's values, and those of list items, are declared unique with `chalkosLib.mkOption`'s `unique`. Example: `[ { by = [ "from" "to" ]; hint = "give each pair of zones one policy"; } ]` |
| `<name>.unique.*.by`{ #chalkoskindsuniqueby } | non-empty (list of (non-empty string or (submodule))) | required | Fields of the spec whose values together no two documents share. Example: `[ "from" "to" ]` |
| `<name>.unique.*.hint`{ #chalkoskindsuniquehint } | string or null | `null` | What to do instead, which ends the refusal. Example: `"give each pair of zones one policy"` |
| `<name>.version`{ #chalkoskindsversion } | string matching <code>v&#91;0&#45;9&#93;&#43;&#40;&#40;alpha&#124;beta&#41;&#91;0&#45;9&#93;&#43;&#41;&#63;</code> | required | API version of the kind. An incompatible change to the spec is a new version. Example: `"v1alpha1"` |

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

Example of `<name>.specRules`:

```nix
[
  {
    rule = "self.neighbours.all(n, n.address != document(\"Peer\", self.peer).spec.address)";
    message = "a neighbour has the peer's own address; list the peer's neighbours alone";
  }
]
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
| `<name>.command`{ #chalkosfactscommand } | list of a program and its arguments, command or package or null | `null` | Program of the image's store and its arguments, as a list or as a string split at white space, that prints the fact as JSON: the program itself or a script that calls it, not a link to a multi-call binary. [Facts](extension-runtime.md#facts) gives what it reads, and [Command fact sandbox](extension-runtime.md#command-fact-sandbox) its limits. Example: `[ "${pkgs.my-tool}/bin/my-tool" "facts" ]` |
| `<name>.fields`{ #chalkosfactsfields } | list of non-empty string | `[ ]` | Keys of the file's JSON object the fact keeps, so rules and their messages see nothing else of the file; empty keeps the whole content, unless `hashOthers` is set. Example: `[ "kind" ]` |
| `<name>.file`{ #chalkosfactsfile } | string or null | `null` | File whose JSON content is the fact: null while the file is missing. A file that cannot be read, or holds no JSON, refuses the delivery. Example: `"/state/kubernetes/share.json"` |
| `<name>.hashOthers`{ #chalkosfactshashothers } | boolean | `false` | Keep the other keys of the file's JSON object too, each as the hex SHA-256 of its value, so rules see whether a value changed but not what it holds, as of a file of private keys. |
| `<name>.inputs`{ #chalkosfactsinputs } | list of `fallbackSecret` | `[ ]` | Delivery inputs the command receives besides the config: `fallbackSecret`, the fallback secret delivered with it. |

## Validators { #validators }

The programs that check a node's documents at each delivery, before the outputs render.

### `chalkos.validators` { #chalkosvalidators }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/runtime/validators.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/validators.nix)

Programs that check a node's documents for what the kinds' schemas and change rules do not
see, such as what several documents mean together, by a name of the validator's own. chalkd
runs every validator before a delivery changes anything, after the change rules and before
the outputs render, and refuses the delivery when one refuses it or fails. The image checks
its own config with them too, as an upgrade does before it stages the image.
`chalkctl validate` runs only the validators chalkctl links, the router layer's `router`;
the others are programs of the image, which the node runs.

The options within each, by their path below `chalkos.validators`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.inputs`{ #chalkosvalidatorsinputs } | non-empty (list of string) | required | Config kinds, by name as for a grant, whose documents the validator reads. Example: `[ "Peer" ]` |
| `<name>.program`{ #chalkosvalidatorsprogram } | list of a program and its arguments, command, package or chalkosLib.validate value | required | Program of the image's store, with its arguments, that checks the documents of `inputs`; chalkd runs it as `<program> validate <name>`. It refuses a config with a report of refusals on standard output. [Program contract](extension-runtime.md#program-contract) gives its input, the report and its sandbox. `chalkosLib.validate.jq` builds a validator from a jq program, whose output is the report. Example: `chalkosLib.validate.jq ./peers.jq` |

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
| `<name>.check`{ #chalkosoutputscheck } | list of a program and its arguments, command, package or chalkosLib.check value or null | `null` | Program of the image's store, with its arguments, that chalkd runs after the renderer to check the rendered files in `$OUT`; null serves the files as the renderer wrote them. chalkd runs it without a shell, and an exit status other than 0 refuses the delivery before it changes anything, and at boot fails `chalkos-config.service`. `chalkosLib.check.run` and `.each` run a service's own check of its config on the files. [Check environment](extension-runtime.md#check-environment) gives what the program reads and may run. Example: `chalkosLib.check.run [ "${pkgs.bird}/bin/bird" "-p" "-c" "$OUT/bird.conf" ]` |
| `<name>.dir`{ #chalkosoutputsdir } | string | `"/run/chalkos/outputs/<name>"` | Where services read the output's files, a link to the last render. Refer to it as `config.chalkos.outputs.<name>.dir`, so a misspelled name fails the evaluation. [Output directories](extension-runtime.md#output-directories) gives the rules a dir follows. |
| `<name>.group`{ #chalkosoutputsgroup } | string | `"root"` | Group of the output's files and directories. |
| `<name>.inputs`{ #chalkosoutputsinputs } | list of string | `[ ]` | Config kinds, by name as for a grant, whose documents the renderer reads. Example: `[ "Peer" ]` |
| `<name>.mode`{ #chalkosoutputsmode } | string matching `0?[0-7]{3}` | `"0644"` | Mode of the output's files; its directories are 0755. |
| `<name>.render`{ #chalkosoutputsrender } | list of a program and its arguments, command, package or chalkosLib.render value | required | Program of the image's store, with its arguments, that renders the output from the documents of `inputs` into `$OUT`. `chalkosLib.render.template`, `.jq` and `.documents` build the common renderers. [Program contract](extension-runtime.md#program-contract) gives its input, its report of refusals and its sandbox. Example: `chalkosLib.render.template ./bird.conf.tmpl` |
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
| `<name>.after`{ #chalkoseffectsafter } | list of non-empty string | `[ ]` | Effects of this image that must succeed before this effect runs when they are due in the same delivery. A prerequisite that has no changes is left alone. A failed prerequisite blocks the effects that depend on it. Example: `[ "networkd" ]` |
| `<name>.chalkd`{ #chalkoseffectschalkd } | string matching `[a-z][a-z0-9-]*:[a-z][a-z0-9-]*` or null | `null` | An action of a layer on what only the running chalkd holds, as `<layer>:<action>`, such as `kubernetes:restart`, which starts chalkd's Kubernetes loops again, or `kubernetes:reload`, which starts the control plane's loop again. chalkd runs the actions of the due effects once each, after their batch's restarts. The layer must be one of the image's. Example: `"kubernetes:reload"` |
| `<name>.hint`{ #chalkoseffectshint } | submodule or null | `null` | Why the effect's units may fail to restart, for the delivery's error: when a unit of `restart` fails, chalkd fails the delivery as a failed precondition that names `text` and the content of `file`, rather than systemctl's error alone. |
| `<name>.hint.file`{ #chalkoseffectshintfile } | path or null | `null` | A file whose text follows `text`; the hint is shown only while the file holds text. |
| `<name>.hint.text`{ #chalkoseffectshinttext } | non-empty string | required | What the delivery's error says after the units' failure. |
| `<name>.kinds`{ #chalkoseffectskinds } | list of string | `[ ]` | Config kinds, by name as for a grant. The effect fires when the documents of one of them changed: one was added, removed or changed, or a value of a Secret one of them references changed. A service that reads its kinds' documents when it starts needs no output in between. Example: `[ "Peer" ]` |
| `<name>.outputs`{ #chalkoseffectsoutputs } | list of string | `[ ]` | Names of outputs of `chalkos.outputs`. The effect fires when one of them changed: when the hash of its files differs from the one before the delivery, or the values of the Secrets its input documents reference do, so a rotated password fires the effect even though the rendered files stay the same. Example: `[ "bird-conf" ]` |
| `<name>.reload`{ #chalkoseffectsreload } | list of string | `[ ]` | Units to `systemctl reload`, by their full names, before the restarts in the same batch. A failed unit is restarted instead, as systemd reloads no failed unit. Example: `[ "systemd-networkd.service" ]` |
| `<name>.restart`{ #chalkoseffectsrestart } | list of string | `[ ]` | Units the effect restarts, by their full names; a unit that does not run stays stopped unless the effect sets `start`. [The config pipeline](../how-it-works/node/config-pipeline.md#chalkd-takes-the-due-effects-in-one-order) gives the order of the actions of every due effect. Example: `[ "bird.service" ]` |
| `<name>.run`{ #chalkoseffectsrun } | list of a program and its arguments, command or package or null | `null` | Program and its arguments the effect runs after the reloads and before the restarts in its batch, as a list or as a string split at white space. chalkd runs it without a shell; the program is a bare program name of chalkd's PATH, or an absolute path in the image's store. chalkd runs it whether or not any unit runs, and a command that fails fails the delivery, so one that talks to a service that may be stopped checks first that the service runs. Example: `[ "chronyc" "reload" "sources" ]` |
| `<name>.start`{ #chalkoseffectsstart } | boolean | `false` | Start the units of `restart` that do not run, with `systemctl restart` instead of `try-restart`, as for a unit whose condition only the delivery meets. |
| `<name>.waitFor`{ #chalkoseffectswaitfor } | submodule or null | `null` | An optional readiness probe run before this effect, even when its prerequisites are unchanged. |
| `<name>.waitFor.argv`{ #chalkoseffectswaitforargv } | non-empty (list of non-empty string) | required | Executable from the image's store and its arguments. This read-only probe runs on the node after the due prerequisites succeed. Exit 0 when ready, 75 to retry, or another nonzero status to fail the effect. An attempt that runs longer than 2 seconds is stopped and counts as a retry. |
| `<name>.waitFor.description`{ #chalkoseffectswaitfordescription } | non-empty string | required | The runtime condition this effect needs, shown in plans and errors. |
| `<name>.waitFor.timeoutSeconds`{ #chalkoseffectswaitfortimeoutseconds } | integer between 1 and 60 (both inclusive) | `15` | Maximum time to wait for the condition, in seconds. |
| `<name>.when`{ #chalkoseffectswhen } | non-empty string or null | `null` | CEL expression that is true when a delivery takes the effect, for a trigger that no output renders; the effect fires when `when` holds, one of `outputs` changed or the documents of one of `kinds` did. [Effect conditions](extension-runtime.md#effect-condition-variables) gives the variables it reads. Example: `"facts.share != oldFacts.share"` |

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
without STATE, `/var/lib`, what chalkd and the layers keep below `/run/chalkos`, or the
services' credentials, and runs for at most `timeout`. chalkd sends at most 1 MiB of what
the command prints. Every image declares the base diagnostics, whose names are taken.

The options within each, by their path below `chalkos.diagnostics`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.capabilities`{ #chalkosdiagnosticscapabilities } | list of string matching `CAP_[A-Z_]+` | `[ ]` | Capabilities of the run's process, as its bounding and ambient sets; it has no other. Only a diagnostic of the admin role takes one that reaches past the sandbox: `CAP_DAC_OVERRIDE`, `CAP_DAC_READ_SEARCH`, `CAP_SYS_ADMIN`, `CAP_SYS_PTRACE`, `CAP_SYS_RAWIO`, `CAP_SYS_MODULE`, `CAP_SETUID`, `CAP_SETGID`, `CAP_SETPCAP`, `CAP_BPF` or `CAP_MKNOD`. Example: `[ "CAP_NET_RAW" ]` |
| `<name>.command`{ #chalkosdiagnosticscommand } | non-empty (list of string) | required | The command chalkd runs, as a list of arguments that no shell reads. The first is a program of the image's store. `{<parameter>}` in an argument becomes the parameter's value within that argument alone, and an argument that names an optional parameter a call leaves out is dropped. Every placeholder names a parameter, and every parameter has a placeholder. Example: `[ "${pkgs.iproute2}/bin/ip" "-json" "route" "get" "{address}" ]` |
| `<name>.description`{ #chalkosdiagnosticsdescription } | non-empty string | required | One sentence on what the diagnostic shows, which `chalkctl node diag` lists. Example: `"The route the kernel takes to an address."` |
| `<name>.groups`{ #chalkosdiagnosticsgroups } | list of non-empty string | `[ ]` | Groups of the image the run's process belongs to, as for a daemon's control socket. Example: `[ "bird" ]` |
| `<name>.networkNamespace`{ #chalkosdiagnosticsnetworknamespace } | string matching <code>&#40;&#91;A&#45;Za&#45;z0&#45;9&#95;&#93;&#124;&#91;&#123;&#93;&#40;&#91;a&#45;z&#93;&#91;a&#45;z0&#45;9&#45;&#93;&#42;&#41;&#91;&#125;&#93;&#41;&#40;&#91;A&#45;Za&#45;z0&#45;9&#95;&#46;&#45;&#93;&#124;&#91;&#123;&#93;&#40;&#91;a&#45;z&#93;&#91;a&#45;z0&#45;9&#45;&#93;&#42;&#41;&#91;&#125;&#93;&#41;&#42;</code> or null | `null` | Network namespace the run joins, by its name below `/run/netns`, which may name parameters as `command` does, but none `optional`; null runs it in the host's network. The name holds letters, digits, `_`, `.` and `-`, starts with neither `.` nor `-`, and is 255 characters at most, each parameter counting as one at build. chalkd refuses a call whose values break these rules, and fails one whose namespace does not exist before the command runs. Example: `"blue-{name}"` |
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

## Tests { #tests }

The configs an image's tests give it and what the image should make of them, which nix flake check runs.

### `chalkos.tests` { #chalkostests }

Type: map of submodule · Default: `{ }` · Declared in: [modules/node/runtime/tests.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/tests.nix)

Tests of the image, by a name of the test's own: a config a node could be given, and what
the image makes of it, the files its outputs render, the refusals it gives and, for a
change, the effects it takes. `chalkos-rules test` runs each config through the checks
chalkd runs for a delivery: the kinds' schemas, change rules, validators, every output's
renderer and check, and the effects' plan. `system.build.chalkosTestResults` holds what
each test got, `<test>/outputs/<output>/`, `<test>/refusals.json` and
`<test>/effects.json`, and builds whatever the tests got; `system.build.chalkosTests`
fails when a test does, with a diff of each output's golden files. An image's `tests` is its
`chalkosTests`, and a flake that exports `chalkos.lib.testsOf` of its images as its checks
has `nix flake check` run them.

Example:

```nix
{
  # A second Peer: bird.conf gets its session, and bird reloads.
  two-peers = {
    before = ./tests/one-peer.yaml;
    config = ./tests/two-peers.yaml;
    outputs.bird-conf = ./tests/two-peers/bird-conf;
    effects = [ "bird" ];
  };
}
```

The options within each, by their path below `chalkos.tests`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.before`{ #chalkostestsbefore } | path or null | `null` | YAML or JSON file of the node's documents before `config`, which makes the test one of a change from these documents to those of `config`: change rules compare the two, and the effects due are those of the change. It must be accepted, or the test fails. Null makes the test one of a first delivery. Example: `./tests/one-peer.yaml` |
| `<name>.config`{ #chalkostestsconfig } | path | required | YAML or JSON file of a node's documents, as a delivery gives them: a file of the flake, such as `./tests/two-peers.yaml`, or of the store. The test checks them as a set, as chalkd checks a delivery, without the documents of the node's identity, such as its SystemDisk, which a test need not hold. Example: `./tests/two-peers.yaml` |
| `<name>.effects`{ #chalkostestseffects } | list of string or null | `null` | Names of the effects of `chalkos.effects` the change from `before` to `config` takes, sorted by name. An empty list expects none. Null leaves the effects unchecked. A test that sets it needs `before`. Example: `[ "bird" ]` |
| `<name>.facts`{ #chalkostestsfacts } | map of JSON value | `{ }` | Facts that rules and effects' `when` conditions read as `facts.<name>`, by name, each any JSON value. A fact the test leaves out reads as null. Each must be a fact of `chalkos.facts`. Example: `{ greetingLock = { message = "hello"; }; }` |
| `<name>.node`{ #chalkostestsnode } | string | `"example"` | Name of the node, which rules read as `node.name` and renderers and validators as `.node.name`. Rules read the node's static addresses as `node.addresses`, from the test's `config`. |
| `<name>.oldFacts`{ #chalkostestsoldfacts } | map of JSON value | `{ }` | Facts as they were before the change, in the form of `facts`, which the check of `before` and effects' `when` conditions read. Example: `{ greetingLock = null; }` |
| `<name>.outputs`{ #chalkostestsoutputs } | map of path | `{ }` | Golden files of outputs of `chalkos.outputs`, by output: a directory that holds exactly the files the output renders from `config`, with the hash of each store path written as `eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee`. A missing, extra or different file fails the test. An output left out is rendered and checked, but its files are not compared. An output that renders no files has no golden directory in a flake of a git repository, as git keeps no empty directory, so a test leaves it out. Example: `{ bird-conf = ./tests/two-peers/bird-conf; }` |
| `<name>.refusals`{ #chalkostestsrefusals } | list of submodule | `[ ]` | The refusals the config gets, from the kinds' schemas, change rules, validators and outputs' checks, in the order chalkctl prints them, each once. They are always compared: the default, an empty list, expects the config to be accepted. |
| `<name>.refusals.*.kind`{ #chalkostestsrefusalskind } | string | `""` | Kind of the refused document; empty for a refusal of no one document. Example: `"Peer"` |
| `<name>.refusals.*.message`{ #chalkostestsrefusalsmessage } | string | required | The refusal's message, exactly as chalkctl prints it. Example: `"enp3s0 has no Network document"` |
| `<name>.refusals.*.name`{ #chalkostestsrefusalsname } | string | `""` | Name of the refused document; empty for a refusal of no one document. Example: `"router"` |
| `<name>.refusals.*.path`{ #chalkostestsrefusalspath } | string | `""` | Path of the refused field in the document; empty for the whole document. Example: `"spec.interfaces[2]"` |

Example of `<name>.refusals`:

```nix
[
  {
    kind = "Peer";
    message = "enp3s0 has no Network document; bird binds the session to an interface the node configures";
    name = "router";
    path = "spec.interfaces[2]";
  }
]
```

### `chalkos.testsOnBuild` { #chalkostestsonbuild }

Type: boolean · Default: `false` · Declared in: [modules/node/runtime/tests.nix](https://github.com/trevex/chalkos/blob/main/modules/node/runtime/tests.nix)

Whether the image directory's build runs the tests of `chalkos.tests` and fails when one
does. The tests change neither the image's content nor its root hash; without this,
`nix flake check` runs them through `chalkos.lib.testsOf`.

## Image directory { #image-directory }

The files a layer adds to its image's directory, which chalkctl reads with the schemas.

### `chalkos.imageDirectory.annotations` { #chalkosimagedirectoryannotations }

Type: map of string · Default: `{ }` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

Annotations of the image's OCI manifest by key, which `metadata.json` records and
`chalkbuild oci` writes into the manifest. The image sets
`org.opencontainers.image.revision` and `org.opencontainers.image.version` from mkImage's
[`revision`](mkimages.md#revision) and [`tag`](mkimages.md#tag) when it is given them, and
a layer adds its own, such as the Kubernetes layer's `dev.chalkos.kubernetes.version`.
Like the revision and tag, they change no root hash.

Example:

```nix
{
  "org.opencontainers.image.source" = "https://github.com/example/cluster";
}
```

### `chalkos.imageDirectory.facts` { #chalkosimagedirectoryfacts }

Type: map of path · Default: `{ }` · Declared in: [modules/node/base/image-directory.nix](https://github.com/trevex/chalkos/blob/main/modules/node/base/image-directory.nix)

The facts of the image's layers by layer name, each a JSON file the image directory holds as
`facts/<layer>.json`. chalkctl reads them with the schemas and checks config against both,
as a node does against those of its image. A layer without facts sets none.

## chalkosLib { #chalkoslib }

Every image's NixOS modules get `chalkosLib` as a module argument; add it to a module's
arguments, as in `{ chalkosLib, config, lib, ... }:`. Code inside the module, submodules
included, uses that argument. The flake's `lib` holds the same functions, beside `mkImages`, for
code outside an image.

`chalkosLib` holds the type and the `mkOption` of an extension kind's fields, the renderers of
`chalkos.outputs.<name>.render`, the checks of `chalkos.outputs.<name>.check`, the validators of
`chalkos.validators.<name>.program`, the common effects and the sandboxes of units.
`chalkosLib.internal` holds chalkos's own helpers; it is not part of the extension API and changes
without notice.

| Helper | Arguments | Result |
| --- | --- | --- |
| `chalkosLib.mkOption`{ #chalkoslibmkoption } | `lib.mkOption`'s; `rules`, a list of `{ rule; message or messageExpression; takes ? "refuse"; description ? null; }`; `unique`, `true` or one or a list of `{ across ? "kind" or "list"; by ? [ field or { field; as; } ]; as ? null; hint ? null; }`; `maxItems`, a positive integer; and `fixed`, `"node"`, `"cluster"` or both in a list | An option for a field of a kind, with the field's change rules, the values that must not repeat, the most items a list holds and whether the field is fixed per node or held alike across the cluster. The build fails on a `unique` that does not fit the field's type, on a `maxItems` on a field that is no list, on `fixed = "node"` in a kind without `state`, and on `fixed` inside a list's items or a map's values. |
| `chalkosLib.types.ref`{ #chalkoslibtypesref } | a kind's name, or `{ kind; reserved ? [ ]; }` | Type of a field that holds the name of a document of the kind. The build fails when the image lacks the kind. chalkctl and chalkd refuse a config whose field names a document the config lacks. Names in `reserved` stand for something that is no document: the field takes them as they are, and no document of the kind may take them as its name. |
| `chalkosLib.types.secretRef`{ #chalkoslibtypessecretref } | — | Type of a field that holds a reference to a key of a Secret: `secretRef` with `name` and `key`. |
| `chalkosLib.types.address`{ #chalkoslibtypesaddress } | — | Type of a field that holds an IPv4 or IPv6 address. Its schema has `format: ip`: chalkctl and chalkd refuse an IPv4 address with leading zeros or a number above 255, and an IPv6 address with a zone. Rules read the value with `ip()`. |
| `chalkosLib.types.prefix`{ #chalkoslibtypesprefix } | — | Type of a field that holds an address range: an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6. Its schema has `format: cidr`, which chalkctl and chalkd check. Rules read the value with `cidr()`. |
| `chalkosLib.types.portRange`{ #chalkoslibtypesportrange } | — | Type of a field that holds a port from 1 to 65535, as a number, or a range of ports, as the string `"<first>-<last>"`. Its schema has `format: port-range` for the string: chalkctl and chalkd refuse a range whose first port is higher than its last. |
| `chalkosLib.types.imageReference`{ #chalkoslibtypesimagereference } | — | Type of a field that holds a container image reference: a registry, a repository and a tag, a digest or both, as `registry.example/app:v1.2.3@sha256:<digest>`. Its schema has `format: image-reference`, which chalkctl and chalkd check. |
| `chalkosLib.render.template`{ #chalkoslibrendertemplate } | a path ending in `.tmpl` | Renderer of a Go `text/template` into one file, named as the template less `.tmpl`; a missing or null key prints as nothing. |
| `chalkosLib.render.jq`{ #chalkoslibrenderjq } | a path ending in `.jq` | Renderer of a jq program into one file, named as the program less `.jq`; a string is written raw. |
| `chalkosLib.render.documents`{ #chalkoslibrenderdocuments } | a list of kind names | Renderer of the documents of those kinds as they are, each as `<Kind>/<name>.json`. |
| `chalkosLib.check.run`{ #chalkoslibcheckrun } | a list of a program and its arguments | Check that runs the program once, with `$OUT` in the arguments replaced by the directory of the rendered files. It fails when the program exits with a status other than 0. |
| `chalkosLib.check.each`{ #chalkoslibcheckeach } | a glob below `$OUT`, then a list of a program and its arguments | Check that runs the program once for each file the glob matches, in the order of their names, with `$FILE` in the arguments replaced by the file's path and `$OUT` by the directory. It passes when no file matches, and fails at the first file the program refuses. |
| `chalkosLib.validate.jq`{ #chalkoslibvalidatejq } | a path to a jq program | Validator that runs the jq program on its input, the documents by kind as a renderer reads them; what the program outputs is the report of refusals. |
| `chalkosLib.restartOn`{ #chalkoslibrestarton } | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that restarts the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
| `chalkosLib.reloadOn`{ #chalkoslibreloadon } | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that reloads the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
| `chalkosLib.sandbox.noExec`{ #chalkoslibsandboxnoexec } | — | A unit's `serviceConfig` in which programs run from the store alone: `NoExecPaths=/` and `ExecPaths=/nix/store`. Merge it into the unit's own with `//`. |
| `chalkosLib.sandbox.hardened`{ #chalkoslibsandboxhardened } | — | A service's `serviceConfig` sandbox that starts from nothing: `noExec`, an empty capability bounding set, Unix sockets alone, no new privileges, a read-only system (`ProtectSystem=strict`), no devices, no home, a private `/tmp`, kernel settings, modules, logs, cgroups, clock and host name out of reach, no namespaces and the system calls of `@system-service`. Widen `CapabilityBoundingSet` and `RestrictAddressFamilies` to what the service needs, and name the paths it writes in `ReadWritePaths`. [Write units without a shell](../build/shell-free.md#write-units-without-a-shell) shows it on a unit. |

Sources: the option declarations of the node modules in `modules/node/`, through
nixosOptionsDoc, and `lib/chalkos.nix`.
