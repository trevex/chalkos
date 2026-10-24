---
title: "Kubernetes certificates"
description: "Where the control plane's and the kubelets' certificates come from, how they are renewed and who approves them"
---

# Kubernetes certificates

A Kubernetes cluster on chalkos takes its CAs and keys from the cluster's secrets file instead of
`kubeadm init`. Each node receives its part in its Kubernetes share at install, and a control plane
issues its own certificates from the CAs in its share at every boot. The last field of a control
plane's Kubernetes line in `chalkctl status` says whether the control plane runs on them:

```text
kubernetes controlplane: bootstrapped, node ready: True, vip holder, control plane current
```

`control plane current` means that the API server and etcd serve the current certificates and the
API server answers ready.

## The CAs and keys of a cluster come from the secrets file

The secrets file holds the cluster's Kubernetes CA, the front-proxy and etcd CAs, the
service-account key and the encryption key. A control plane's share holds all of them with their
keys. A worker's share holds the cluster CA certificate and a kubelet client certificate.
[Which CAs and keys does a cluster have?](../node/certificates.md#which-cas-and-keys-does-a-cluster-have)
lists the CAs and their lifetimes. The API server encrypts Secrets in etcd with the cluster's
encryption key.

## A control plane issues its certificates at every boot

The control plane's certificates are issued at every boot into `/run/chalkos/kubernetes/pki`, a
tmpfs, which is mounted into the static pods as `/etc/kubernetes/pki`. Each pod carries a hash of
the certificates it mounts in the annotation `chalkos.dev/certificates`. When chalkd renews the
certificates, the hash changes, so the kubelet starts the pod again on the new files.
[Certificates of Kubernetes](../node/certificates.md#certificates-of-kubernetes) lists each
certificate a control plane issues.

## Kubelets renew their own certificates

A worker receives a kubelet client certificate for its own node name in its share at install. Its
kubelet registers at the cluster endpoint with it. The kubelet renews the certificate itself
through a certificate request. The controller-manager approves such a request for a node that
renews its own, through the RBAC binding chalkd applies for it.

Every kubelet also requests a serving certificate, which the API server checks when it fetches
logs or execs into a pod. chalkd on the control planes approves such a request when all of these
hold:

- the node asks for its own name;
- the certificate is for server use only;
- its addresses are ones the node's Node object lists.

chalkd leaves every other request pending. The approver runs from the moment the API server is
ready, beside the apply of the add-ons.

## Admin kubeconfigs carry the group chalkos:cluster-admins

[`chalkctl kubeconfig`](../../reference/cli/chalkctl_kubeconfig.md) writes an admin kubeconfig from
the secrets file, for the cluster endpoint. Its client certificate is in the group
`chalkos:cluster-admins`, which chalkd's RBAC add-on binds to `cluster-admin`.
[Get a kubeconfig](../../kubernetes/bootstrap.md#get-a-kubeconfig) describes its flags.

## Rotating the Kubernetes CAs replaces them across the cluster

`chalkctl rotate` replaces the Kubernetes CAs and keys across the cluster in phases.
[How is a CA or key rotated?](../node/certificates.md#how-is-a-ca-or-key-rotated) explains the
phases, and [Rotate the Kubernetes CAs and keys](../../kubernetes/rotate-kubernetes-cas.md) gives
the steps.

## Limits

- The approver of kubelet serving certificates trusts the addresses a kubelet reports for its own
  Node, as the [Security model](../node/security.md) explains.
- Nothing revokes a single kubeconfig. It works until its certificate expires or the Kubernetes CA
  is rotated.

## Related pages

- [Certificates](../node/certificates.md): every CA of a cluster, renewal and rotation.
- [The control plane](control-plane.md): the static pods that run on these certificates.
- [Rotate the Kubernetes CAs and keys](../../kubernetes/rotate-kubernetes-cas.md).
