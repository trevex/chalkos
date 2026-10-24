---
title: "Certificates"
description: "Every CA, key and certificate of a cluster, with its lifetime, its renewal point and the status notes about it"
---

# Certificates

This page is for operators who plan rotations or read the `certificates:` section of
`chalkctl status`. It lists every CA and key of a cluster, every certificate chalkos issues with
its lifetime and the point at which it is renewed, and the notes status shows about them.
[Certificates](../how-it-works/node/certificates.md) in How chalkos works explains how they chain.

Every CA has an ECDSA P-256 key. Every certificate chalkos issues is capped at its CA's own expiry
and backdated by one hour. No two CAs of a secrets file may share a key: the file is refused with
`<CA> and <CA> share a key; every CA needs its own`.

## CAs and keys

`chalkctl gen secrets` generates each of these once, into the secrets file. The last five exist
on the shipped variant's Kubernetes.

| CA or key | Lifetime | Issues or does | Held by | Status warns |
| --- | --- | --- | --- | --- |
| OS CA | 10 years | Client certificates and the node CA; path length 1 | The secrets file; its certificate is on every installed node and in every image built with `osCA` | in its last year |
| Node CA | 5 years | Node certificates, for TLS servers and clients only; path length 0 | The secrets file and every control plane | in its last 18 months |
| Kubernetes CA | 10 years | The certificates of the API server, the controllers, chalkd and the kubelets, and admin kubeconfigs | The secrets file and every control plane; workers get its certificate | in its last year |
| Front-proxy CA | 10 years | The certificate the API server presents to aggregated API servers | The secrets file and every control plane | in its last year |
| etcd CA | 10 years | etcd's server and peer certificates and those of its clients | The secrets file and every control plane | in its last year |
| Service-account key | No expiry | Signs service-account tokens, with ES256 | The secrets file and every control plane | never |
| Encryption key | No expiry | Encrypts Secrets in etcd, with secretbox | The secrets file and every control plane | never |

The secrets file also holds the recovery secret, from which each node's recovery key is derived.

## Certificates

| Certificate | Issued by | Lifetime | Renewed | Kept in |
| --- | --- | --- | --- | --- |
| Node certificate | chalkctl at install; a control plane, or chalkctl node renew | 1 year | by chalkd once two thirds of its lifetime have passed, checked about hourly; on a node without Kubernetes, by chalkctl node renew | `/state/chalkd/node.pem` on STATE, with its key and the node CA's certificate |
| Client certificate of a client file | chalkctl config new | 1 year, or `--ttl` | by issuing a new client file | the client file |
| chalkctl's admin certificate | chalkctl, from the secrets file, for one command | 1 hour | never; each command issues its own | memory |
| Control plane certificates | the control plane, from its Kubernetes share | 1 year | at every boot, and once two thirds of the lifetime of the first to expire have passed | `/run/chalkos/kubernetes/pki`, a tmpfs |
| chalkd's clients of the API server and etcd | chalkd on a control plane | 1 hour | at half their lifetime | memory |
| Kubelet client certificate | a worker's share at install; a control plane at every boot; then the controller-manager | 1 year | by the kubelet, once 70 to 90% of its lifetime have passed | `/var/lib/kubelet/pki` on VAR |
| Kubelet serving certificate | the controller-manager, on a request chalkd approves | 1 year | by the kubelet | `/var/lib/kubelet/pki` on VAR |
| Admin kubeconfig | chalkctl kubeconfig | 1 year, or `--ttl` | by issuing a new kubeconfig | the kubeconfig |

The control plane certificates are the API server's serving certificate, its clients of the
kubelets and of etcd, the front-proxy client, etcd's server and peer certificates, and the
certificates in the kubeconfigs of the controller-manager and the scheduler.

A failed renewal of a node certificate keeps the current certificate and retries after a minute,
doubling the wait up to an hour. chalkctl warns about a client file during its last 30 days and
refuses it once it expired.

## Status notes

`chalkctl status` lists each certificate a node holds with its expiry, and the CAs it trusts by the
first 16 hex digits of their fingerprints, the issuing one marked `(issues)`. A certificate line
carries a note when something needs doing:

| Note | Shown for |
| --- | --- |
| `less than a third of its lifetime remains` | A node or control plane certificate whose renewal is due |
| `less than a third of its lifetime remains; renew it with chalkctl node renew <node>` | A node certificate on a node without Kubernetes, whose renewal is due |
| `renewal failing: <reason>; expires <date>` | A renewal that failed |
| `less than a tenth of its lifetime remains, though the kubelet renews it itself` | A kubelet certificate close to its end |
| `<CA> expires <date>` | A CA in its last year |
| `node CA expires <date>; run chalkctl node-ca rotate` | The node CA in its last 18 months |
| `expired`, `<CA> expired <date>` | A certificate or CA past its end |
| `expired; deliver a new one with chalkctl apply-config <node> --kubernetes-share` | A worker's kubelet client certificate, when the one the kubelet renewed was lost with VAR and the one in its Kubernetes share has expired too |

The node CA is reported six months before the other CAs because no node certificate outlives it,
so the certificates it issues get shorter in its last year.

The clock's line is `time: synchronised to <source>, offset <seconds> s`, or
`time: not synchronised; certificates are checked against this clock`.

Sources: `pkg/pki`, `pkg/clustersecrets/kubernetes.go`, `pkg/kubernetes/pki`, `pkg/chalkd/renew.go`,
`pkg/chalkd/certstatus.go`, `pkg/chalkctl/credentials.go` and `pkg/chalkctl/node.go`.
