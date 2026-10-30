---
title: "Add a diagnostic"
description: "Declare a read-only command that operators run on a node with chalkctl node diag"
---

# Add a diagnostic

This page is for an image author whose service has state an operator needs to see on a node,
such as bird's BGP sessions. At the end, `chalkctl node diag w1 bird-protocols` prints them. The
page continues the homelab's BGP extension.

A diagnostic is a read-only command of the image that chalkd runs on a node for a client of the
node API. Nodes have no logins. On a node without Kubernetes no pod can debug the host either,
so a diagnostic is how an operator reads a service's state there. Every image declares the base
diagnostics, which [Built-ins](../reference/builtins.md#diagnostics) lists.

## Before you begin

You need an image flake with a module of your own, as
[Add software, drivers and kernel modules](add-software.md) sets up, and a service whose state a
command prints.

## Declare the diagnostic

`chalkos.diagnostics.<name>` declares a diagnostic. Here is bird's, as `extensions/bgp.nix`
declares it:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:diagnostic"
```

The name is what `chalkctl node diag` takes: up to 64 lower-case letters, digits and hyphens,
starting with a letter. `role` is the least client role that runs it. Readers may run this one,
because it only reads bird's state. A diagnostic without a `role` needs an operator.

## The command is a list of arguments

`command` is the command line as a list, and no shell reads it. Its first argument is a program
of the image's store: `"${bird}/bin/birdc"`, never `birdc`. A placeholder such as `{peer}` is
replaced by the parameter's value inside its own argument alone. `"'{peer}'"` becomes
`'router-a'`, one argument, whatever the value holds. The quotes are for bird, which needs them
around a protocol name with a hyphen.

`birdc -r` asks bird in its restricted mode, which takes only commands that read. Run a program
in such a mode when it has one.

## Parameters are typed

Each parameter names its type. chalkd refuses a value of another type before it runs anything,
and passes the command the value's canonical form:

| Type | Takes | Passes |
| --- | --- | --- |
| `address` | An IPv4 or IPv6 address, with a zone for a link-local one | The address in its canonical form |
| `prefix` | An address with a prefix length | The prefix in its canonical form |
| `hostname` | A DNS name of letters, digits, `_` and `-` | The name, fully qualified with a trailing dot |
| `interface` | A network interface's name of up to 15 characters | The name |
| `int` | A decimal integer from `min` to `max` | The integer without leading zeros |
| `enum` | One of `values` | The value |
| `pattern` | A string that `regex` matches whole | The string |

`regex` is in Go's RE2 syntax and runs from `^` to `$`. Whatever the type, chalkd refuses a
value that starts with `-`, which a program would read as an option, and a value that holds a
control character. The regex of `peer` takes the names a Peer document has, so no value closes
bird's quotes.

A call gives every parameter that has neither `optional` nor a `default`. When a call leaves out
an optional parameter, chalkd drops every argument that names it. Without `peer`, the command is
`birdc -r show protocols`, and bird shows every protocol. A `default` fills the arguments
instead, as `count`'s default of `4` does for the base diagnostic `ping`. A parameter has one or
the other.

## The run gets a sandbox of its own

chalkd runs each call as a transient systemd unit. The unit runs:

- under a dynamic user, without new privileges;
- with only the `capabilities` given, as its bounding and ambient sets, and the `groups` given;
- on a read-only file system with a private `/tmp`, without home directories, devices, STATE,
  `/var/lib`, or the config, credentials and Kubernetes keys chalkd writes below `/run/chalkos`;
- in the node's network, or in the network namespace that `networkNamespace` names, with Unix,
  IPv4, IPv6 and netlink sockets alone;
- with at most 256 MiB of memory and 64 tasks;
- for at most `timeout`, `30s` by default and `10m` at most, after which systemd stops it.

chalkd sends at most 1 MiB of what the command prints, then stops it. A node runs four
diagnostics at a time. A fifth call fails with
`resource_exhausted: 4 diagnostics run already; wait for one to end`.

bird makes its control socket writable for its group, `bird`. `groups = [ "bird" ]` lets the run
reach the socket. A daemon whose socket only root may use needs another way in. The base
diagnostic `nft-ruleset` gets `CAP_NET_ADMIN` to read nftables, and `ping` gets `CAP_NET_RAW`.

## Run in a network namespace

A service that runs in a network namespace of its own answers there alone. `networkNamespace`
names such a namespace below `/run/netns`, and may name parameters as `command` does. chalkd then
runs the unit with `NetworkNamespacePath=/run/netns/<name>`. The router's `nat64-sessions` reads
a NAT64's sessions in the NAT64's namespace:

```nix
networkNamespace = "nat64-{instance}";
```

The name holds letters, digits, `_`, `.` and `-`, and starts with neither `.` nor `-`. A
parameter that it names is not `optional`. chalkd refuses a call whose values fill the name to
anything else. A call whose namespace does not exist fails before the command runs, with
`failed_precondition: diagnostic nat64-sessions: the network namespace nat64-guest does not exist`.

## The build checks the declaration

The image fails to build when chalkd could not run the declaration:

- the name or a parameter's name is not of the form `[a-z][a-z0-9-]*`, or the name is longer than
  64 characters;
- the first argument is no program of the store;
- a placeholder names no parameter, or a parameter has no placeholder;
- the network namespace is not of the form above, or names an optional parameter;
- a parameter is optional and has a default, or its default is not of its type;
- a group is none of the image's;
- the timeout is not from `1s` to `10m`, a capability is none of Linux's, or a regex does not
  compile or does not run from `^` to `$`.

The message names the diagnostic or its option, such as
`chalkos.diagnostics.bird-protocols: the command or networkNamespace names {protocol}, which is no parameter`.

## Check that it worked

Build the image and upgrade a node to it, as
[Test and roll out an image](test-and-roll-out.md) describes. `chalkctl node diag` lists the new
diagnostic, and running it prints bird's protocols:

```sh
chalkctl node diag w1
chalkctl node diag w1 bird-protocols peer=router-a
```

`not_found: the image declares no diagnostic "bird-protocols"; chalkctl node diag lists those it does`
means the node still runs an image without it.

## What next

- [Built-ins](../reference/builtins.md#diagnostics) lists the diagnostics every image declares.
- [Extension API](../reference/extension-api.md#diagnostics) lists the options of
  `chalkos.diagnostics`.
- [Status, logs and the console](../operate/status-and-logs.md#chalkctl-node-diag-runs-the-images-diagnostics)
  shows how an operator runs diagnostics.
