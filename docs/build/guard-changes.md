---
title: "Guard changes with rules and facts"
description: "Refuse a config change a node cannot make, with CEL rules on your kind and facts about the node"
---

# Guard changes with rules and facts

This page is for an image author whose kind has values that must not change freely once a node
runs them. At the end the kind carries change rules, chalkctl and chalkd refuse a change that
breaks one, and the refusal tells the operator what to do instead. The examples use a `Site`
kind of a web server, myapp, and the `Rack` kind of `rack.nix` in the
[extensions example](https://github.com/trevex/chalkos/tree/main/examples/extensions).

A change rule is a CEL expression that every config the node accepts makes true. The rules are
part of the image's schemas, so what a node accepts changes with an upgrade. A rule that needs
node state, such as what a survey found, reads it from a fact: the JSON of a file or of a program.

[Extension runtime](../reference/extension-runtime.md) lists the variables and functions a rule
reads, its cost limits and its errors. [The config pipeline](../how-it-works/node/config-pipeline.md#chalkctl-and-chalkd-both-run-the-rules)
explains where the rules run.

See it in a full extension: [Keep a Peer's AS number](../guides/bgp-extension.md#keep-a-peers-as-number)
of the BGP guide keeps a neighbour's AS number and tests the refusal.

## Before you begin

You need a kind of your own, as [Declare a config kind](declare-a-kind.md) declares one. Rules
on a field need `chalkosLib.mkOption`, which that page introduces. CEL is the
[Common Expression Language](https://cel.dev/); its syntax resembles that of C and JavaScript.

## Keep a field's value with a field rule

Clients reach a site by its hostname, so a new hostname is a new site. The `hostname` field of
`Site` declares a rule that keeps it:

```nix
hostname = chalkosLib.mkOption {
  type = lib.types.nonEmptyStr;
  description = "Name the site answers to.";
  rules = [
    {
      description = "A Site keeps its hostname.";
      rule = "self == oldSelf";
      messageExpression = ''"the hostname stays " + oldSelf + "; add a Site of another name for " + self'';
    }
  ];
};
```

In a field's rule, `self` is the field's value in the delivered document and `oldSelf` its value
in the document of the same name that the node runs. `self == oldSelf` holds when the hostname
stays the same. A rule that reads `oldSelf` runs only where the node's running config holds the
field, so a new `Site` takes any hostname.

Say the `Site` `shop` changes its hostname from `shop.example.org` to `store.example.org`.
`chalkctl config apply` fetches the node's config, prints what changed, runs the rule against the
node's config, and stops before it sends anything. It prints the node, then the refusal:

```text
the config is refused: Site/shop: spec.hostname: the hostname stays shop.example.org; add a Site of another name for store.example.org
```

A field rule's refusal starts with the kind and the document's name, then the field, as
`Site/shop: spec.hostname:`, the form a renderer's refusal takes too. The rest comes from
`messageExpression`, which builds the message from the same variables as the rule.

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
the names of the delivered `Site` documents to the documents:

```nix
chalkos.kinds.Site.rules = [
  {
    rule = "size(documents) <= 2";
    message = "a node serves two sites at most";
  }
];
```

With three `Site` documents in a node's files, `chalkctl validate` refuses the node:

```text
the config is refused: a node serves two sites at most
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
the same name, so it runs as a field's rule does, once for each document. The `Site` of
[Make a field's values unique](declare-a-kind.md#make-a-fields-values-unique) keeps its
backend's address out of its list of clients:

```nix
chalkos.kinds.Site.specRules = [
  {
    description = "A Site's clients leave out its backend.";
    rule = ''self.allow.all(c, c.address != document("Backend", self.backend).spec.address)'';
    message = "the backend's address is no client; list the site's clients alone";
  }
];
```

A spec rule's message starts like a field rule's, with the kind, the document's name and `spec`:
`Site/shop: spec: the backend's address is no client; list the site's clients alone`.

The same check as a kind rule loops over `documents` around the loop over the clients. A kind
rule that compares two lists of each document, such as each reservation of a subnet with each of
its pools, therefore costs three nested loops, which exceeds the
[cost limit](../reference/extension-runtime.md#cost-limits). As a spec rule it costs two.

The build estimates that cost with the `maxItems` of each list, or with 256 items in a list that
sets none. A node stops the rule at the same limit on the lists of the config it gets, so a spec
rule over two unbounded lists can pass the build and still exceed the limit on longer lists.
Bound such lists with `maxItems` on `chalkosLib.mkOption`: the schema then refuses a list longer
than the rule can loop over, and the build checks the rule's cost at the bounds. A `DHCPSubnet`
takes 32 pools and 512 reservations at most: its rule that compares each reservation with each
pool then costs about 330,000.

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

## Keep a field once the node holds state, and alike on every node

Some values work only while they stay the same. A Kubernetes cluster's domain is part of every
service's name and of the API server's certificate, so a node keeps it once it joined the cluster,
and every node of the cluster holds the same domain. `chalkosLib.mkOption`'s `fixed` declares such
a field without a hand-written rule. The kind's `state` names the fact that is null until the node
holds the state the field depends on:

```nix
chalkos.facts.deployed.file = "/var/lib/myapp/deployed.json";
chalkos.kinds.Site.state = {
  fact = "deployed";
  description = "the site is deployed";
  remedy = "add a Site of another name";
};
chalkos.kinds.Site.spec.hostname = chalkosLib.mkOption {
  type = lib.types.nonEmptyStr;
  description = "Name the site answers to.";
  fixed = [ "node" "cluster" ];
};
```

`fixed` takes `"node"`, `"cluster"` or both in a list. The two usually go together.

`fixed = "node"` gives the field the rule `facts.deployed == null || self == oldSelf`: once the
fact that `state` names is set, the node refuses a change of the field. A kind with a field marked
`"node"` needs `state`, and the image builds only when it declares the fact. The refusal
names the field by its path below `spec`, such as `hostname` or `podCIDRs.ipv4`, and, for a
string or a list of strings, both values. It ends with `description` and `remedy`:

```text
the config is refused: Site/shop: spec.hostname: the config changes hostname from shop.example.org to store.example.org; it is fixed once the site is deployed, so keep it, or add a Site of another name
```

`remedy` defaults to `reinstall the node`. A field of another type, or one whose old or new
value is null, gets the message without the values. `description` and `remedy` may contain no
quotes, backslashes or line breaks.

The rule runs only where the delivered config holds the field, so a config that leaves the field
out would drop its value without the rule seeing it. The build therefore fails on
`fixed = "node"` for a field that a valid config may leave without a value: a field below a
nullable group, which a config may set to null, and a field whose schema neither gives it a
default nor requires it, such as one whose default is a function, or a required field below an
option with a default. A nullable field with a default, null included, is allowed: every config
holds it once its defaults are filled in, and the rule sees null.

`fixed = "cluster"` adds no rule. It marks the field in the kind's schema with
`x-chalkos-fixed: cluster`; on a nullable field the mark sits on the field's `anyOf`. chalkctl
compares the nodes of an inventory on the marked fields. Two nodes disagree when a field that
either node's image marks holds different values on them, with the defaults filled in, so
a value left to its default equals the default written out. A document of one node is compared
with the other node's document of the same kind and name, or, for a kind a node holds once, with
its one document of the kind. A document that only one of the nodes holds is no disagreement.

- `chalkctl validate` names each pair of nodes that disagree once, and lists them in `-o json`
  under `disagreements`. A node whose own config is invalid is compared with no other node.
- `chalkctl config apply` refuses to deliver to a node that disagrees with any other node of the
  inventory, whichever nodes the command names. It does not compare a node whose config cannot be
  read; `chalkctl validate` of every node names that node as invalid.
- Without an inventory, chalkctl compares nothing.

The refusal names both nodes and both values, as JSON:

```text
the nodes node1 and node2 disagree on fields every node of the cluster holds alike: Site shop: spec.hostname is "shop.example.org" on node1 and "store.example.org" on node2; give every node of the inventory the same value
```

A list or a map compares as a whole. The evaluation fails on a mark inside a list's items or a
map's values; mark the list or the map instead. `sdk.FixedChanges` of the Go SDK finds the changes
of marked fields between two sets of documents, so other tools can run the same comparison.

The Kubernetes `Cluster` marks `endpoint`, `podCIDRs`, `serviceCIDRs` and `dnsIPs` with
`"cluster"`, and `domain` and `ipFamilies` with both. Its `state` names the fact `share`, with the
remedy `reinstall the node into a new cluster`. The per-family rules that keep `podCIDRs`,
`serviceCIDRs` and `dnsIPs` once the node holds a share are kind rules of their own, since they
compare only the families both configs have.

## Check that it worked

Build the image, which compiles every rule and checks the fields each reads against the kind's
schema, as [Change rules](../reference/extension-runtime.md#change-rule-variables) describes.
Then run `chalkctl validate`, which runs the rules of kinds and the field rules that read neither
`oldSelf` nor facts:

```sh
chalkctl validate
```

The rules that read `oldSelf` run on `chalkctl config apply`, against the node's config. Rules
that read facts run on the node alone; `chalkctl config apply --dry-run` has the node run them
without delivering the config. A test of the image runs every rule without a node. It gives the
rules that read `oldSelf` its `before`, and those that read facts its `facts`, as
[Test an extension](test-an-extension.md#refusals-name-the-document-the-field-and-the-message)
shows.

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
