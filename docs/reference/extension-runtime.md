---
title: "Extension runtime"
description: "What an extension's rules, templates and programs read, the functions they call, the limits they run within and the errors they show"
---

# Extension runtime

This page is for image authors who write an extension's change rules, effect conditions,
templates, renderers, checks and validators. It lists what each one reads, the functions it can
call, the limits it runs within and the errors it shows. [Extension API](extension-api.md) lists
the options that declare them, and the Build pages, from
[Declare a config kind](../build/declare-a-kind.md) on, show them at work.

## Rule attributes { #rule-attributes }

A change rule, in the `rules` of `chalkosLib.mkOption` or in a kind's `rules` or `specRules`, has
five attributes:

| Attribute | Meaning |
| --- | --- |
| `rule` | CEL that is true for every config the node accepts. |
| `message` | What the node says when the rule is false: why, and what to do instead. |
| `messageExpression` | CEL that yields the message. When it fails, the node says `message`, or names the rule's kind or field without one. |
| `takes` | What the refused change needs instead: `refuse`, the default, `reset` or `reinstall`. |
| `description` | One sentence on what the rule guards, which the reference pages show. The node never reads it. |

A rule without either message says `the config does not meet a rule of` and the kind or field
that declares it, such as `the config does not meet a rule of Peer spec.asn`.

## CEL variables { #cel-variables }

Change rules and effect conditions are CEL expressions. Each kind of expression reads its own set
of variables.

### Change rules { #change-rule-variables }

Every rule compares the delivered config with the config of the slot the node boots now. That
config is the baseline also when the delivered config is for another image, as in an upgrade.

| Variable | Rules | Value |
| --- | --- | --- |
| `self` | a field's or a spec's | The field's value, or the spec, in the delivered document, with the schema's defaults filled in. |
| `oldSelf` | a field's or a spec's | The field's value, or the spec, in the running config's document of the same kind and name; for a kind a node holds once, its document whatever its name. |
| `documents` | a kind's | The delivered documents of the kind, by name. |
| `oldDocuments` | a kind's | The running config's documents of the kind, by name. |
| `config` | every | The whole delivered config by apiVersion, kind and name: `config["bgp.example.org/v1alpha1"].Peer["router"]`. |
| `oldConfig` | every | The running config, in the same shape. |
| `node.name` | every | The node's name in the inventory, which its node certificate carries. |
| `node.addresses` | every | The node's static addresses in the delivered config: the `Address=` values of its Network documents that are IP addresses, without prefix length, in the order of the networks' names, as its node certificate names them. A list of strings; compare them with `ip()`, which takes every spelling of an address alike. |
| `facts.<name>` | every | The fact of that name. |

Every value of a `Secret` is its SHA-256 in these variables, so a rule can tell that a secret
changed without reading it. Each `data` and `stringData` value of a Secret object that a document
embeds, an object of `apiVersion: v1` and `kind: Secret` such as a Kubernetes Secret in an
`Addon`, is its SHA-256 as well. Maps iterate in the order of their keys, so a list built from the
names of `documents` is sorted.

A rule binds `self` to a path through the properties of objects. It cannot sit on the items of a
list or the values of a map. A rule on a list or a map compares it
whole. Lists are not matched item by item by a key.

The build types `self`, `oldSelf` and the documents of `documents` and `oldDocuments` from the
kind's schema:

- A submodule is an object of the fields it declares. A rule that reads a field the schema does
  not declare, such as `self.modle`, fails the build with `undefined field 'modle'`.
- An `attrsOf` is a map, whose keys a rule reads as they come; a `listOf` is a list of its items'
  type.
- A string, an enum of strings, a bool and an int are themselves, so `self.port == "22"` on an
  int fails the build with `found no matching overload`. `<`, `<=`, `>` and `>=` compare an int
  with a double, as in `self.count > 1.5`, but `==` and `!=` between an int and a double fail
  the build.
- A `nullOr` submodule is the object or `null`. Every other value is `dyn`, which the build does
  not check: a float, a `nullOr` of anything but a submodule, a port range and an embedded
  Kubernetes object.

`config`, `oldConfig`, `document()` and facts are `dyn`.

