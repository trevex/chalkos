---
title: "Roles and credentials"
description: "The role each node API method needs, the modes it is served in, and the order chalkctl looks for credentials"
---

# Roles and credentials

This page is for operators who hand out client files or read a refusal of the node API. It lists
the least role each method of the node API needs, the modes chalkd serves it in, what the console
on the machine's screen may call, and the order in which chalkctl looks for the credentials of a
command.

## Roles

A client's role comes from its certificate. A certificate the OS CA issued directly grants the
role its Organization names. A certificate whose chain passes through the node CA has the role
`node`, whatever its Organization says.

| Role | Granted by | Allows |
| --- | --- | --- |
| reader | a client certificate with the Organization `reader` | the methods that need reader |
| operator | a client certificate with the Organization `operator` | the methods that need reader or operator |
| admin | a client certificate with the Organization `admin`, or the certificate chalkctl issues itself from the secrets file | every method that needs reader, operator or admin |
| `node` | a node certificate, which chains through the node CA | RenewNodeCertificate of the Kubernetes layer alone |

No other role allows a method that needs `node`, and `node` allows no other method. A node in
maintenance mode whose image was built without an OS CA treats every client as an admin, so a
caller without a certificate may install it and run every diagnostic.

## Methods

chalkd checks the image's layers first, then the mode, then the certificate, then the role. A
method of a layer the image lacks fails with `UNIMPLEMENTED` and
`the kubernetes layer is not in this image`. A method called in a mode it is not served in fails
with `the node is in maintenance mode; install it first` or `the node is installed`. A
certificate that does not chain to an OS CA the node trusts at that moment fails with
`the client certificate does not verify against the OS CAs the node trusts: ...`. A call whose
role is too low fails with a message that names the role it needs, such as
`/chalkos.node.v1.NodeService/Reboot needs the operator role`.

chalkd serves NodeService on every image and the API of each layer beside it:
KubernetesService on images with the `kubernetes` layer, and RouterService on images with the
`router` layer.

The Console column says whether [the console](#the-console) may call the method: `yes`, or
`action` while the image allows the console's actions.

| Service | Method | Least role | Served in | Console |
| --- | --- | --- | --- | --- |
| NodeService | ActivateImage | operator | normal mode | |
| NodeService | ApplyConfig | admin | normal mode | |
| NodeService | ConfirmConfig | admin | normal mode | |
| NodeService | Contract | reader | maintenance and normal mode | |
| NodeService | Diagnostics | reader | maintenance and normal mode | yes |
| NodeService | Disks | reader | maintenance and normal mode | yes |
| NodeService | EnrollSecureBoot | admin | maintenance mode | |
| NodeService | GetConfig | admin | normal mode | |
| NodeService | Info | reader | maintenance and normal mode | yes |
| NodeService | Install | admin | maintenance mode | |
| NodeService | Logs | reader | maintenance and normal mode | yes |
| NodeService | Reboot | operator | maintenance and normal mode | action |
| NodeService | ResetVolume | admin | normal mode | |
| NodeService | RollbackConfig | admin | normal mode | action |
| NodeService | RollbackImage | operator | normal mode | action |
| NodeService | RunDiagnostic | reader, and the diagnostic's own role | maintenance and normal mode | yes, for the diagnostics of readers and operators |
| NodeService | SecureBootDrivers | reader | maintenance mode | |
| NodeService | Shutdown | operator | maintenance and normal mode | action |
| NodeService | Status | reader | normal mode | yes |
| NodeService | Upgrade | operator; admin when the request carries config or a fallback secret | normal mode | |
| KubernetesService | Bootstrap | admin | normal mode | |
| KubernetesService | DrainNode | operator | normal mode | |
| KubernetesService | EtcdLeave | admin | normal mode | |
| KubernetesService | EtcdMembers | reader | normal mode | |
| KubernetesService | EtcdRemoveMember | admin | normal mode | |
| KubernetesService | RenewNodeCertificate | `node` | normal mode, on control planes | |
| KubernetesService | RevokeNode | admin | normal mode | |
| KubernetesService | RotationStep | admin | normal mode | |
| KubernetesService | Status | reader | normal mode | yes |
| KubernetesService | UncordonNode | operator | normal mode | |
| KubernetesService | UnrevokeNode | admin | normal mode | |
| RouterService | Routes | reader | normal mode | |
| RouterService | Status | reader | normal mode | yes |

An Upgrade with config or a fallback secret from an operator fails with
`/chalkos.node.v1.NodeService/Upgrade with config or a fallback secret needs the admin role; without them the node gives the image its pending config, else carries its own config over`.

RunDiagnostic also needs the role the diagnostic names, which `chalkctl node diag` lists. A
reader who runs `ping` fails with `the diagnostic ping needs the operator role`.

## The console

[The console on the machine's screen](../operate/console.md) reaches chalkd through the socket
`/run/chalkd/console.sock`, without TLS. The socket's directory has mode 0700 and the socket mode
0600, both owned by root. A call on the socket has no certificate and no role. chalkd checks the
image's layers and the mode as for any call, then the Console column in place of the role:

- A method with an empty Console column fails with `PERMISSION_DENIED` and
  `the console may not call <method>`, such as
  `the console may not call /chalkos.node.v1.NodeService/Install`.
- A method marked `action` fails with `PERMISSION_DENIED` and
  `the image turns the console's actions off` on an image built with
  [`chalkos.console.actions.enable`](image-options.md#chalkosconsoleactionsenable) set to false.
- RunDiagnostic runs the diagnostics of readers and operators. A diagnostic of admins fails with
  `the diagnostic <name> needs the admin role`.

chalkd serves the socket only on an image built with
[`chalkos.console.enable`](image-options.md#chalkosconsoleenable) set to true, the default.
chalkctl has no command that calls Shutdown.

## Where chalkctl finds its credentials

Commands that need the secrets file read it only from the file `--secrets` names, or from
standard input with `--secrets -`. chalkctl never looks for a secrets file on its own, and no
inventory names one.

Commands that accept a client file take the first of:

1. the client file `--config` names, or the secrets file `--secrets` names. Passing both fails
   with `pass --secrets or --config, not both`;
2. the client file `$CHALKOSCONFIG` names;
3. `~/.config/chalkos/config`, where `chalkctl client new` writes a client file without `--out`.

With none of them, the command fails with
`there is no client file at <path>; pass --config or --secrets`.

chalkctl refuses a client file whose certificate expired, with
`the certificate of <path> expired on <date>; issue a new one with chalkctl client new`. It warns
during the certificate's last 30 days.

Sources: `pkg/chalkd/server.go`, `pkg/chalkd/console.go`, `pkg/chalkd/diagnostics.go`,
`pkg/chalkd/layers/kubernetes/plugin.go`, `pkg/chalkd/layers/router/router.go`,
`pkg/chalkd/upgrade.go`, `pkg/pki/roles.go` and `pkg/chalkctl/credentials.go`.
