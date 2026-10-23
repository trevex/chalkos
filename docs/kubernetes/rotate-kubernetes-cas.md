---
title: "Rotate the Kubernetes CAs and keys"
description: "Replace the Kubernetes CAs, the service-account key and the encryption key, and revoke Kubernetes access"
---

# Rotate the Kubernetes CAs and keys

[`chalkctl rotate`](../reference/cli/chalkctl_rotate.md) replaces what the API server and etcd trust
in the phases [Rotate certificates and
CAs](../operate/rotate-certificates.md#run-a-rotation-phase-by-phase) describes.

## Rotate the Kubernetes CAs

`kubernetes-ca` replaces the cluster's three Kubernetes CAs together. In accept, switch and finish,
control planes take the phase one at a time, each restarting its static pods on the new files and
waiting until etcd has every member healthy again, so etcd keeps its quorum, and workers follow.
The refresh delivers nothing to control planes, which issued their kubelet client certificates
themselves at the switch: it gives each worker a kubelet client certificate of the new CA, then
has every node's kubelet renew its serving certificate.

At the pause after accept, chalkos's add-ons and the workloads of the cluster's
[`Addon`](../reference/kinds.md#addon) documents have restarted and
trust both CAs. Restart your own workloads that talk to the API server, so they trust both CAs
too; pods started from then on do. Issue new kubeconfigs with `chalkctl kubeconfig`. Then
resume: the switch makes the new CAs issue, and the refresh gives the kubelets new client and
serving certificates.

At the pause after refresh, issue kubeconfigs again for anyone who did not get one at the first
pause, and finish. As with the OS CA, kubeconfigs issued during the rotation still trust the old
CA; issue them again after the finish.

## Rotate the service-account key

The API server signs service-account tokens with this key. The rotation runs through accept,
switch and refresh without stopping; the refresh restarts chalkos's add-ons, which get tokens of
the new key at once. Kubelets renew the tokens of other pods within the hour after the switch, so
the rotation pauses and names the time from which you can finish. It also names the Secrets of
type `kubernetes.io/service-account-token` that hold tokens of the old key: nothing signs those
again, so recreate them before you finish.

`--finish` before that hour has passed is refused, because it would invalidate tokens that pods
still use; `--finish --force` does it anyway.

## Rotate the encryption key

The API server encrypts Secrets in etcd with this key. The refresh has a control plane rewrite
every encrypted object, so it is stored under the new key, and checks that etcd holds none under
the old one. On a large cluster the rewrite can take longer than `--timeout`; the error says so,
and `--resume` with a longer timeout repeats it without harm, writing back unchanged the
objects it rewrote already. `--finish` is refused while etcd holds objects under the old key,
because removing the key would lose them.

## Revoke Kubernetes access

chalkd and the API server check no revocation list. Access ends
when what the certificate chains to is no longer trusted:

- To revoke a kubeconfig, rotate the Kubernetes CAs, and issue new kubeconfigs to everyone else.
- To revoke a service-account token, delete its ServiceAccount or Secret in Kubernetes; to
  invalidate every token at once, rotate the service-account key.