A submodule is no map. The build refuses a rule that loops over one, takes its `size()`, tests a
key with `in` or compares it with a map literal, as `self.sub.all(k, ...)`, `size(self.sub)`,
`"a" in self.sub` and `self.sub != {}` do. Test each field with `has()` instead, or read the
submodule as a map with `dyn(self.sub)`. Only a `nullOr` field compares with `null`:
`self.model != null` on a string that is not `nullOr` fails the build.

A rule reads a field whose name CEL reserves, such as `in`, by index: `self["in"]`. The build
checks a name given as a string literal as it checks a select. An index by another string, such
as `self.podCIDRs[family]`, yields `dyn`.

A field the schema declares can still be absent from a document, such as one that the running
config's document lacks because its image did not declare the field yet. Reading it fails the
rule at run time, so test such a field with `has()` before reading it.

### Effect conditions { #effect-condition-variables }

An effect's `when` reads these variables:

| Variable | Value |
| --- | --- |
| `config` | The config the node runs once the delivery is applied, by apiVersion, kind and name: `config["bgp.example.org/v1alpha1"].Peer["router"]`. |
| `oldConfig` | The config before the delivery, in the same shape. |
| `node.name` | The node's name, which its node certificate carries. |
| `node.addresses` | The node's static addresses in the config it runs once the delivery is applied, as rules read them. |
| `facts.<name>` | A fact once the delivery is applied. |
| `oldFacts.<name>` | The same fact before the delivery changed anything. |

Every value of a `Secret` is its SHA-256 in `config` and `oldConfig`. An expression can tell that
a secret changed, but not read it. `documents`, `self` and `oldSelf` exist in change rules alone.

chalkd loads the facts an expression reads before a delivery changes anything, and again once it
is applied, so `facts.<name> != oldFacts.<name>` fires on a file that the delivery writes. A fact
the node cannot find reads as null.

The image build compiles every `when`, and fails on one that does not compile or reads a fact no
module declares. On the node, a `when` that cannot be evaluated counts as true. Its units then
restart or reload once more than they needed to, so none runs on config it has not read.
chalkctl prints the reason as a warning.

### Facts { #facts }

A fact is read by its name alone: `facts["rackSurvey"]`, `has(facts.rackSurvey)` or `facts` by
itself fails the build, in a change rule and in an effect's `when` alike.

A command fact reads `{"config": ..., "oldConfig": ...}` on standard input, in the shape that
rules see, and prints the fact as JSON. A command with `inputs = [ "fallbackSecret" ];` also reads
the fallback secret delivered with the config. No other command reads it.

chalkd loads a fact when a rule first reads it, and keeps it for the rest of the delivery. A file
is read once. A command runs once for each config it reads: a delivery while an image is staged
runs the command once for the running image's check and once for the staged image's. A rule that
does not reach a fact, such as one whose first operand of `||` holds, loads none.

## CEL functions { #cel-functions }

Rules have CEL's standard functions, the lists, sets, strings and bindings (`cel.bind`)
extensions of cel-go, and chalkos's document lookup and its address, size, port and DNS
functions:

| Function | Result |
| --- | --- |
| `document(kind, name)` | The document of the kind by that name, as `config` holds it, or `null` when the config holds none or `name` is `null`. `kind` is a string literal naming a kind the image declares, without its group, as in `document("Peer", self)`; another kind fails the build. |
| `cidr(s)` | The address range `s`, with its address masked, so `cidr("10.244.1.0/16") == cidr("10.244.0.0/16")`. |
| `<cidr>.ip(n)` | The address `n` places after the range's first, which must lie in the range: `cidr("10.96.0.0/12").ip(10)` is `10.96.0.10`. |
| `<cidr>.last()` | The range's last address, its IPv4 broadcast address: `cidr("192.0.2.0/24").last()` is `192.0.2.255`. `<cidr>.ip(0)` is its first. |
| `<cidr>.prefix()` | The prefix length, an int. |
| `<cidr>.contains(<ip>)` | Whether the range holds the address. |
| `ip(s)` | The address `s`, equal to every spelling of it. |
| `<ip>.family()` | `ipv4` or `ipv6`. |
| `<ip>.compare(<ip>)` | `-1`, `0` or `1` as the address comes before, equals or comes after the other, in numeric order: `ip("10.0.0.2").compare(ip("10.0.0.10"))` is `-1`. IPv4 addresses come before IPv6 ones. |
| `string(<cidr>)`, `string(<ip>)` | The canonical spelling: `fd00::/108`, `fd00::a`. |
| `bytes(s)` | A size such as `1G` or `512M` in bytes, an int, with suffixes in powers of 1024. It replaces CEL's conversion of a string to bytes. |
| `isSize(s)` | Whether `bytes(s)` reads `s`: digits with an optional K, M, G, T or P suffix, below 8 EiB. |
| `portRange(p)` | The ports of `p`, a port as an int or a range `"<first>-<last>"`, as a map of `first` and `last`: `portRange("8000-8100").last` is `8100`, `portRange(22).first` is `22`. A port as a string works too: `portRange("22")` is the range of port 22 alone. Both lie from 1 to 65535, and `first` is no higher than `last`. |
| `isDNSName(s)` | Whether `s` is a DNS name in lower case without the root's dot, of 253 characters at most: labels of letters, digits, `-` and `_`, each of 63 characters at most, that start with a letter, a digit or `_` and end with a letter or a digit, or are `_` alone. |
| `isRecordData(type, s)` | Whether `s` is data a DNS record of the type holds, as the router's resolver takes it: an IPv4 address for `A` and an IPv6 address for `AAAA`, both without a zone, and for `AAAA` not an IPv4-mapped address such as `::ffff:192.0.2.1`; a name `isDNSName` takes for `CNAME` and `PTR`; a preference and a name for `MX`, and a priority, a weight, a port and a name for `SRV`, a single space apart, each number from 0 to 65535 without leading zeros; and 1 to 255 printable characters without quotes or backslashes for `TXT`. |

