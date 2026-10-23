---
title: "Upgrade Kubernetes"
description: "Move a cluster to a new Kubernetes release while etcd keeps its quorum and workloads drain within their budgets"
---

# Upgrade Kubernetes

On a cluster with Kubernetes, [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) also keeps
the version skew, guards etcd's quorum and drains each node; [Upgrade a
cluster](../operate/upgrade-cluster.md) covers the rest of the upgrade.

## Before you begin

- Three control planes, for an upgrade without API downtime. A cluster with one or two needs
  `--allow-downtime`, described below.

## Move to a new Kubernetes release

Set [`chalkos.kubernetes.package`](../reference/options.md#chalkoskubernetespackage) in the
variant's modules. Every image with Kubernetes carries the kubelet, so every such variant gets
a new image. Move one minor release at a time, as the Kubernetes version skew policy
requires; `chalkctl upgrade` upgrades the control planes before the
workers, which keeps the kubelets no newer than the API
server.

## Keep etcd's quorum and drain each node

Workloads move off each node while it reboots, within their
PodDisruptionBudgets.

1. Before a control plane is touched, chalkctl checks that etcd keeps its quorum without it:
   the other voters must be healthy and enough for a quorum. After the previous control plane
   came back, it retries this check for up to a minute while etcd settles.
2. The drain cordons the node and evicts its pods through the eviction API, so
   PodDisruptionBudgets apply. DaemonSet pods, static pods, finished pods and pods without a
   controller are kept; chalkctl names the last kind, because nothing restarts them elsewhere.

## Choose the flags for Kubernetes

| Flag | Effect |
| --- | --- |
| `--allow-downtime` | Lets a cluster of one or two control planes upgrade, although etcd loses its quorum while one of them reboots. etcd and the API server are down until it is back. With three or more, the flag changes nothing: chalkctl still waits for the other voters to be healthy. |
| `--delete-emptydir-data` | Also evicts pods with `emptyDir` volumes, whose data is lost. Without it, such a pod stops the drain. |

## Uncordon a node that rolled back

The next run that upgrades the node uncordons it. To give the node back to the scheduler before
that, uncordon it yourself:

```sh
kubectl uncordon w2
```

## If something goes wrong

- `without cp2 etcd has 1 healthy voter of 3, fewer than the 2 its quorum needs`: another
  control plane is down or unhealthy. chalkctl does not reboot cp2 until enough of the others are
  healthy; `chalkctl etcd members` shows which one is not.
- `etcd has 1 voter, and without cp1 fewer than the 1 its quorum needs: ...; pass --allow-downtime
  to accept that`: the cluster has one or two control planes, so the API server is down while one
  reboots. Pass `--allow-downtime` when that is acceptable.
- `pods ... have emptyDir volumes, whose data an eviction deletes; pass --delete-emptydir-data to
  evict them`: the drain refuses to delete those pods' scratch data unless you allow it.
- `etcd member cp3 is a learner yet; upgrade once it joined`: a control plane is still joining;
  run again once it finished.
- A drain that does not finish within `--timeout` leaves the node cordoned and stops the run.
  A PodDisruptionBudget that allows no eviction is the usual cause; `kubectl get pdb -A` shows
  it.
