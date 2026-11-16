---
title: "Guard changes with rules and facts"
description: "Refuse a config change a node cannot make, with CEL rules on your kind and facts about the node"
---

# Guard changes with rules and facts

This page is for an image author whose kind has values that must not change freely once a node
runs them. At the end the kind carries change rules, chalkctl and chalkd refuse a change that
breaks one, and the refusal tells the operator what to do instead. The page uses two example
extensions: the `Peer` kind of the homelab's `extensions/bgp.nix` and the `Rack` kind of
`rack.nix` in the [extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions).

A change rule is a CEL expression that every config the node accepts makes true. The rules are
part of the image's schemas, so what a node accepts changes with an upgrade. A rule that needs
node state, such as what a survey found, reads it from a fact: the JSON of a file or of a program.

[Extension runtime](../reference/extension-runtime.md) lists the variables and functions a rule
reads, its cost limits and its errors. [The config pipeline](../how-it-works/node/config-pipeline.md#chalkctl-and-chalkd-both-run-the-rules)
explains where the rules run.

## Before you begin

You need a kind of your own, as [Declare a config kind](declare-a-kind.md) declares one. Rules
on a field need `chalkosLib.mkOption`, which that page introduces. CEL is the
[Common Expression Language](https://cel.dev/); its syntax resembles that of C and JavaScript.

## Keep a field's value with a field rule

A BGP neighbour's AS number is part of who it is: a new number is a new neighbour. The `asn`
field of `Peer` declares a rule that keeps it:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:rule"
```

In a field's rule, `self` is the field's value in the delivered document and `oldSelf` its value
in the document of the same name that the node runs. `self == oldSelf` holds when the number stays
the same. A rule that reads `oldSelf` runs only where the node's running config holds the field,
so a new `Peer` takes any number.

Say `bgp/fabric.yaml` changes the `asn` of the `Peer` `router` from 65000 to 65100.
`chalkctl config apply node1` fetches the node's config, prints what changed, runs the rule
against the node's config, and stops before it sends anything:

```console
$ chalkctl config apply node1
node1: config 0 added, 0 removed, 1 changed
chalkctl: node1: the config is refused: Peer/router: spec.asn: the AS number stays 65000; add a Peer of another name for 65100
```

The error names the node, then the refusal. A field rule's refusal starts with the kind and the
document's name, then the field, as `Peer/router: spec.asn:`, the form a renderer's refusal takes
too. The rest comes from `messageExpression`, which builds the message from the same variables as
the rule.

## Say what the refused change takes

A rule's `message` or `messageExpression` says why the change is refused and what to do instead.
Its `takes` says what the refused change needs instead: `refuse`, the default, `reset` or
`reinstall`. [Rule attributes](../reference/extension-runtime.md#rule-attributes) lists every
attribute of a rule.

A config whose rules fail is refused as a whole. The refusal starts with the most that any failed
rule takes, then lists every failed rule's message, separated by `;`:

| `takes` | The refusal starts with | Meaning |
| --- | --- | --- |
| `refuse` | `the config is refused:` | The change cannot be made. The message says what to do instead. |
| `reinstall` | `the config takes a reinstall of <node>:` | Only a reinstall of the node carries the change out. |
| `reset` | `the config takes chalkctl storage reset:` | `chalkctl storage reset` carries the change out. |

Your own kinds mostly use `refuse`. The storage kinds use `reset` and `reinstall`, because a
volume's new format is possible once the volume is recreated.
[`chalkctl storage reset`](../reference/cli/chalkctl_storage_reset.md) recreates one volume.

## Compare a kind's documents with a kind rule

A rule over all documents of a kind goes into
[`chalkos.kinds.<name>.rules`](../reference/extension-api.md#chalkoskindsrules). Here `documents` maps
the names of the delivered `Peer` documents to the documents:

```nix
chalkos.kinds.Peer.rules = [
  {
    rule = "size(documents) <= 2";
    message = "a node has two BGP neighbours at most";
  }
];
```

With two more `Peer` documents in `bgp/fabric.yaml`, `chalkctl validate` refuses the three nodes
that get the file:

```console
$ chalkctl validate
chalkctl: the config of 3 nodes is invalid:
node node1: the config is refused: a node has two BGP neighbours at most
node node2: the config is refused: a node has two BGP neighbours at most
node node3: the config is refused: a node has two BGP neighbours at most
```

A kind's message names nothing by itself, so it says which kind it is about.

A value that must not repeat, among a kind's documents or within one list, needs no rule.
Declare it with `unique` on the field or the kind, as
[Make a field's values unique](declare-a-kind.md#make-a-fields-values-unique) shows.
chalkctl and chalkd check such declarations before any rule runs. A rule that compares each
value with every other one costs two nested loops, which comes close to the
[cost limit](../reference/extension-runtime.md#cost-limits).

## Compare fields of one document with a spec rule

A rule that compares fields of one document goes into
[`chalkos.kinds.<name>.specRules`](../reference/extension-api.md#chalkoskindsspecrules). Its `self`
is the spec of one delivered document and `oldSelf` the spec of the running config's document of
the same name, so it runs as a field's rule does, once for each document. The `Session` of
[Make a field's values unique](declare-a-kind.md#make-a-fields-values-unique) keeps its
neighbours off its peer's address:

```nix
chalkos.kinds.Session.specRules = [
  {
    description = "A Session's neighbours leave out its peer's address.";
    rule = ''self.neighbours.all(n, n.address != document("Peer", self.peer).spec.address)'';
    message = "a neighbour has the peer's own address; list the peer's neighbours alone";
  }
];
```

A spec rule's message starts like a field rule's, with the kind, the document's name and `spec`:
`Session/up: spec: a neighbour has the peer's own address; list the peer's neighbours alone`.

The same check as a kind rule loops over `documents` around the loop over the neighbours. A kind
rule that compares two lists of each document, such as each reservation of a subnet with each of
its pools, therefore costs three nested loops, which exceeds the
[cost limit](../reference/extension-runtime.md#cost-limits). As a spec rule it costs two.

The build estimates that cost with 256 items in each list, but a node meets the lists of the
config it gets. A spec rule over two long lists can pass the build and still exceed the node's
limit. Bound such lists with `maxItems` on `chalkosLib.mkOption`, so that the schema refuses a
list longer than the rule can loop over, and test the rule at the bounds. A `DHCPSubnet` takes 32
pools and 512 reservations at most: its rule that compares each reservation with each pool then
costs about 330,000.

## Read node state from a fact

A fact is node state that a rule reads as `facts.<name>`. The extensions example's `Rack` kind
refuses a location that differs from the one a rack survey wrote to the node:

```nix title="rack.nix"
--8<-- "examples/extensions/rack.nix"
```

[`chalkos.facts`](../reference/extension-api.md#chalkosfacts) declares the fact `rackSurvey`: the JSON
of `/var/lib/rack-survey/location.json`, or null while the file is missing. The rule does not
read `oldSelf`, so it checks every `Rack` a node is delivered, its first one too.

Say the survey file on `server1` holds `{"location": "rack-b/u3"}`, because the node moved, while
`nodes/server1.yaml` still says `rack-a/u12`. The rule reads a fact, so chalkctl cannot check it and
the node refuses the delivery. chalkctl prints the node's error after `chalkctl:` and the error's
code. The message part of it is:

```text
the config is refused: Rack/rack: spec.location: the survey found the node at rack-b/u3, not at rack-a/u12; fix the location, or survey the node again
```

Set the location the survey found where the survey is right, or survey the node again where the
document is.

A fact is a file or a command:

- A file fact, `{ file = "/var/lib/rack-survey/location.json"; }`, is the file's JSON, and null
  while the file is missing. `fields = [ "location" ]` keeps only those keys of the file's JSON
  object, so a rule and its message see nothing else of the file. `hashOthers = true` keeps the
  object's other keys too, each as the hex SHA-256 of its value, so a rule sees that a value
  changed but not what it holds. A file that cannot be read or holds no JSON, or no JSON object
  with `fields` or `hashOthers`, refuses the delivery.
- A command fact, `{ command = "${pkg}/bin/tool facts"; }`, is a program of the image's store with
  its arguments, which prints the fact as JSON. [Extension runtime](../reference/extension-runtime.md#facts)
  lists what it reads, and the [sandbox](../reference/extension-runtime.md#command-fact-sandbox)
  it runs in.

A message can show what a fact holds, as the `Rack` message shows the survey's location. Keep
what a rule does not need out of the fact with `fields`, or as hashes with `hashOthers`.

Treat a null fact as unknown, as the `Rack` rule does with `facts.rackSurvey == null`. A file
fact is null while its file is missing, and facts can be null at an install too:
[Null means the node does not know](../how-it-works/node/config-pipeline.md#null-means-the-node-does-not-know)
lists every case.

## Check that it worked

Build the image, which compiles every rule. Then run `chalkctl validate`, which runs the rules of
kinds and the field rules that read neither `oldSelf` nor facts:

```sh
chalkctl validate
```

The rules that read `oldSelf` run on `chalkctl config apply`, against the node's config. Rules
that read facts run on the node alone; `chalkctl config apply --dry-run` has the node run them
without delivering the config.

## If it fails

A refusal that starts with `cannot check:` comes from a rule that cannot be evaluated or a fact
that cannot be loaded. It names the kind of error and no value, as
[Rule errors](../reference/extension-runtime.md#rule-errors) lists. The journal of
`chalkd.service` holds what a failed command fact wrote.

## Built into chalkos

chalkos's own kinds guard changes the same way.
[Built-ins](../reference/builtins.md#change-rules) lists their rules and the
[facts](../reference/builtins.md#facts) that they and the built-in effects read: those of every
image and those that images with Kubernetes add. The router's `DHCPSubnet` checks its pools and
reservations against each other with spec rules.
[Built-in rules and facts](../how-it-works/node/config-pipeline.md#built-in-rules-and-facts)
explains the storage facts and the rules images with Kubernetes add.

## What next

- [Add a diagnostic](add-a-diagnostic.md) lets an operator look into the service on a node.
- [Validate config with a program](validate-config.md) for a check no rule can make.
- [Test an extension](test-an-extension.md) to keep the rules' refusals in the image's tests.
- [React to changes](react-to-changes.md) for `when`, which uses the same CEL and facts.
- [The config pipeline](../how-it-works/node/config-pipeline.md) for where rules run in a
  delivery.