`document()` reads one document at the cost of one step, so a rule that loops over a list of
names and reads each named document costs as one loop. `isDNSName` and `isRecordData` cost a step
and a tenth of a step for each character of the string they check, as CEL's own string
functions do. chalkos's other functions cost one step each.

chalkctl and chalkd check every `chalkosLib.types.ref` field before any rule runs, so
`document()` of such a field yields `null` only for a `null` field or a reserved name. Rules of
effects have no `document()`.

## Cost limits and build checks { #cost-limits }

The image build runs `chalkos-rules check` over the image's schemas, facts, effects and outputs.
It fails on a rule that does not parse or type-check, which includes a rule that reads a field the
kind's schema does not declare, as [Change rules](#change-rule-variables) describes. It fails on a
rule that reads a fact no module declares, and on a command fact that is no program of the store.
It also limits a rule's cost:

- The build refuses a rule whose estimated cost exceeds 10,000,000, estimated with 256 elements
  in every list, map and string. A loop over a kind's documents inside another loop over them
  fits; a third nested loop does not.
- A node stops a rule at a cost of 1,000,000 on the config it meets.

A rule of the running image that does not compile on the node fails the delivery as an internal
error.

## Rule errors { #rule-errors }

A refusal does not leak a secret or a fact's content that the rule only compared:

- A rule that cannot be evaluated refuses the delivery with the kind of error alone, such as
  `cannot check: Peer/router: spec.asn: a rule cannot be evaluated: no such key`. The kind is
  one of `no such key`, `index out of bounds`, `no such overload`, `unknown time zone`,
  `invalid input to a function`, `cost limit exceeded`, `overflow`, `division by zero` and
  `conversion error`. An error of chalkos's functions names the function, such as
  `ip: the argument is not an address`. Of any other error the refusal says only
  `a rule cannot be evaluated`.
- A rule that reads a field the schema declares and the document lacks names the field, a name
  from the schema rather than a value:
  `cannot check: Peer/router: spec: a rule cannot be evaluated: no such key: asn, a field the config leaves out; test it with has() first`.
  A rule that looks a key up by a value, as `self.labels[self.name]` does, names no missing key,
  since the key can be a value. A loop's lookup of `documents[n]` or `oldDocuments[n]` counts as
  none.
- A `messageExpression` that fails gives the same kind of error after the message.
- A rule that yields no bool says which type it yields.
- A command fact that fails shows its exit status alone: `cannot check: fact disks: the command
  failed (exit 1); see the node's journal`. Its standard error goes to the node's journal, cut to
  4 KiB. An output that is no JSON, and a file that is not JSON, show nothing of their content.

## Command fact sandbox { #command-fact-sandbox }

chalkd runs a command fact through `systemd-run --pipe --wait` as a transient unit, as root
without `CAP_SYS_MODULE`:

