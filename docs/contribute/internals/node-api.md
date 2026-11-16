---
title: "The node API in code"
description: "Where the node API and the layers' APIs are defined, how chalkd serves them and where their permissions live"
---

# The node API in code

[chalkd checks the role of every request](../../how-it-works/node/security.md#chalkd-checks-the-role-of-every-request) explains the API's roles; this page maps the API to the code.

## Four proto files define the APIs

The API is defined in four files below `api/chalkos/`:

| File | What it holds |
| --- | --- |
| `node/v1/node.proto` | NodeService, which every image serves. |
| `kubernetes/v1/kubernetes.proto` | The Kubernetes layer's API. |
| `router/v1/router.proto` | The router layer's API. |
| `check/v1/check.proto` | What chalkd and an image's checker exchange. No client calls it, and the API reference leaves it out. |

`buf generate` writes the Go code below `pkg/api/`. Comments in the proto files become the
[Node API](../../reference/api.md) page, so they are written for users.

chalkd serves NodeService over Connect on TCP port 50000 with mutual TLS. It mounts each compiled
layer plugin's service beside it.

## Each method has a permission entry

One table per API says which client role may call a method, and in which mode. NodeService's is
`permissions` in `pkg/chalkd/server.go`. Each layer plugin has its own `Permissions`, such as that
of `pkg/chalkd/layers/kubernetes`.

```go
nodev1connect.NodeServiceInfoProcedure: {Modes: layer.AnyMode, Role: pki.RoleReader},
kubernetesv1connect.KubernetesServiceBootstrapProcedure: {Modes: layer.NormalOnly, Role: pki.RoleAdmin},
```

A new method needs an entry there, because the authoriser refuses a method without one. For a
method of a layer the image lacks, the authoriser answers `UNIMPLEMENTED` before it checks the
mode. A handler of a layer's method therefore runs only on a node in normal mode whose image
carries that layer. It does not check for a missing layer itself.

One check sits outside the tables. `Upgrade` needs the admin role when its header carries config
or a fallback secret, as `ApplyConfig` does. The table cannot see the header, so `Upgrade` checks
the role itself.