- The file system is read-only, STATE among it, apart from `/dev`, the unit's own empty `/tmp`
  and `/var/tmp`, and `/run/cryptsetup`, cryptsetup's lock directory, which the passphrase test
  writes.
- Home directories are hidden, and kernel tunables and control groups are read-only.
- The unit cannot load a kernel module itself. The kernel still loads one on request, as for a
  socket family or a cipher the unit uses.
- It has no network and no socket beyond Unix sockets and the kernel crypto API's.
- Devices stay accessible, because the storage facts resolve disks and test passphrases on block
  devices. cryptsetup may set up a temporary device-mapper mapping for a cipher the kernel crypto
  API lacks.
- It gains no privileges.

A command that exits with an error, takes longer than 30 seconds, prints more than 1 MiB or prints
no JSON refuses the delivery.

A command fact names its program itself, or a script that calls it, never a link to a multi-call
binary such as kmod's `lsmod`, lvm2's `lvs` or busybox. For an image that does not run, as when a
staged image checks its config, chalkd follows a link within that image's store and runs what it
leads to under that name, so `lsmod` would run as `kmod`.

## Template functions { #template-functions }

[`chalkosLib.render.template`](extension-api.md#chalkoslibrendertemplate) renders a Go
`text/template`. The template reads the renderer's input as `.node.name` and
`.documents.<Kind>`. Besides Go's own functions it has five more:

| Function | Result |
| --- | --- |
| `join <sep> <list>` | The list's values, each as it prints, joined with `<sep>`. |
| `quote <value>` | The value as a double-quoted string with Go's escapes. |
| `json <value>` | The value as JSON. |
| `default <fallback> <value>` | `<fallback>` when the value is missing, null, empty or false; the value otherwise. |
| `refuse <kind> <name> <path> <message>` | Stops the render and refuses the config with a refusal of those four fields, as the [refusal report](#refusal-report) describes. |

A key that a document lacks, or that holds null, prints as nothing, so `{{ .spec.password }}`
prints nothing for a `Peer` without a password. A key below a missing one, such as
`.spec.missing.name`, fails the render.

## Program contract { #program-contract }

Renderers and validators are programs of the image's store, and a check holds shell commands that
bash runs. chalkd runs each with the same input and in the same sandbox, with the differences
below.

### Input { #program-input }

Every renderer and validator reads the same input on standard input, one JSON object: the node's
name and the documents of each input kind.

- `documents` holds one list per kind of `inputs`, empty when the node holds no document of the
  kind. Each list is sorted by the documents' names.
- The documents carry their schema defaults, so a field the YAML leaves out holds its default.
- The object holds no `Secret`. A `secretRef` field holds the reference alone, never the value.
- `node.name` is the node's name in the inventory, which its node certificate carries.

A renderer writes its files into the directory that the environment variable `OUT` names. chalkd
runs a validator as `<program> validate <name>`: it appends `validate` and the validator's name to
the command, which a program that serves several validators reads to tell them apart.

A command that `render` or `program` names is a program of the image's store, with its arguments,
or a package that is such a program. chalkd splits the command on spaces and runs it without a
shell, so the command cannot redirect output or expand `$OUT`. A script from
`pkgs.writeShellScript` does both.

### Output { #program-output }

A renderer may write any number of files and directories into `$OUT`, but no links. Its standard
output is reserved for refusals: anything it prints there that is not a report of refusals fails
the render. jq runs with `-r` in `chalkosLib.render.jq`, so a program whose result is a string
writes the string raw, without quotes.

What a renderer, a validator or a check writes to standard error goes to the node's journal
alone, because it can echo the documents it read.

### Refusal report { #refusal-report }

A renderer or a validator refuses a config by printing a report of refusals on standard output,
one JSON object:

```json
{"refusals": [{"kind": "Peer", "name": "router", "path": "spec.asn", "message": "what is wrong and how to fix it"}]}
```

- `kind` and `name` name the document. `name` takes the form of a document name.
- `path` names the field, as written in the document, such as `spec.asn` or
  `spec.neighbors[0].address`, without spaces or colons.
- `message` says what is wrong and how to fix it, on one line of at most 1 KiB.

These rules decide what a report does:

- Any refusal refuses the delivery, whether the program then exits with 0 or not.
- An empty report, `{"refusals": []}`, or no output refuses nothing. The program then passes when
  it exits with 0.
- Any other exit status fails the program.
- A report that does not take this form fails the program as its own error, as does more than
  64 KiB of output. When the program also exits with a status other than 0, chalkd reports that
  status, as it does for any program that fails.

chalkd reports each refusal after `the config is refused:`, as it reports a rule's refusal, and
reports a refusal that several outputs print once. A failed validator refuses the delivery with
`validator <name>: its program failed (exit <n>); see the node's journal`, and a failed renderer
or check with a message that names the output and the exit status. A refusal can quote any value
of the documents. It cannot carry a secret's value, because no program reads one.

A jq renderer cannot refuse: what the program prints is the output's file. Its `error` fails the
render, with the message in the node's journal alone. A check refuses the rendered files by
failing. Its standard output goes nowhere.

### Check environment { #check-environment }

`check` holds shell commands. chalkd runs them with bash and `set -e`, so the first command that
fails refuses the files. The check runs after the renderer, in the same sandbox:

- `$OUT` holds the files as the renderer wrote them, before they get the output's owner and mode.
  The check may not change them.
- Standard input holds the renderer's input, so a check can compare the files with the documents.
- `PATH` holds coreutils, grep and sed alone. Any other program is named by its store path,
  as `${bird}/bin/bird` is.
- Unlike the renderer, the check may open netlink sockets. They reach the sandbox's own network
  namespace, or one the check creates: the router's check runs
  `unshare --map-root-user --net nft --check --file "$OUT/ruleset.nft"`, so the kernel checks the
  ruleset.

An output without `check` has its files served as the renderer wrote them.

### Sandbox and limits { #sandbox }

chalkd runs every renderer, check and validator as a transient unit with these limits:

- It runs as the unprivileged system user `chalkos-render`, without capabilities, devices or
  network. A check may open netlink sockets, which reach no network but a namespace's own. A
  renderer or a validator opens none.
- The system is read-only. STATE, `/var`, and the config, credentials and Kubernetes keys chalkd
  writes below `/run/chalkos` are hidden, so a program cannot read a secret even by its path.
- A renderer may write `$OUT` and a private `/tmp` of 16 MiB alone. A validator writes no
  output's files, so it may write no path but its private `/tmp` of 16 MiB.
- It must finish within 30 seconds. The validators of a delivery run one after another.
- No file it writes, standard output among them, may grow past 16 MiB, and a renderer's files
  together may take 16 MiB and hold no links, because chalkd sets their owner afterwards and must
  not follow a link to a file the output does not own.

The outputs' files, those the node serves and those a render stages, sit on a tmpfs of their own
at `/run/chalkos/outputs`. It has room for every output's files twice and 16 MiB more, so a
renderer that writes without end fills that tmpfs and not `/run`.

An image that does not run checks a config in `chalkos-check.service`, which adds limits of its
own, as
[An image that does not run checks its own config](../how-it-works/node/config-pipeline.md#an-image-that-does-not-run-checks-its-own-config)
describes.

## Output directories { #output-directories }

Services read the files at the output's `dir`, `/run/chalkos/outputs/<name>` by default. `dir` is
a symbolic link to the directory of the last render. chalkd replaces the link in one rename, so a
service reads the files of one render and never a mix of two.

The built-in output `networkd` uses `/run/systemd/network`. The first swap removes a directory it
finds at `dir`, so the image's evaluation refuses a `dir` that breaks these rules:

- It is a clean path below `/run`, and no component starts with a dot.
- It stays out of `/run/chalkos`, except for `/run/chalkos/outputs/<name>` and the paths below it.
- It is neither another output's `dir` nor inside one.

chalkd checks the same rules again when it reads the image's `/etc/chalkos/outputs.json`, and
refuses an `outputs.json` with a `dir` that breaks one.

The files belong to the output's `user` and `group`, `root` and `root` by default, with its
`mode`, `0644` by default. Directories get `0755`. `mode` is three octal digits with an optional
leading `0`.

## Field types { #field-types }

A kind's field's option type decides its JSON Schema. The converter knows these types:

| Option type | JSON Schema |
| --- | --- |
| `str` | `string` |
| `nonEmptyStr` | `string` with `minLength: 1` |
| `strMatching "<regex>"` | `string` with `pattern` anchored at both ends |
| `bool` | `boolean` |
| `int` | `integer` |
| `ints.u8`, `ints.u16`, `ints.u32`, `ints.s8`, `ints.s16`, `ints.s32`, `port` | `integer` with the type's `minimum` and `maximum` |
| `ints.unsigned`, `ints.positive` | `integer` with `minimum` 0 or 1 |
| `ints.between <low> <high>` | `integer` with `minimum` and `maximum` |
| `float` | `number` |
| `enum [ ... ]` | `enum` with the values |
| `nullOr <type>` | `anyOf` the type and `null` |
| `listOf <type>`, `nonEmptyListOf <type>` | `array` of the type; the second with `minItems: 1` |
| `attrsOf <type>`, `lazyAttrsOf <type>` | `object` whose values have the type |
| `submodule { options = ...; }` | `object` with the submodule's options, refusing others |
| `chalkosLib.types.ref "<Kind>"`, `chalkosLib.types.ref { kind = "<Kind>"; reserved = [ ... ]; }` | `string` with the pattern of a document name, and `x-chalkos-ref` with the kind's `apiVersion`, the `kind` and `reserved` |
| `chalkosLib.types.secretRef` | `object` holding `secretRef` with `name` and `key` |
| `chalkosLib.types.address` | `string` with `format: ip` |
| `chalkosLib.types.prefix` | `string` with `format: cidr` |
| `chalkosLib.types.portRange` | `anyOf` an `integer` from 1 to 65535 and a `string` with `format: port-range` |
| `chalkosLib.types.imageReference` | `string` with `format: image-reference` |

A pattern of `strMatching` is a POSIX regular expression to Nix and an ECMA-262 one to JSON
Schema. Character classes such as `[[:alpha:]]` differ between the two, so a pattern works the
same in both only within their common subset.

Any other type fails the image's evaluation and names the field. A field of type `path` fails
like this:

```text
error: chalkos.kinds.Peer.spec.keyFile: the option type absolute path has no JSON Schema mapping. Kinds accept str,
nonEmptyStr, strMatching, bool, int, ints.*, port, float, enum, nullOr, listOf, attrsOf,
submodule and the types of chalkosLib.types.
```

## Unique declarations { #unique }

`unique` on [`chalkosLib.mkOption`](extension-api.md#chalkoslibmkoption) takes `true`, one
declaration, or a list of declarations, each checked on its own. A declaration has these
attributes:

| Attribute | Meaning |
| --- | --- |
| `across` | `"kind"` compares the values of all the node's documents of the kind, `"list"` the items of one list. The default is `"list"` for a list with `by` and `"kind"` otherwise. |
| `by` | The fields of a list's items, or of an object, that make up the key, each a field name or `{ field; as; }`. Without `by`, the key is the value itself, or each item of a list of values. |
| `as` | The normaliser of a key without `by`. |
| `hint` | What to do instead, which ends the refusal. |

`unique = true` on a single value compares it across the kind's documents. On a list of values,
it compares each item across the kind, so a value listed twice in one document is refused too.
`chalkos.kinds.<Kind>.unique` takes a list of `{ by; hint; }`, whose `by` names fields of the
spec and compares across the kind.

A normaliser changes how a value compares. The message still shows the value as written:

| `as` | Compares |
| --- | --- |
| `ip` | as an address, as the rules' `ip()` reads it: `2001:db8::1` equals `2001:db8:0::1`. |
| `cidr` | as an address range with its host bits cleared, as `cidr()` reads it: `10.1.0.0/8` equals `10.0.0.0/8`. |
| `host` | as a registry host, in lower case and with `docker.io` as `index.docker.io`. A port stays, so `ghcr.io:443` and `ghcr.io` differ. |
| `lower` | in lower case. |

A null value or a field left out takes no part. A key of several fields takes part when one of
them holds a value, and a null field then equals another null field. A value its normaliser
cannot read takes no part either.

The check takes a time that grows with the number of values.

The image build fails on a declaration that does not fit its field: an unknown normaliser, `by`
on a list of values, `across = "list"` on a field that is no list, a `by` field the items lack or
one that holds a list or an object, `as` on a field that may hold a value other than a string (an
`enum` of strings holds strings only), and a declaration on a `secretRef`.

Sources: the CEL environment and functions in `pkg/rules`, the facts and their sandbox in
`pkg/facts`, the renderers, validators and their sandbox in `pkg/outputs` and `pkg/refusals`,
the template functions in `cmd/chalkos-render`, the output directories in
`modules/node/runtime/outputs.nix`, and the field types and `unique` in `lib/kinds/schema.nix`.
