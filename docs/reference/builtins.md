---
title: "Built-ins"
description: "The change rules, facts, outputs, effects and diagnostics that chalkos's images declare"
---

# Built-ins

This page is for image authors and operators who want to know why a node refused a config,
what a delivery reloads and restarts, or what `chalkctl node diag` runs. It lists the change
rules, facts, outputs, effects and diagnostics that chalkos's own modules declare: those of every
image, and those that images with Kubernetes and router images add. An extension declares more
with the [Extension API](extension-api.md), and its outputs, effects and diagnostics take names
other than these.

## Change rules { #change-rules }

A change rule refuses a config that breaks it before the node applies any of it.
[Guard changes with rules and facts](../build/guard-changes.md) describes how rules are written
and where they run. Each table holds a kind's rules: the field a field rule sits on, or `—` for
a kind rule, which sees all of the kind's documents; what the rule guards; what a change it
refuses takes, `refuse`, `reset` or `reinstall`; the CEL expression, which holds for a config
the rule accepts; and the message, or the CEL expression that builds it. An expression too long
for its row follows the table.

### Every image { #rules-every-image }

The rules of the kinds every image accepts.

#### Mirror { #rules-mirror }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | No two Mirror documents mirror the same registry. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
documents.all(n, documents.all(m, n == m || documents[n].spec.registry != documents[m].spec.registry))
```

Message of rule 1:

```text
"the Mirror documents " + documents.filter(n, documents.exists(m, m != n && documents[m].spec.registry == documents[n].spec.registry)).join(", ") + " mirror the same registry; give one Mirror per registry"
```

#### Registry { #rules-registry }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | No two Registry documents name the same host. | `refuse` | `documents.all(n, documents.all(m, n == m || documents[n].spec.host != documents[m].spec.host))` | after the table |
| 2 | — | A Registry gives username and password together, or neither. | `refuse` | `documents.all(n, (documents[n].spec.username == null) == (documents[n].spec.password == null))` | after the table |

Message of rule 1:

```text
"the Registry documents " + documents.filter(n, documents.exists(m, m != n && documents[m].spec.host == documents[n].spec.host)).join(", ") + " name the same host; give one Registry per host"
```

Message of rule 2:

```text
documents.filter(n, (documents[n].spec.username == null) != (documents[n].spec.password == null)).map(n, "Registry " + n).join(", ") + ": give username and password together, or neither to pull anonymously"
```

#### SystemDisk { #rules-systemdisk }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | VAR keeps its format, encryption and size; a larger VAR, or one that fills the disk, is accepted. | `reinstall` | after the table | after the table |

Expression of rule 1:

```text
facts.recorded == null || !("var" in facts.recorded.volumes) || size(documents) != 1 || cel.bind(sd, documents.map(n, documents[n].spec)[0], cel.bind(r, facts.recorded.volumes["var"], size([(r.format == null ? "" : r.format) != "ext4" ? "format ext4 instead of " + ((r.format == null ? "" : r.format) == "" ? "none" : r.format) : "",
 ((sd.var.encryption.mode != null ? sd.var.encryption.mode : sd.encryption.mode) != r.encryption ? "encryption " + (sd.var.encryption.mode != null ? sd.var.encryption.mode : sd.encryption.mode) + " instead of " + r.encryption : ""),
 ((sd.var.size == null ? "" : sd.var.size) == (r.size == null ? "" : r.size) || (sd.var.size == null ? "" : sd.var.size) == "" ? "" : (r.size == null ? "" : r.size) == "" ? "size " + (sd.var.size == null ? "" : sd.var.size) + " instead of filling its disk" : !isSize((sd.var.size == null ? "" : sd.var.size)) || !isSize((r.size == null ? "" : r.size)) ? "size " + (sd.var.size == null ? "" : sd.var.size) + " instead of " + (r.size == null ? "" : r.size) : bytes((sd.var.size == null ? "" : sd.var.size)) < bytes((r.size == null ? "" : r.size)) ? "smaller: " + (sd.var.size == null ? "" : sd.var.size) + " instead of " + (r.size == null ? "" : r.size) : "")].filter(x, x != "")) == 0))
```

Message of rule 1:

```text
cel.bind(sd, documents.map(n, documents[n].spec)[0], cel.bind(r, facts.recorded.volumes["var"], "VAR: " + [(r.format == null ? "" : r.format) != "ext4" ? "format ext4 instead of " + ((r.format == null ? "" : r.format) == "" ? "none" : r.format) : "",
 ((sd.var.encryption.mode != null ? sd.var.encryption.mode : sd.encryption.mode) != r.encryption ? "encryption " + (sd.var.encryption.mode != null ? sd.var.encryption.mode : sd.encryption.mode) + " instead of " + r.encryption : ""),
 ((sd.var.size == null ? "" : sd.var.size) == (r.size == null ? "" : r.size) || (sd.var.size == null ? "" : sd.var.size) == "" ? "" : (r.size == null ? "" : r.size) == "" ? "size " + (sd.var.size == null ? "" : sd.var.size) + " instead of filling its disk" : !isSize((sd.var.size == null ? "" : sd.var.size)) || !isSize((r.size == null ? "" : r.size)) ? "size " + (sd.var.size == null ? "" : sd.var.size) + " instead of " + (r.size == null ? "" : r.size) : bytes((sd.var.size == null ? "" : sd.var.size)) < bytes((r.size == null ? "" : r.size)) ? "smaller: " + (sd.var.size == null ? "" : sd.var.size) + " instead of " + (r.size == null ? "" : r.size) : "")].filter(x, x != "").join("; ") + "; only a reinstall of " + node.name + " carries it out"))
```

#### Volume { #rules-volume }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A volume stays on the disk it was created on: the system disk or a disk of its own. | `reinstall` | after the table | after the table |
| 2 | — | A disk reference that differs from the one the volume's disk was pinned by finds that disk. | `reinstall` | after the table | after the table |
| 3 | — | A disk reference that differs from the one the volume's disk was pinned by resolves. | `refuse` | after the table | after the table |
| 4 | — | A volume keeps its format and encryption, also the encryption it inherits from the SystemDisk, and does not shrink or take a fixed size where it filled its disk. | `reset` | after the table | after the table |
| 5 | — | The pinned disk of a volume that the config creates or changes is plugged in. | `refuse` | after the table | after the table |
| 6 | — | No volume the machine records is left out of the config. | `reinstall` | `facts.recorded == null || facts.recorded.volumes.all(n, n == "var" || n in documents)` | after the table |
| 7 | — | A new TPM2-encrypted volume, on a node with a fallback, comes with the fallback secret, which opens the node's fallback keyslot. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
facts.recorded == null || documents.all(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, !(r != null && (v.disk == null ? "system" : n) != r.disk)))))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, r != null && (v.disk == null ? "system" : n) != r.disk)))).map(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, "Volume " + n + ": disk " + (v.disk == null ? "system" : n) + " instead of " + r.disk + "; only a reinstall of " + node.name + " carries it out")))).join("; ")
```

Expression of rule 2:

```text
facts.recorded == null || documents.all(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error == "" && !facts.disks[n].same)))))
```

Message of rule 2:

```text
documents.filter(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error == "" && !facts.disks[n].same)))).map(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, "Volume " + n + ": disk reference " + facts.disks[n].ref + " resolves to " + facts.disks[n].found + ", but the volume lives on " + facts.disks[n].identity + "; only a reinstall of " + node.name + " carries it out")))).join("; ")
```

Expression of rule 3:

```text
facts.recorded == null || documents.all(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error != "")))))
```

Message of rule 3:

```text
documents.filter(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error != "")))).map(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, "Volume " + n + ": disk reference " + facts.disks[n].ref + " cannot be resolved: " + facts.disks[n].error + "; "
+ (facts.disks[n].errorKind == "noDisk" ? "fix the disk reference or plug in the disk it names"
  : facts.disks[n].errorKind == "listDisks" ? "try again once the node can list its disks"
  : "fix the disk reference")
)))).join("; ")
```

Expression of rule 4:

```text
facts.recorded == null || documents.all(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, !(r != null && (v.disk == null ? "system" : n) == r.disk && !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error == "" && !facts.disks[n].same) && !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error != "") && size([(v.format == "raw" ? "" : v.format) != (r.format == null ? "" : r.format) ? "format " + ((v.format == "raw" ? "" : v.format) == "" ? "raw" : (v.format == "raw" ? "" : v.format)) + " instead of " + ((r.format == null ? "" : r.format) == "" ? "raw" : (r.format == null ? "" : r.format)) : "",
 ((v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) != r.encryption ? "encryption " + (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) + " instead of " + r.encryption : ""),
 ((v.size == null ? "" : v.size) == (r.size == null ? "" : r.size) || (v.size == null ? "" : v.size) == "" ? "" : (r.size == null ? "" : r.size) == "" ? "size " + (v.size == null ? "" : v.size) + " instead of filling its disk" : !isSize((v.size == null ? "" : v.size)) || !isSize((r.size == null ? "" : r.size)) ? "size " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : bytes((v.size == null ? "" : v.size)) < bytes((r.size == null ? "" : r.size)) ? "smaller: " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : "")].filter(x, x != "")) > 0)))))
```

Message of rule 4:

```text
documents.filter(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, r != null && (v.disk == null ? "system" : n) == r.disk && !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error == "" && !facts.disks[n].same) && !(v.disk != null && n in facts.disks && facts.disks[n].changed && facts.disks[n].error != "") && size([(v.format == "raw" ? "" : v.format) != (r.format == null ? "" : r.format) ? "format " + ((v.format == "raw" ? "" : v.format) == "" ? "raw" : (v.format == "raw" ? "" : v.format)) + " instead of " + ((r.format == null ? "" : r.format) == "" ? "raw" : (r.format == null ? "" : r.format)) : "",
 ((v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) != r.encryption ? "encryption " + (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) + " instead of " + r.encryption : ""),
 ((v.size == null ? "" : v.size) == (r.size == null ? "" : r.size) || (v.size == null ? "" : v.size) == "" ? "" : (r.size == null ? "" : r.size) == "" ? "size " + (v.size == null ? "" : v.size) + " instead of filling its disk" : !isSize((v.size == null ? "" : v.size)) || !isSize((r.size == null ? "" : r.size)) ? "size " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : bytes((v.size == null ? "" : v.size)) < bytes((r.size == null ? "" : r.size)) ? "smaller: " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : "")].filter(x, x != "")) > 0)))).map(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, "Volume " + n + ": " + [(v.format == "raw" ? "" : v.format) != (r.format == null ? "" : r.format) ? "format " + ((v.format == "raw" ? "" : v.format) == "" ? "raw" : (v.format == "raw" ? "" : v.format)) + " instead of " + ((r.format == null ? "" : r.format) == "" ? "raw" : (r.format == null ? "" : r.format)) : "",
 ((v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) != r.encryption ? "encryption " + (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) + " instead of " + r.encryption : ""),
 ((v.size == null ? "" : v.size) == (r.size == null ? "" : r.size) || (v.size == null ? "" : v.size) == "" ? "" : (r.size == null ? "" : r.size) == "" ? "size " + (v.size == null ? "" : v.size) + " instead of filling its disk" : !isSize((v.size == null ? "" : v.size)) || !isSize((r.size == null ? "" : r.size)) ? "size " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : bytes((v.size == null ? "" : v.size)) < bytes((r.size == null ? "" : r.size)) ? "smaller: " + (v.size == null ? "" : v.size) + " instead of " + (r.size == null ? "" : r.size) : "")].filter(x, x != "").join("; ") + "; reset it with chalkctl storage reset " + node.name + " " + n)))).join("; ")
```

Expression of rule 5:

```text
facts.recorded == null || documents.all(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, !(v.disk != null && n in facts.disks && facts.disks[n].pinned && !facts.disks[n].present && (r == null || (v.format == "raw" ? "" : v.format) != (r.format == null ? "" : r.format) || (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) != r.encryption || ((v.size == null ? "" : v.size) != (r.size == null ? "" : r.size) && !(isSize((v.size == null ? "" : v.size)) && isSize((r.size == null ? "" : r.size)) && bytes((v.size == null ? "" : v.size)) == bytes((r.size == null ? "" : r.size)))) || (v.mountPoint == null ? "" : v.mountPoint) != (r.mountPoint == null ? "" : r.mountPoint)))))))
```

Message of rule 5:

```text
documents.filter(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, v.disk != null && n in facts.disks && facts.disks[n].pinned && !facts.disks[n].present && (r == null || (v.format == "raw" ? "" : v.format) != (r.format == null ? "" : r.format) || (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) != r.encryption || ((v.size == null ? "" : v.size) != (r.size == null ? "" : r.size) && !(isSize((v.size == null ? "" : v.size)) && isSize((r.size == null ? "" : r.size)) && bytes((v.size == null ? "" : v.size)) == bytes((r.size == null ? "" : r.size)))) || (v.mountPoint == null ? "" : v.mountPoint) != (r.mountPoint == null ? "" : r.mountPoint)))))).map(n, cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, facts.disks[n].presentError != ""
  ? "Volume " + n + ": the node cannot find its disk " + n + ": " + facts.disks[n].presentError + "; try again once it can"
  : "Volume " + n + ": its disk " + n + " (" + facts.disks[n].identity + ") is not plugged in; plug it in"
)))).join("; ")
```

Message of rule 6:

```text
facts.recorded.volumes.filter(n, n != "var" && !(n in documents)).map(n, n in oldDocuments
  ? "Volume " + n + ": the config removes it; only a reinstall of " + node.name + " removes a volume"
  : "Volume " + n + ": an earlier config of this image or another added it, with an upgrade or chalkctl apply-config --staged, and it stays on the node; name it in this config too; only a reinstall of " + node.name + " removes it").join("; ")
```

Expression of rule 7:

```text
facts.recorded == null || config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0].encryption.fallback == "none" || size(documents.filter(n, !(n in facts.recorded.volumes) && cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) == "tpm2"))))) == 0 || (facts.fallback.sent && facts.fallback.matches != false)
```

Message of rule 7:

```text
!facts.fallback.sent
  ? "Volume " + documents.filter(n, !(n in facts.recorded.volumes) && cel.bind(v, documents[n].spec, cel.bind(sd, config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0], cel.bind(r, n in facts.recorded.volumes ? facts.recorded.volumes[n] : null, (v.encryption.mode != null ? v.encryption.mode : sd.encryption.mode) == "tpm2"))))[0] + " is encrypted and the node's fallback is " + config["storage.chalkos.dev/v1alpha1"].SystemDisk.map(k, config["storage.chalkos.dev/v1alpha1"].SystemDisk[k].spec)[0].encryption.fallback + ", but no fallback secret was sent"
  : "the fallback secret does not match the node's existing fallback keyslot"
```

### Images with Kubernetes { #rules-kubernetes }

The rules of the kinds that images with Kubernetes accept, and those they add to other kinds.

#### Cluster { #rules-cluster }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | The pod range of each family both configs have stays once the node holds a Kubernetes share. | `refuse` | after the table | after the table |
| 2 | — | The service range of each family both configs have stays once the node holds a Kubernetes share. | `refuse` | after the table | after the table |
| 3 | — | The cluster DNS's address of each family both configs have, by default the 10th of its service range, stays once the node holds a Kubernetes share. | `refuse` | after the table | after the table |
| 4 | `spec.allowSchedulingOnControlPlanes` | Whether workloads run on control planes stays once the node holds a Kubernetes share. | `refuse` | `facts.share == null || self == oldSelf` | the config changes allowSchedulingOnControlPlanes; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; add or remove the node-role.kubernetes.io/control-plane:NoSchedule taint with kubectl taint instead, or reinstall the node |
| 5 | `spec.ipFamilies` | The cluster's address families stay once the node holds a Kubernetes share. | `refuse` | `facts.share == null || self == oldSelf` | after the table |

Expression of rule 1:

```text
facts.share == null || size(oldDocuments) == 0 || size(documents) == 0 || cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], was.ipFamilies.filter(f, f in gets.ipFamilies).all(f, cidr(was.podCIDRs[f]) == cidr(gets.podCIDRs[f]))))
```

Message of rule 1:

```text
cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], cel.bind(families, was.ipFamilies.filter(f, f in gets.ipFamilies),
  "the config changes the cluster's podCIDRs from "
  + families.map(f, f + " " + string(cidr(was.podCIDRs[f]))).join(", ") + " to "
  + families.map(f, f + " " + string(cidr(gets.podCIDRs[f]))).join(", ") + "; they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster")
))
```

Expression of rule 2:

```text
facts.share == null || size(oldDocuments) == 0 || size(documents) == 0 || cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], was.ipFamilies.filter(f, f in gets.ipFamilies).all(f, cidr(was.serviceCIDRs[f]) == cidr(gets.serviceCIDRs[f]))))
```

Message of rule 2:

```text
cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], cel.bind(families, was.ipFamilies.filter(f, f in gets.ipFamilies),
  "the config changes the cluster's serviceCIDRs from "
  + families.map(f, f + " " + string(cidr(was.serviceCIDRs[f]))).join(", ") + " to "
  + families.map(f, f + " " + string(cidr(gets.serviceCIDRs[f]))).join(", ") + "; they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster")
))
```

Expression of rule 3:

```text
facts.share == null || size(oldDocuments) == 0 || size(documents) == 0 || cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], was.ipFamilies.filter(f, f in gets.ipFamilies).all(f, (was.dnsIPs[f] != null ? ip(was.dnsIPs[f]) : cidr(was.serviceCIDRs[f]).ip(10)) == (gets.dnsIPs[f] != null ? ip(gets.dnsIPs[f]) : cidr(gets.serviceCIDRs[f]).ip(10)))))
```

Message of rule 3:

```text
cel.bind(was, oldDocuments.map(o, oldDocuments[o].spec)[0], cel.bind(gets, documents.map(n, documents[n].spec)[0], cel.bind(families, was.ipFamilies.filter(f, f in gets.ipFamilies),
  "the config changes the cluster's dnsIPs from "
  + families.map(f, f + " " + string((was.dnsIPs[f] != null ? ip(was.dnsIPs[f]) : cidr(was.serviceCIDRs[f]).ip(10)))).join(", ") + " to "
  + families.map(f, f + " " + string((gets.dnsIPs[f] != null ? ip(gets.dnsIPs[f]) : cidr(gets.serviceCIDRs[f]).ip(10)))).join(", ") + "; they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster")
))
```

Message of rule 5:

```text
"the config changes the cluster's ipFamilies from " + oldSelf.join(", ") + " to " + self.join(", ") + "; they are fixed once the cluster exists, so keep them, or reinstall the node into a new cluster"
```

#### Node { #rules-node }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | The node keeps the type its Kubernetes share is for. | `refuse` | `facts.share == null || documents.all(n, documents[n].spec.type == facts.share.kind)` | after the table |
| 2 | `spec.labels` | The node keeps its labels once it holds a Kubernetes share. | `refuse` | `facts.share == null || self == oldSelf` | the config changes the node's labels; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; change the Node object with kubectl label instead, or reinstall the node |
| 3 | `spec.taints` | The node keeps its taints once it holds a Kubernetes share. | `refuse` | `facts.share == null || self == oldSelf` | the config changes the node's taints; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; change the Node object with kubectl taint instead, or reinstall the node |

Message of rule 1:

```text
cel.bind(type, documents.map(n, documents[n].spec.type)[0],
  "the config changes the node's type from \"" + facts.share.kind + "\" to \"" + type
  + "\"; a node keeps its type once it holds a Kubernetes share, so keep it, or reinstall the node as a " + type)
```

### Router images { #rules-router-images }

The rules of the kinds that router images accept, and those they add to other kinds.

#### AddressSet { #rules-addressset }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | An AddressSet's ranges are of its family. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
documents.all(n, documents[n].spec.prefixes.all(p, cidr(p).ip(0).family() == documents[n].spec.family))
```

Message of rule 1:

```text
documents.filter(n, !documents[n].spec.prefixes.all(p, cidr(p).ip(0).family() == documents[n].spec.family)).map(n, "AddressSet " + n + " holds ranges of another family than " + documents[n].spec.family).join("; ")
```

#### BGP { #rules-bgp }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | One BGP document serves a VRF. | `refuse` | after the table | after the table |
| 2 | — | A BGP document's vrf is a VRF document. | `refuse` | after the table | after the table |
| 3 | — | A BGP instance does not import its own VRF. | `refuse` | after the table | after the table |
| 4 | — | A delivery that drops the default VRF's BGP document keeps no running BGP instance of another VRF, as FRR keeps its default instance while another runs. | `refuse` | after the table | the config drops the default VRF's BGP document and keeps a running BGP instance of another VRF, which FRR does not allow; drop the other VRFs' BGP documents in the same delivery, or keep the default VRF's |
| 5 | `spec.asn` | BGP keeps its AS number. | `refuse` | `self == oldSelf` | after the table |
| 6 | `spec.import` | An import's VRFs have BGP documents. | `refuse` | after the table | after the table |
| 7 | `spec.import` | An import names each VRF once. | `refuse` | `[self.ipv4, self.ipv6].all(f, f == null || f.vrfs.all(v, size(f.vrfs.filter(w, w == v)) == 1))` | the import names a VRF twice |
| 8 | `spec.import` | An import's policy is a RoutingPolicy document. | `refuse` | after the table | after the table |
| 9 | `spec.import` | An import's policy matches PrefixLists of its family alone. | `refuse` | after the table | after the table |
| 10 | `spec.networks` | A network's prefix is an address range. | `refuse` | `self.all(w, cidr(w.prefix).prefix() >= 0)` | a network is no address range |
| 11 | `spec.networks` | A network's policy is a RoutingPolicy document. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
cel.bind(v, documents.map(n, (documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf)), v.all(x, size(v.filter(y, y == x)) == 1))
```

Message of rule 1:

```text
cel.bind(v, documents.map(n, (documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf)), "more than one BGP document serves the VRF " + v.filter(x, size(v.filter(y, y == x)) > 1)[0] + "; a VRF has one BGP instance")
```

Expression of rule 2:

```text
cel.bind(vrfs, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), documents.all(n, documents[n].spec.vrf == null || documents[n].spec.vrf in vrfs))
```

Message of rule 2:

```text
cel.bind(vrfs, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), documents.filter(n, documents[n].spec.vrf != null && !(documents[n].spec.vrf in vrfs)).map(n, "BGP " + n + " names the VRF " + documents[n].spec.vrf).join(", ") + ", which no VRF document declares; leave vrf out for the default VRF")
```

Expression of rule 3:

```text
documents.all(n, cel.bind(s, documents[n].spec, [s["import"].ipv4, s["import"].ipv6].all(f, f == null || !((documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf) in f.vrfs))))
```

Message of rule 3:

```text
documents.filter(n, cel.bind(s, documents[n].spec, ![s["import"].ipv4, s["import"].ipv6].all(f, f == null || !((documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf) in f.vrfs)))).map(n, "BGP " + n).join(", ") + " imports the routes of its own VRF; name other VRFs"
```

Expression of rule 4:

```text
!oldDocuments.exists(n, (has(oldDocuments[n].spec.vrf) ? oldDocuments[n].spec.vrf : null) == null) || documents.exists(n, documents[n].spec.vrf == null) || !documents.exists(n, documents[n].spec.vrf != null && oldDocuments.exists(m, (has(oldDocuments[m].spec.vrf) ? oldDocuments[m].spec.vrf : null) == documents[n].spec.vrf))
```

Message of rule 5:

```text
"the AS number stays " + string(oldSelf) + "; to change it, deliver the config without the BGP document, which ends the router's sessions, and then with the new AS number"
```

Expression of rule 6:

```text
cel.bind(have, cel.bind(bgps, (("router.chalkos.dev/v1alpha1" in config && "BGP" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].BGP : {}), bgps.map(n, (bgps[n].spec.vrf == null ? "default" : bgps[n].spec.vrf))), [self.ipv4, self.ipv6].all(f, f == null || f.vrfs.all(v, v in have)))
```

Message of rule 6:

```text
cel.bind(have, cel.bind(bgps, (("router.chalkos.dev/v1alpha1" in config && "BGP" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].BGP : {}), bgps.map(n, (bgps[n].spec.vrf == null ? "default" : bgps[n].spec.vrf))), "the import names the VRF " + [self.ipv4, self.ipv6].filter(f, f != null).map(f, f.vrfs.filter(v, !(v in have))).flatten()[0] + ", which no BGP document serves; leaking takes routes from BGP alone")
```

Expression of rule 8:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), [self.ipv4, self.ipv6].all(f, f == null || f.policy == null || f.policy in pols))
```

Message of rule 8:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), "an import names the RoutingPolicy " + [self.ipv4, self.ipv6].filter(f, f != null && f.policy != null && !(f.policy in pols))[0].policy + ", which the config does not hold")
```

Expression of rule 9:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), (self.ipv4 == null || self.ipv4.policy == null || (!(self.ipv4.policy in pols) || cel.bind(named, pols[self.ipv4.policy].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4"))) && (self.ipv6 == null || self.ipv6.policy == null || (!(self.ipv6.policy in pols) || cel.bind(named, pols[self.ipv6.policy].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv6")))))
```

Message of rule 9:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), "the " + (self.ipv4 == null || self.ipv4.policy == null || (!(self.ipv4.policy in pols) || cel.bind(named, pols[self.ipv4.policy].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4")) ? "ipv6" : "ipv4") + " import's RoutingPolicy matches PrefixLists of another family"))
```

Expression of rule 11:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), self.all(w, w.policy == null || w.policy in pols))
```

Message of rule 11:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), "a network names the RoutingPolicy " + self.filter(w, w.policy != null && !(w.policy in pols))[0].policy + ", which the config does not hold")
```

#### BGPPeer { #rules-bgppeer }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A BGPPeer names exactly one of address, interface and interfaces. | `refuse` | after the table | after the table |
| 2 | — | A BGPPeer's vrf is a VRF document. | `refuse` | after the table | after the table |
| 3 | — | A BGPPeer's VRF has a BGP document. | `refuse` | after the table | after the table |
| 4 | — | A BGPPeer's interface is no link another VRF than the peer's holds; names compare as written. | `refuse` | after the table | after the table |
| 5 | `spec.address` | A BGPPeer's address is a unicast address. | `refuse` | after the table | `"the address " + self + " is no neighbour's: give a unicast address"` |
| 6 | `spec.description` | A BGPPeer's description holds 80 characters at most. | `refuse` | `self == null || size(self) <= 80` | the description holds more than 80 characters |
| 7 | `spec.families` | A BGPPeer exchanges the routes of one family at least. | `refuse` | `self.ipv4 != null || self.ipv6 != null` | the peer exchanges the routes of no family; give families.ipv4 or families.ipv6, an empty one accepting and sending every route |
| 8 | `spec.families` | A family's policies are RoutingPolicy documents. | `refuse` | after the table | after the table |
| 9 | `spec.families` | A family's policies match PrefixLists of that family alone. | `refuse` | after the table | after the table |
| 10 | `spec.interface` | A BGPPeer's interfaces are not named like addresses. | `refuse` | after the table | after the table |
| 11 | `spec.interfaces` | A BGPPeer's interfaces are not named like addresses. | `refuse` | after the table | after the table |
| 12 | `spec.password` | A BGPPeer's password names its Secret and key as credentials' names. | `refuse` | after the table | the Secret's name and key hold letters, digits, '.', '_' and '-' alone, as the names of frr.service's credentials do |

Expression of rule 1:

```text
documents.all(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces].filter(x, x != null).size() == 1))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(s, documents[n].spec, [s.address, s.interface, s.interfaces].filter(x, x != null).size() != 1)).map(n, "BGPPeer " + n).join(", ") + " names not exactly one of address, interface and interfaces; a peer is reached at an address, on an interface or on each interface of a list"
```

Expression of rule 2:

```text
cel.bind(vrfs, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), documents.all(n, documents[n].spec.vrf == null || documents[n].spec.vrf in vrfs))
```

Message of rule 2:

```text
cel.bind(vrfs, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), documents.filter(n, documents[n].spec.vrf != null && !(documents[n].spec.vrf in vrfs)).map(n, "BGPPeer " + n + " names the VRF " + documents[n].spec.vrf).join(", ") + ", which no VRF document declares; leave vrf out for the default VRF")
```

Expression of rule 3:

```text
cel.bind(have, cel.bind(bgps, (("router.chalkos.dev/v1alpha1" in config && "BGP" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].BGP : {}), bgps.map(n, (bgps[n].spec.vrf == null ? "default" : bgps[n].spec.vrf))), documents.all(n, (documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf) in have))
```

Message of rule 3:

```text
cel.bind(have, cel.bind(bgps, (("router.chalkos.dev/v1alpha1" in config && "BGP" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].BGP : {}), bgps.map(n, (bgps[n].spec.vrf == null ? "default" : bgps[n].spec.vrf))), documents.filter(n, !((documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf) in have)).map(n, "BGPPeer " + n + " runs in the VRF " + (documents[n].spec.vrf == null ? "default" : documents[n].spec.vrf)).join(", ") + ": a peer's VRF needs a BGP document, which gives the router its AS number there")
```

Expression of rule 4:

```text
cel.bind(held, cel.bind(v, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), v.map(n, v[n].spec.interfaces.map(i, [i, n])).flatten()), documents.all(n, cel.bind(s, documents[n].spec, (s.interface == null || held.all(h, h[0] != s.interface || h[1] == s.vrf)))))
```

Message of rule 4:

```text
cel.bind(held, cel.bind(v, (("router.chalkos.dev/v1alpha1" in config && "VRF" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].VRF : {}), v.map(n, v[n].spec.interfaces.map(i, [i, n])).flatten()), documents.filter(n, cel.bind(s, documents[n].spec, !(s.interface == null || held.all(h, h[0] != s.interface || h[1] == s.vrf)))).map(n, "BGPPeer " + n + " names the interface " + documents[n].spec.interface).join(", ") + ", which a VRF document puts in another VRF than the peer's; give the peer that VRF or the interface another")
```

Expression of rule 5:

```text
self == null || cel.bind(a, ip(self), a != ip("0.0.0.0") && a != ip("::") && !cidr("224.0.0.0/4").contains(a) && !cidr("ff00::/8").contains(a))
```

Expression of rule 8:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), (self.ipv4 == null || self.ipv4["import"] == null || self.ipv4["import"] in pols) && (self.ipv4 == null || self.ipv4["export"] == null || self.ipv4["export"] in pols) && (self.ipv6 == null || self.ipv6["import"] == null || self.ipv6["import"] in pols) && (self.ipv6 == null || self.ipv6["export"] == null || self.ipv6["export"] in pols))
```

Message of rule 8:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), cel.bind(u, [(self.ipv4 == null || self.ipv4["import"] == null || self.ipv4["import"] in pols ? [] : [self.ipv4["import"], "ipv4"]), (self.ipv4 == null || self.ipv4["export"] == null || self.ipv4["export"] in pols ? [] : [self.ipv4["export"], "ipv4"]), (self.ipv6 == null || self.ipv6["import"] == null || self.ipv6["import"] in pols ? [] : [self.ipv6["import"], "ipv6"]), (self.ipv6 == null || self.ipv6["export"] == null || self.ipv6["export"] in pols ? [] : [self.ipv6["export"], "ipv6"])].filter(u, size(u) > 0)[0], "the " + u[1] + " family names the RoutingPolicy " + u[0] + ", which the config does not hold"))
```

Expression of rule 9:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), (self.ipv4 == null || self.ipv4["import"] == null || (!(self.ipv4["import"] in pols) || cel.bind(named, pols[self.ipv4["import"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4"))) && (self.ipv4 == null || self.ipv4["export"] == null || (!(self.ipv4["export"] in pols) || cel.bind(named, pols[self.ipv4["export"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4"))) && (self.ipv6 == null || self.ipv6["import"] == null || (!(self.ipv6["import"] in pols) || cel.bind(named, pols[self.ipv6["import"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv6"))) && (self.ipv6 == null || self.ipv6["export"] == null || (!(self.ipv6["export"] in pols) || cel.bind(named, pols[self.ipv6["export"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv6")))))
```

Message of rule 9:

```text
cel.bind(pols, (("router.chalkos.dev/v1alpha1" in config && "RoutingPolicy" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].RoutingPolicy : {}), cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), cel.bind(u, [(self.ipv4 == null || self.ipv4["import"] == null || (!(self.ipv4["import"] in pols) || cel.bind(named, pols[self.ipv4["import"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4")) ? [] : [self.ipv4["import"], "ipv4"]), (self.ipv4 == null || self.ipv4["export"] == null || (!(self.ipv4["export"] in pols) || cel.bind(named, pols[self.ipv4["export"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv4")) ? [] : [self.ipv4["export"], "ipv4"]), (self.ipv6 == null || self.ipv6["import"] == null || (!(self.ipv6["import"] in pols) || cel.bind(named, pols[self.ipv6["import"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv6")) ? [] : [self.ipv6["import"], "ipv6"]), (self.ipv6 == null || self.ipv6["export"] == null || (!(self.ipv6["export"] in pols) || cel.bind(named, pols[self.ipv6["export"]].spec.terms.filter(t, t.match.prefixList != null && t.match.prefixList in lists), size(named) == 0 || lists[named[0].match.prefixList].spec.family == "ipv6")) ? [] : [self.ipv6["export"], "ipv6"])].filter(u, size(u) > 0)[0], "the " + u[1] + " family's RoutingPolicy " + u[0] + " matches a PrefixList of another family")))
```

Expression of rule 10:

```text
self == null || [self].all(i, !i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$|^[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*$"))
```

Message of rule 10:

```text
"the interface " + [self].filter(i, i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$|^[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*$"))[0] + " is named like an address, which FRR cannot tell from a neighbour's; give the peer its address, or rename the interface"
```

Expression of rule 11:

```text
self == null || self.all(i, !i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$|^[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*$"))
```

Message of rule 11:

```text
"the interface " + self.filter(i, i.matches("^([0-9]{1,3}[.]){3}[0-9]{1,3}$|^[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*$"))[0] + " is named like an address, which FRR cannot tell from a neighbour's; give the peer its address, or rename the interface"
```

Expression of rule 12:

```text
self == null || self.secretRef.name.matches("^[A-Za-z0-9._-]+$") && self.secretRef.key.matches("^[A-Za-z0-9._-]+$")
```

#### CommunityList { #rules-communitylist }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A CommunityList holds standard or large communities, not both. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
documents.all(n, cel.bind(c, documents[n].spec.communities, cel.bind(large, c[0].indexOf(":") != c[0].lastIndexOf(":"), c.all(x, (x.indexOf(":") != x.lastIndexOf(":")) == large))))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(c, documents[n].spec.communities, cel.bind(large, c[0].indexOf(":") != c[0].lastIndexOf(":"), !c.all(x, (x.indexOf(":") != x.lastIndexOf(":")) == large)))).map(n, "CommunityList " + n).join(", ") + " holds standard and large communities; give each kind a list of its own"
```

#### FirewallPolicy { #rules-firewallpolicy }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A FirewallPolicy comes from a FirewallZone, not from local. | `refuse` | `documents.all(n, documents[n].spec.from != "local")` | after the table |
| 2 | — | A FirewallPolicy's zones are FirewallZone documents, or local for its to. | `refuse` | after the table | after the table |
| 3 | — | One FirewallPolicy at most goes from a zone to another. | `refuse` | after the table | after the table |
| 4 | `spec.rules` | A set:&lt;name> is the one entry of its source or destination. | `refuse` | after the table | a rule's source or destination holds set:&lt;name> beside other entries; a set stands alone, so give the other ranges a rule of their own or add them to the set |
| 5 | `spec.rules` | A rule's sets are AddressSet documents. | `refuse` | after the table | a rule names an AddressSet that the config does not hold |
| 6 | `spec.rules` | A rule with a family has addresses and sets of that family alone. | `refuse` | after the table | a rule has addresses or a set of another family than its own |
| 7 | `spec.rules` | A rule matches IPv4 or IPv6: its family, protocol, addresses and sets leave one. | `refuse` | after the table | a rule matches no connection: its family, protocol, addresses and sets leave neither IPv4 nor IPv6 |
| 8 | `spec.rules` | Ports go with tcp, udp or sctp, ICMP types with icmp or icmpv6, and packet-too-big with icmpv6. | `refuse` | after the table | a rule has ports without the protocol tcp, udp or sctp, or ICMP types its protocol lacks |

Message of rule 1:

```text
documents.filter(n, documents[n].spec.from == "local").map(n, "FirewallPolicy " + n).join(", ") + " comes from local, the router itself, whose own connections the firewall accepts; a policy comes from a FirewallZone"
```

Expression of rule 2:

```text
cel.bind(zones, (("router.chalkos.dev/v1alpha1" in config && "FirewallZone" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].FirewallZone : {}), documents.all(n, (documents[n].spec.from == "local" || documents[n].spec.from in zones) && (documents[n].spec.to == "local" || documents[n].spec.to in zones)))
```

Message of rule 2:

```text
cel.bind(zones, (("router.chalkos.dev/v1alpha1" in config && "FirewallZone" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].FirewallZone : {}), documents.filter(n, !((documents[n].spec.from == "local" || documents[n].spec.from in zones) && (documents[n].spec.to == "local" || documents[n].spec.to in zones))).map(n, "FirewallPolicy " + n + " names the zone " + (documents[n].spec.from == "local" || documents[n].spec.from in zones ? documents[n].spec.to : documents[n].spec.from) + ", which no FirewallZone document declares").join("; "))
```

Expression of rule 3:

```text
cel.bind(pairs, documents.map(n, documents[n].spec.from + " to " + documents[n].spec.to), pairs.all(p, size(pairs.filter(q, q == p)) == 1))
```

Message of rule 3:

```text
cel.bind(pairs, documents.map(n, documents[n].spec.from + " to " + documents[n].spec.to), "more than one FirewallPolicy goes from " + pairs.filter(p, size(pairs.filter(q, q == p)) > 1)[0] + "; give each pair of zones one policy")
```

Expression of rule 4:

```text
cel.bind(sets, (("router.chalkos.dev/v1alpha1" in config && "AddressSet" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].AddressSet : {}), self.all(r, cel.bind(src, (size(r.source) == 1 && r.source[0].startsWith("set:") ? r.source[0].substring(4) : ""), cel.bind(dst, (size(r.destination) == 1 && r.destination[0].startsWith("set:") ? r.destination[0].substring(4) : ""), [r.source, r.destination].all(l, size(l) == 1 || !l.exists(a, a.startsWith("set:")))))))
```

Expression of rule 5:

```text
cel.bind(sets, (("router.chalkos.dev/v1alpha1" in config && "AddressSet" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].AddressSet : {}), self.all(r, cel.bind(src, (size(r.source) == 1 && r.source[0].startsWith("set:") ? r.source[0].substring(4) : ""), cel.bind(dst, (size(r.destination) == 1 && r.destination[0].startsWith("set:") ? r.destination[0].substring(4) : ""), (src == "" || src in sets) && (dst == "" || dst in sets)))))
```

Expression of rule 6:

```text
cel.bind(sets, (("router.chalkos.dev/v1alpha1" in config && "AddressSet" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].AddressSet : {}), self.all(r, cel.bind(src, (size(r.source) == 1 && r.source[0].startsWith("set:") ? r.source[0].substring(4) : ""), cel.bind(dst, (size(r.destination) == 1 && r.destination[0].startsWith("set:") ? r.destination[0].substring(4) : ""), r.family == null || (src != "" ? !(src in sets) || sets[src].spec.family == r.family : r.source.all(a, a.startsWith("set:") || cidr(a).ip(0).family() == r.family)) && (dst != "" ? !(dst in sets) || sets[dst].spec.family == r.family : r.destination.all(a, a.startsWith("set:") || cidr(a).ip(0).family() == r.family))))))
```

Expression of rule 7:

```text
cel.bind(sets, (("router.chalkos.dev/v1alpha1" in config && "AddressSet" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].AddressSet : {}), self.all(r, cel.bind(src, (size(r.source) == 1 && r.source[0].startsWith("set:") ? r.source[0].substring(4) : ""), cel.bind(dst, (size(r.destination) == 1 && r.destination[0].startsWith("set:") ? r.destination[0].substring(4) : ""), ((r.family == null || r.family == "ipv4") && r.protocol != "icmpv6" && (size(r.source) == 0 || (src != "" ? !(src in sets) || sets[src].spec.family == "ipv4" :r.source.exists(a, !a.startsWith("set:") && cidr(a).ip(0).family() == "ipv4"))) && (size(r.destination) == 0 || (dst != "" ? !(dst in sets) || sets[dst].spec.family == "ipv4" :r.destination.exists(a, !a.startsWith("set:") && cidr(a).ip(0).family() == "ipv4")))) || ((r.family == null || r.family == "ipv6") && r.protocol != "icmp" && (size(r.source) == 0 || (src != "" ? !(src in sets) || sets[src].spec.family == "ipv6" :r.source.exists(a, !a.startsWith("set:") && cidr(a).ip(0).family() == "ipv6"))) && (size(r.destination) == 0 || (dst != "" ? !(dst in sets) || sets[dst].spec.family == "ipv6" :r.destination.exists(a, !a.startsWith("set:") && cidr(a).ip(0).family() == "ipv6"))))))))
```

Expression of rule 8:

```text
cel.bind(sets, (("router.chalkos.dev/v1alpha1" in config && "AddressSet" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].AddressSet : {}), self.all(r, cel.bind(src, (size(r.source) == 1 && r.source[0].startsWith("set:") ? r.source[0].substring(4) : ""), cel.bind(dst, (size(r.destination) == 1 && r.destination[0].startsWith("set:") ? r.destination[0].substring(4) : ""), (size(r.sourcePorts) == 0 && size(r.destinationPorts) == 0 || r.protocol in ["tcp", "udp", "sctp"]) && (size(r.icmpTypes) == 0 || r.protocol in ["icmp", "icmpv6"]) && !(r.protocol == "icmp" && "packet-too-big" in r.icmpTypes)))))
```

#### FirewallZone { #rules-firewallzone }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | No FirewallZone is named local, the router itself. | `refuse` | `!("local" in documents)` | a FirewallZone is named local, which is the router itself: FirewallPolicy documents name it without a document, so give the zone another name |
| 2 | — | An interface or glob is in one FirewallZone, once; globs compare as written. | `refuse` | after the table | after the table |

Expression of rule 2:

```text
cel.bind(all, documents.map(n, documents[n].spec.interfaces).flatten(), all.all(i, size(all.filter(j, j == i)) == 1))
```

Message of rule 2:

```text
cel.bind(all, documents.map(n, documents[n].spec.interfaces).flatten(), "the interface " + all.filter(i, size(all.filter(j, j == i)) > 1)[0] + " is in more than one FirewallZone, or twice in one; give each interface one zone")
```

#### NAT64 { #rules-nat64 }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A NAT64's name has 15 characters at most, as Jool's instance names do. | `refuse` | `documents.all(n, size(n) <= 15)` | after the table |
| 2 | — | No two NAT64s translate addresses of one prefix. | `refuse` | after the table | after the table |
| 3 | — | One NAT64 at most has an empty pool4, which takes the router's own addresses. | `refuse` | `size(documents.filter(n, size(documents[n].spec.pool4) == 0)) <= 1` | after the table |
| 4 | — | No two NAT64s share an address of their pools. | `refuse` | after the table | after the table |
| 5 | `spec.pool4` | A pool's entries are IPv4 ranges of 65536 addresses at most. | `refuse` | `self.all(a, cidr(a.prefix).ip(0).family() == "ipv4" && cidr(a.prefix).prefix() >= 16)` | after the table |
| 6 | `spec.pool4` | A pool's entries hold no unspecified, loopback, multicast or broadcast address. | `refuse` | after the table | after the table |
| 7 | `spec.pool4` | A pool entry's ports lie from 1 to 65535 and leave out the kernel's ephemeral ports, 32768 to 60999. | `refuse` | after the table | after the table |
| 8 | `spec.pool4` | A pool entry names each protocol once. | `refuse` | `self.all(a, ["tcp", "udp", "icmp"].all(p, size(a.protocols.filter(q, q == p)) <= 1))` | a pool entry names a protocol twice |
| 9 | `spec.pool4` | No two entries of a pool share a protocol, addresses and ports. | `refuse` | after the table | after the table |
| 10 | `spec.prefix` | A NAT64 prefix is an IPv6 range of 32, 40, 48, 56, 64 or 96 bits of no link-local, multicast or unspecified addresses, whose bits 64 to 71 are zero, as RFC 6052 gives them. | `refuse` | after the table | after the table |

Message of rule 1:

```text
documents.filter(n, size(n) > 15).map(n, "NAT64 " + n).join(", ") + " has a name of more than 15 characters, which Jool does not take as an instance's name; give it a shorter one"
```

Expression of rule 2:

```text
documents.all(n, documents.all(m, n == m || !(cidr(documents[n].spec.prefix).contains(cidr(documents[m].spec.prefix).ip(0)) || cidr(documents[m].spec.prefix).contains(cidr(documents[n].spec.prefix).ip(0)))))
```

Message of rule 2:

```text
documents.filter(n, documents.exists(m, n != m && (cidr(documents[n].spec.prefix).contains(cidr(documents[m].spec.prefix).ip(0)) || cidr(documents[m].spec.prefix).contains(cidr(documents[n].spec.prefix).ip(0))))).map(n, "NAT64 " + n).join(", ") + " translate addresses of one prefix; give each NAT64 a prefix of its own"
```

Message of rule 3:

```text
documents.filter(n, size(documents[n].spec.pool4) == 0).map(n, "NAT64 " + n).join(", ") + " take the router's own addresses, with an empty pool4, and would share them; give all but one a pool4"
```

Expression of rule 4:

```text
cel.bind(e, documents.map(n, documents[n].spec.pool4.map(a, [n, a.prefix])).flatten(), e.all(x, e.all(y, x[0] == y[0] || !(cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))))))
```

Message of rule 4:

```text
cel.bind(e, documents.map(n, documents[n].spec.pool4.map(a, [n, a.prefix])).flatten(), e.filter(x, e.exists(y, x[0] != y[0] && (cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))))).map(x, "NAT64 " + x[0] + " " + x[1]).join(", ") + " share addresses; give each NAT64 a pool of its own")
```

Message of rule 5:

```text
"the pool entry " + self.filter(a, !(cidr(a.prefix).ip(0).family() == "ipv4" && cidr(a.prefix).prefix() >= 16))[0].prefix + " is no IPv4 range of 16 bits or more"
```

Expression of rule 6:

```text
self.all(a, cel.bind(c, cidr(a.prefix), c.ip(0).family() != "ipv4" || c.ip(0) != ip("0.0.0.0") && !cidr("127.0.0.0/8").contains(c.ip(0)) && !cidr("224.0.0.0/4").contains(c.ip(0)) && !c.contains(ip("255.255.255.255"))))
```

Message of rule 6:

```text
"the pool entry " + self.filter(a, !cel.bind(c, cidr(a.prefix), c.ip(0).family() != "ipv4" || c.ip(0) != ip("0.0.0.0") && !cidr("127.0.0.0/8").contains(c.ip(0)) && !cidr("224.0.0.0/4").contains(c.ip(0)) && !c.contains(ip("255.255.255.255"))))[0].prefix + " holds addresses no connection leaves from: the unspecified address, loopback, multicast or broadcast"
```

Expression of rule 7:

```text
self.all(a, cel.bind(lo, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[0])), cel.bind(hi, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[1])), lo >= 1 && lo <= hi && hi <= 65535 && (hi < 32768 || lo > 60999))))
```

Message of rule 7:

```text
"a pool entry takes the ports " + string(self.filter(a, !cel.bind(lo, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[0])), cel.bind(hi, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[1])), lo >= 1 && lo <= hi && hi <= 65535 && (hi < 32768 || lo > 60999))))[0].ports) + ", which lie outside 1-32767 and 61000-65535 or run backwards; Jool refuses the kernel's ephemeral ports, 32768 to 60999"
```

Expression of rule 9:

```text
cel.bind(e, self.map(a, ["tcp", "udp", "icmp"].filter(p, p in a.protocols).map(p, [p, a.prefix, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[0])), (type(a.ports) == int ? a.ports : int(a.ports.split("-")[1]))])).flatten(), e.all(x, size(e.filter(y, x[0] == y[0] && (cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))) && x[2] <= y[3] && y[2] <= x[3])) <= 1))
```

Message of rule 9:

```text
cel.bind(e, self.map(a, ["tcp", "udp", "icmp"].filter(p, p in a.protocols).map(p, [p, a.prefix, (type(a.ports) == int ? a.ports : int(a.ports.split("-")[0])), (type(a.ports) == int ? a.ports : int(a.ports.split("-")[1]))])).flatten(), cel.bind(x, e.filter(x, size(e.filter(y, x[0] == y[0] && (cidr(x[1]).contains(cidr(y[1]).ip(0)) || cidr(y[1]).contains(cidr(x[1]).ip(0))) && x[2] <= y[3] && y[2] <= x[3])) > 1)[0], "the pool entry " + x[1] + " shares " + x[0] + " addresses and ports with another; give each entry of a protocol addresses or ports of its own"))
```

Expression of rule 10:

```text
cidr(self).ip(0).family() == "ipv6" && cidr(self).prefix() in [32, 40, 48, 56, 64, 96] && !cidr("::ffff:0:0/96").contains(cidr(self).ip(0)) && cel.bind(x, cidr(self).ip(0), x != ip("::") && !cidr("fe80::/10").contains(x) && !cidr("ff00::/8").contains(x)) && cel.bind(a, string(cidr(self).ip(0)), cidr(a + "/72").ip(0) == cidr(a + "/64").ip(0))
```

Message of rule 10:

```text
!(cidr(self).ip(0).family() == "ipv6" && cidr(self).prefix() in [32, 40, 48, 56, 64, 96] && !cidr("::ffff:0:0/96").contains(cidr(self).ip(0))) ? "the prefix " + self + " is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96" : !cel.bind(x, cidr(self).ip(0), x != ip("::") && !cidr("fe80::/10").contains(x) && !cidr("ff00::/8").contains(x)) ? "the prefix " + self + " holds link-local, multicast or unspecified addresses" : "the prefix " + self + " sets bits 64 to 71, which RFC 6052 keeps zero"
```

#### NATRule { #rules-natrule }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | masquerade and snat name out alone, dnat names in alone. | `refuse` | after the table | after the table |
| 2 | — | out and in name a zone or an interface, one of them. | `refuse` | after the table | after the table |
| 3 | — | A NATRule's zone is a FirewallZone document. | `refuse` | after the table | after the table |
| 4 | — | snat and dnat give to, masquerade does not. | `refuse` | `documents.all(n, (documents[n].spec.type == "masquerade") == (documents[n].spec.to == null))` | after the table |
| 5 | — | A NATRule's addresses are of its family. | `refuse` | after the table | after the table |
| 6 | — | Ports go with match.protocol. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
documents.all(n, cel.bind(s, documents[n].spec, s.type == "dnat" ? s["in"] != null && s.out == null : s.out != null && s["in"] == null))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(s, documents[n].spec, !(s.type == "dnat" ? s["in"] != null && s.out == null : s.out != null && s["in"] == null))).map(n, "NATRule " + n).join(", ") + ": a masquerade or snat rule names out alone, a dnat rule in alone"
```

Expression of rule 2:

```text
documents.all(n, [documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || (p.zone == null) != (p.interface == null)))
```

Message of rule 2:

```text
documents.filter(n, ![documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || (p.zone == null) != (p.interface == null))).map(n, "NATRule " + n).join(", ") + ": out and in name a zone or an interface, one of them"
```

Expression of rule 3:

```text
cel.bind(zones, (("router.chalkos.dev/v1alpha1" in config && "FirewallZone" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].FirewallZone : {}), documents.all(n, [documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || p.zone == null || p.zone in zones)))
```

Message of rule 3:

```text
cel.bind(zones, (("router.chalkos.dev/v1alpha1" in config && "FirewallZone" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].FirewallZone : {}), documents.filter(n, ![documents[n].spec.out, documents[n].spec["in"]].all(p, p == null || p.zone == null || p.zone in zones)).map(n, "NATRule " + n).join(", ") + " names a zone that no FirewallZone document declares")
```

Message of rule 4:

```text
documents.filter(n, (documents[n].spec.type == "masquerade") != (documents[n].spec.to == null)).map(n, "NATRule " + n).join(", ") + ": snat and dnat translate to the address of to, masquerade to that of the interface and has no to"
```

Expression of rule 5:

```text
documents.all(n, cel.bind(s, documents[n].spec, (s.match.source + s.match.destination).all(a, cidr(a).ip(0).family() == s.family) && (s.to == null || ip(s.to.address).family() == s.family)))
```

Message of rule 5:

```text
documents.filter(n, cel.bind(s, documents[n].spec, !((s.match.source + s.match.destination).all(a, cidr(a).ip(0).family() == s.family) && (s.to == null || ip(s.to.address).family() == s.family)))).map(n, "NATRule " + n + " has addresses of another family than " + documents[n].spec.family).join("; ")
```

Expression of rule 6:

```text
documents.all(n, cel.bind(s, documents[n].spec, s.match.protocol != null || size(s.match.destinationPorts) == 0 && (s.to == null || s.to.port == null)))
```

Message of rule 6:

```text
documents.filter(n, cel.bind(s, documents[n].spec, s.match.protocol == null && (size(s.match.destinationPorts) > 0 || s.to != null && s.to.port != null))).map(n, "NATRule " + n).join(", ") + ": ports need match.protocol"
```

#### PrefixList { #rules-prefixlist }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A PrefixList's entries are of its family, with ge and le from the prefix length to the family's longest prefix, ge no more than le. | `refuse` | after the table | after the table |
| 2 | `spec.entries` | A PrefixList's entries match apart: no two have one prefix, ge and le. | `refuse` | `self.all(e, self.exists_one(x, cidr(x.prefix) == cidr(e.prefix) && x.ge == e.ge && x.le == e.le))` | after the table |

Expression of rule 1:

```text
documents.all(n, cel.bind(s, documents[n].spec, s.entries.all(e, cel.bind(c, cidr(e.prefix), cel.bind(max, s.family == "ipv4" ? 32 : 128, c.ip(0).family() == s.family && (e.ge == null || e.ge >= c.prefix() && e.ge <= max) && (e.le == null || e.le >= c.prefix() && e.le <= max) && (e.ge == null || e.le == null || e.ge <= e.le))))))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(s, documents[n].spec, !s.entries.all(e, cel.bind(c, cidr(e.prefix), cel.bind(max, s.family == "ipv4" ? 32 : 128, c.ip(0).family() == s.family && (e.ge == null || e.ge >= c.prefix() && e.ge <= max) && (e.le == null || e.le >= c.prefix() && e.le <= max) && (e.ge == null || e.le == null || e.ge <= e.le)))))).map(n, "PrefixList " + n).join(", ") + " has an entry of another family, or a ge or le outside its range: both lie from the prefix length to 32 for IPv4 or 128 for IPv6, and ge no higher than le"
```

Message of rule 2:

```text
cel.bind(d, self.filter(e, !self.exists_one(x, cidr(x.prefix) == cidr(e.prefix) && x.ge == e.ge && x.le == e.le))[0], "two entries match " + string(cidr(d.prefix)) + (d.ge != null ? " ge " + string(d.ge) : "") + (d.le != null ? " le " + string(d.le) : "") + ", and the later never decides; drop one")
```

#### Resolver { #rules-resolver }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A Resolver listens on an address of a family its clients' ranges are of. | `refuse` | after the table | after the table |
| 2 | — | With DNS64, no local zone holds ipv4only.arpa, which Unbound would answer before DNS64. | `refuse` | after the table | after the table |
| 3 | `spec.allow` | The resolver's clients are address ranges. | `refuse` | `self.all(p, cidr(p).prefix() >= 0)` | a client is no address range |
| 4 | `spec.allow` | Each range appears once. | `refuse` | `self.all(a, size(self.filter(b, cidr(b) == cidr(a))) == 1)` | after the table |
| 5 | `spec.dns64` | A NAT64 prefix is an IPv6 range of 32, 40, 48, 56, 64 or 96 bits of no link-local, multicast or unspecified addresses, whose bits 64 to 71 are zero, as RFC 6052 gives them. | `refuse` | after the table | after the table |
| 6 | `spec.forwarders` | The resolver forwards to unicast addresses. | `refuse` | after the table | after the table |
| 7 | `spec.forwarders` | Each forwarder appears once. | `refuse` | `self.all(a, size(self.filter(b, ip(b) == ip(a))) == 1)` | after the table |
| 8 | `spec.listen` | The resolver listens on unicast addresses. | `refuse` | after the table | after the table |
| 9 | `spec.listen` | Each address appears once. | `refuse` | `self.all(a, size(self.filter(b, ip(b) == ip(a))) == 1)` | after the table |
| 10 | `spec.localZones` | A local zone appears once. | `refuse` | `self.all(z, size(self.filter(y, y.name == z.name)) == 1)` | after the table |
| 11 | `spec.localZones` | A local zone's records are of the zone's name or names below it. | `refuse` | `self.all(z, z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))` | after the table |
| 12 | `spec.localZones` | The names of local zones and their records hold 253 characters at most. | `refuse` | `self.all(z, size(z.name) <= 253) && self.map(z, z.records).flatten().all(r, size(r.name) <= 253)` | after the table |
| 13 | `spec.localZones` | An A or AAAA record holds an address of its family. | `refuse` | after the table | after the table |
| 14 | `spec.localZones` | The local zones hold 150 records at most, which the rules can compare in pairs. | `refuse` | `size(self.map(z, z.records).flatten()) <= 150` | after the table |
| 15 | `spec.localZones` | A record appears once, as Unbound keeps one of its copies. | `refuse` | after the table | after the table |
| 16 | `spec.localZones` | A redirect zone's records are of the zone's name, which Unbound answers for every name below it. | `refuse` | `self.all(z, z.type != "redirect" || z.records.all(r, r.name == z.name))` | after the table |
| 17 | `spec.localZones` | A CNAME record's name holds no other record. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
documents.all(n, cel.bind(s, documents[n].spec, ["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f))))
```

Message of rule 1:

```text
documents.filter(n, cel.bind(s, documents[n].spec, !["ipv4", "ipv6"].exists(f, s.listen.exists(a, ip(a).family() == f) && s.allow.exists(p, cidr(p).ip(0).family() == f)))).map(n, "Resolver " + n).join(", ") + " listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common"
```

Expression of rule 2:

```text
documents.all(n, cel.bind(s, documents[n].spec, s.dns64 == null || !s.localZones.exists(z, (z.name == "ipv4only.arpa" || z.name.endsWith(".ipv4only.arpa") || "ipv4only.arpa".endsWith("." + z.name)))))
```

Message of rule 2:

```text
documents.filter(n, cel.bind(s, documents[n].spec, s.dns64 != null && s.localZones.exists(z, (z.name == "ipv4only.arpa" || z.name.endsWith(".ipv4only.arpa") || "ipv4only.arpa".endsWith("." + z.name))))).map(n, "Resolver " + n).join(", ") + " has a local zone over ipv4only.arpa, which Unbound would answer before DNS64; drop the zone or DNS64"
```

Message of rule 4:

```text
"the range " + self.filter(a, size(self.filter(b, cidr(b) == cidr(a))) > 1)[0] + " appears more than once"
```

Expression of rule 5:

```text
self == null || (cidr(self.prefix).ip(0).family() == "ipv6" && cidr(self.prefix).prefix() in [32, 40, 48, 56, 64, 96] && !cidr("::ffff:0:0/96").contains(cidr(self.prefix).ip(0)) && cel.bind(x, cidr(self.prefix).ip(0), x != ip("::") && !cidr("fe80::/10").contains(x) && !cidr("ff00::/8").contains(x)) && cel.bind(a, string(cidr(self.prefix).ip(0)), cidr(a + "/72").ip(0) == cidr(a + "/64").ip(0)))
```

Message of rule 5:

```text
!(cidr(self.prefix).ip(0).family() == "ipv6" && cidr(self.prefix).prefix() in [32, 40, 48, 56, 64, 96] && !cidr("::ffff:0:0/96").contains(cidr(self.prefix).ip(0))) ? "the prefix " + self.prefix + " is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96" : !cel.bind(x, cidr(self.prefix).ip(0), x != ip("::") && !cidr("fe80::/10").contains(x) && !cidr("ff00::/8").contains(x)) ? "the prefix " + self.prefix + " holds link-local, multicast or unspecified addresses" : "the prefix " + self.prefix + " sets bits 64 to 71, which RFC 6052 keeps zero"
```

Expression of rule 6:

```text
self.all(a, cel.bind(x, ip(a), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x)))
```

Message of rule 6:

```text
"the forwarder " + self.filter(a, !cel.bind(x, ip(a), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x)))[0] + " is no unicast address"
```

Message of rule 7:

```text
"the forwarder " + self.filter(a, size(self.filter(b, ip(b) == ip(a))) > 1)[0] + " appears more than once"
```

Expression of rule 8:

```text
self.all(a, cel.bind(x, ip(a), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x)))
```

Message of rule 8:

```text
"the address " + self.filter(a, !cel.bind(x, ip(a), x != ip("0.0.0.0") && x != ip("::") && !cidr("127.0.0.0/8").contains(x) && x != ip("::1") && !cidr("169.254.0.0/16").contains(x) && !cidr("fe80::/10").contains(x) && !cidr("224.0.0.0/4").contains(x) && !cidr("ff00::/8").contains(x) && !cidr("::ffff:0:0/96").contains(x)))[0] + " is no unicast address the resolver can listen on"
```

Message of rule 9:

```text
"the address " + self.filter(a, size(self.filter(b, ip(b) == ip(a))) > 1)[0] + " appears more than once"
```

Message of rule 10:

```text
"the local zone " + self.filter(z, size(self.filter(y, y.name == z.name)) > 1)[0].name + " appears more than once"
```

Message of rule 11:

```text
cel.bind(z, self.filter(z, !z.records.all(r, r.name == z.name || r.name.endsWith("." + z.name)))[0], "the local zone " + z.name + " holds a record of " + z.records.filter(r, !(r.name == z.name || r.name.endsWith("." + z.name)))[0].name + ", which is outside it")
```

Message of rule 12:

```text
"the name " + (self.map(z, z.name) + self.map(z, z.records).flatten().map(r, r.name)).filter(n, size(n) > 253)[0] + " holds more than 253 characters"
```

Expression of rule 13:

```text
self.map(z, z.records).flatten().all(r, (r.type == "A" ? ip(r.value).family() == "ipv4" : r.type != "AAAA" || cel.bind(x, ip(r.value), x.family() == "ipv6" && !cidr("::ffff:0:0/96").contains(x)) && !r.value.contains("%")))
```

Message of rule 13:

```text
cel.bind(r, self.map(z, z.records).flatten().filter(r, !(r.type == "A" ? ip(r.value).family() == "ipv4" : r.type != "AAAA" || cel.bind(x, ip(r.value), x.family() == "ipv6" && !cidr("::ffff:0:0/96").contains(x)) && !r.value.contains("%")))[0], "the " + r.type + " record of " + r.name + " holds " + r.value + ", which is no " + (r.type == "A" ? "IPv4" : "IPv6") + " address")
```

Message of rule 14:

```text
"the local zones hold " + string(size(self.map(z, z.records).flatten())) + " records, more than the 150 whose duplicates chalkd can check"
```

Expression of rule 15:

```text
cel.bind(e, self.map(z, z.records).flatten(), size(e) > 150 || e.all(x, size(e.filter(y, x.name == y.name && x.type == y.type && (x.type == "AAAA" ? ip(x.value) == ip(y.value) : x.value == y.value))) == 1))
```

Message of rule 15:

```text
cel.bind(e, self.map(z, z.records).flatten(), size(e) > 50 ? "a record appears more than once in the local zones, with one name, type and value; Unbound keeps one" : cel.bind(r, e.filter(x, size(e.filter(y, x.name == y.name && x.type == y.type && (x.type == "AAAA" ? ip(x.value) == ip(y.value) : x.value == y.value))) > 1)[0], "the record " + r.name + " " + r.type + " " + (r.type == "AAAA" ? string(ip(r.value)) : r.value) + " appears more than once; Unbound keeps one"))
```

Message of rule 16:

```text
cel.bind(z, self.filter(z, z.type == "redirect" && !z.records.all(r, r.name == z.name))[0], "the redirect zone " + z.name + " holds a record of " + z.records.filter(r, r.name != z.name)[0].name + "; a redirect zone's records are of its own name, which Unbound answers for every name below it")
```

Expression of rule 17:

```text
cel.bind(e, self.map(z, z.records).flatten(), size(e) > 150 || e.all(x, x.type != "CNAME" || size(e.filter(y, y.name == x.name)) == 1))
```

Message of rule 17:

```text
cel.bind(e, self.map(z, z.records).flatten(), e.filter(x, x.type == "CNAME" && size(e.filter(y, y.name == x.name)) > 1)[0].name + " holds a CNAME record and another record; a CNAME's name holds it alone")
```

#### Router { #rules-router }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | `spec.management.interfaces` | A delivery that keeps the management interfaces keeps a Network naming each management interface that a running Network names, in a [Match] section's Name=; a glob names every interface its prefix starts, so with `*` a delivery keeps one Network at least. | `refuse` | after the table | after the table |

Expression of rule 1:

```text
self != oldSelf || oldSelf == null || cel.bind(was, cel.bind(nets, (("networkd.chalkos.dev/v1alpha1" in oldConfig && "Network" in oldConfig["networkd.chalkos.dev/v1alpha1"]) ? oldConfig["networkd.chalkos.dev/v1alpha1"].Network : {}), cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? (type(nets[n].spec.match) == list ? nets[n].spec.match : [nets[n].spec.match]) : []).flatten()), " " + dyn(matches.map(m, "name" in m ? (type(m.name) == list ? m.name : [m.name]) : []).flatten()).join(" ").replace("\t", " ") + " ")), cel.bind(gets, cel.bind(nets, (("networkd.chalkos.dev/v1alpha1" in config && "Network" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].Network : {}), cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? (type(nets[n].spec.match) == list ? nets[n].spec.match : [nets[n].spec.match]) : []).flatten()), " " + dyn(matches.map(m, "name" in m ? (type(m.name) == list ? m.name : [m.name]) : []).flatten()).join(" ").replace("\t", " ") + " ")), cel.bind(lost, oldSelf.filter(i, cel.bind(globs, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15].filter(k, k < size(i) || k == size(i) && !i.endsWith("*")).map(k, i.substring(0, k).replace(".", "[.]")).join("|"), (was.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? was.contains(" " + i.substring(0, size(i) - 1)) : was.trim() != "") || was.matches(" (" + globs + ")[*] ")) && !(gets.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? gets.contains(" " + i.substring(0, size(i) - 1)) : gets.trim() != "") || gets.matches(" (" + globs + ")[*] ")))), size(lost) == 0)))
```

Message of rule 1:

```text
cel.bind(was, cel.bind(nets, (("networkd.chalkos.dev/v1alpha1" in oldConfig && "Network" in oldConfig["networkd.chalkos.dev/v1alpha1"]) ? oldConfig["networkd.chalkos.dev/v1alpha1"].Network : {}), cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? (type(nets[n].spec.match) == list ? nets[n].spec.match : [nets[n].spec.match]) : []).flatten()), " " + dyn(matches.map(m, "name" in m ? (type(m.name) == list ? m.name : [m.name]) : []).flatten()).join(" ").replace("\t", " ") + " ")), cel.bind(gets, cel.bind(nets, (("networkd.chalkos.dev/v1alpha1" in config && "Network" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].Network : {}), cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? (type(nets[n].spec.match) == list ? nets[n].spec.match : [nets[n].spec.match]) : []).flatten()), " " + dyn(matches.map(m, "name" in m ? (type(m.name) == list ? m.name : [m.name]) : []).flatten()).join(" ").replace("\t", " ") + " ")), cel.bind(lost, oldSelf.filter(i, cel.bind(globs, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15].filter(k, k < size(i) || k == size(i) && !i.endsWith("*")).map(k, i.substring(0, k).replace(".", "[.]")).join("|"), (was.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? was.contains(" " + i.substring(0, size(i) - 1)) : was.trim() != "") || was.matches(" (" + globs + ")[*] ")) && !(gets.contains(" " + i + " ") || i.endsWith("*") && (size(i) > 1 ? gets.contains(" " + i.substring(0, size(i) - 1)) : gets.trim() != "") || gets.matches(" (" + globs + ")[*] ")))), "the config leaves the management interfaces " + lost.join(", ") + " without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery")))
```

#### RoutingPolicy { #rules-routingpolicy }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | `spec.terms` | A term's lists are PrefixList and CommunityList documents. | `refuse` | after the table | after the table |
| 2 | `spec.terms` | A term's asPath holds 200 characters at most. | `refuse` | `self.all(t, t.match.asPath == null || size(t.match.asPath) <= 200)` | a term's asPath holds more than 200 characters |
| 3 | `spec.terms` | A RoutingPolicy's PrefixLists are of one family. | `refuse` | after the table | the terms match PrefixLists of both families; a policy serves the routes of one family, so give each family a policy of its own |
| 4 | `spec.terms` | A term that rejects sets nothing. | `refuse` | after the table | a term that rejects routes sets local preference, MED, communities or a prepend, which no route it rejects keeps; drop them or accept |

Expression of rule 1:

```text
cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), cel.bind(clists, (("router.chalkos.dev/v1alpha1" in config && "CommunityList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].CommunityList : {}), self.all(t, (t.match.prefixList == null || t.match.prefixList in lists) && (t.match.communityList == null || t.match.communityList in clists))))
```

Message of rule 1:

```text
cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), cel.bind(clists, (("router.chalkos.dev/v1alpha1" in config && "CommunityList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].CommunityList : {}), cel.bind(m, self.filter(t, !((t.match.prefixList == null || t.match.prefixList in lists) && (t.match.communityList == null || t.match.communityList in clists)))[0].match, m.prefixList != null && !(m.prefixList in lists) ? "a term names the PrefixList " + m.prefixList + ", which the config does not hold" : "a term names the CommunityList " + m.communityList + ", which the config does not hold")))
```

Expression of rule 3:

```text
cel.bind(lists, (("router.chalkos.dev/v1alpha1" in config && "PrefixList" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].PrefixList : {}), cel.bind(families, self.filter(t, t.match.prefixList != null && t.match.prefixList in lists).map(t, lists[t.match.prefixList].spec.family), families.all(f, f == families[0])))
```

Expression of rule 4:

```text
self.all(t, t["then"].action == "accept" || t["then"].localPref == null && t["then"].med == null && size(t["then"].addCommunities) == 0 && size(t["then"].removeCommunities) == 0 && t["then"].prepend == null)
```

#### VRF { #rules-vrf }

| Rule | Field | Guards | Takes | Expression | Message |
| --- | --- | --- | --- | --- | --- |
| 1 | — | A VRF is named as its device, in 15 characters at most, and not default. | `refuse` | `documents.all(n, n != "default" && size(n) <= 15)` | after the table |
| 2 | — | A VRF's table is none of the kernel's own: 253, 254 and 255. | `refuse` | `documents.all(n, !(documents[n].spec.table in [253, 254, 255]))` | after the table |
| 3 | — | A table belongs to one VRF. | `refuse` | `cel.bind(t, documents.map(n, documents[n].spec.table), t.all(x, size(t.filter(y, y == x)) == 1))` | after the table |
| 4 | — | An interface or glob is in one VRF, once; globs compare as written. | `refuse` | after the table | after the table |
| 5 | — | No VRF holds an interface of Router.spec.management.interfaces; globs match as globs. | `refuse` | after the table | after the table |
| 6 | — | While the node API answers on every interface, a Network document names a link that no VRF holds; globs match as globs. | `refuse` | after the table | every link the Network documents name is in a VRF, and chalkd answers in the default VRF alone, on every interface while Router.spec.management.interfaces is null; keep the link you reach the router through out of every VRF |
| 7 | — | No NetDev document creates a device of a VRF's name. | `refuse` | after the table | after the table |

Message of rule 1:

```text
documents.filter(n, n == "default" || size(n) > 15).map(n, "VRF " + n).join(", ") + ": a VRF is named as its device, in 15 characters at most, and default names the VRF of every link no VRF holds"
```

Message of rule 2:

```text
documents.filter(n, documents[n].spec.table in [253, 254, 255]).map(n, "VRF " + n).join(", ") + " uses table 253, 254 or 255, which the kernel keeps for the default VRF; give it another number"
```

Message of rule 3:

```text
cel.bind(t, documents.map(n, documents[n].spec.table), "more than one VRF uses table " + string(t.filter(x, size(t.filter(y, y == x)) > 1)[0]) + "; give each VRF a table of its own")
```

Expression of rule 4:

```text
cel.bind(all, documents.map(n, documents[n].spec.interfaces).flatten(), all.all(i, size(all.filter(j, j == i)) == 1))
```

Message of rule 4:

```text
cel.bind(all, documents.map(n, documents[n].spec.interfaces).flatten(), "the interface " + all.filter(i, size(all.filter(j, j == i)) > 1)[0] + " is in more than one VRF, or twice in one; give each interface one VRF")
```

Expression of rule 5:

```text
cel.bind(held, documents.map(n, documents[n].spec.interfaces).flatten(), cel.bind(api, cel.bind(routers, (("router.chalkos.dev/v1alpha1" in config && "Router" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].Router : {}), routers.map(k, routers[k].spec.management.interfaces == null ? [] : routers[k].spec.management.interfaces).flatten()), cel.bind(heldName, "^(" + held.map(i, i.replace(".", "[.]").replace("*", "[^ ]*")).join("|") + ")$", cel.bind(heldText, held.map(i, " " + i).join(""), api.all(i, !(i.matches(heldName) || i.endsWith("*") && heldText.contains(" " + i.replace("*", ""))))))))
```

Message of rule 5:

```text
cel.bind(held, documents.map(n, documents[n].spec.interfaces).flatten(), cel.bind(api, cel.bind(routers, (("router.chalkos.dev/v1alpha1" in config && "Router" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].Router : {}), routers.map(k, routers[k].spec.management.interfaces == null ? [] : routers[k].spec.management.interfaces).flatten()), cel.bind(heldName, "^(" + held.map(i, i.replace(".", "[.]").replace("*", "[^ ]*")).join("|") + ")$", cel.bind(heldText, held.map(i, " " + i).join(""), "the management interface " + api.filter(i, (i.matches(heldName) || i.endsWith("*") && heldText.contains(" " + i.replace("*", ""))))[0] + " is in a VRF, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"))))
```

Expression of rule 6:

```text
size(documents) == 0 || cel.bind(routers, (("router.chalkos.dev/v1alpha1" in config && "Router" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].Router : {}), routers.exists(k, routers[k].spec.management.interfaces != null)) || cel.bind(held, documents.map(n, documents[n].spec.interfaces).flatten(), !cel.bind(nets, (("networkd.chalkos.dev/v1alpha1" in config && "Network" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].Network : {}), cel.bind(matches, dyn(nets.map(n, "match" in nets[n].spec ? (type(nets[n].spec.match) == list ? nets[n].spec.match : [nets[n].spec.match]) : []).flatten()), " " + dyn(matches.map(m, "name" in m ? (type(m.name) == list ? m.name : [m.name]) : []).flatten()).join(" ").replace("\t", " ") + " ")).matches("^( +(" + held.map(i, i.replace(".", "[.]").replace("*", "[^ ]*")).join("|") + "|![^ ]*))* *$"))
```

Expression of rule 7:

```text
cel.bind(made, cel.bind(netdevs, (("networkd.chalkos.dev/v1alpha1" in config && "NetDev" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].NetDev : {}), netdevs.map(n, !has(netdevs[n].spec.netDev) ? [] : (type(netdevs[n].spec.netDev) == list ? netdevs[n].spec.netDev : [netdevs[n].spec.netDev]).map(s, has(s.name) ? s.name : "")).flatten()), documents.all(n, !(n in made)))
```

Message of rule 7:

```text
cel.bind(made, cel.bind(netdevs, (("networkd.chalkos.dev/v1alpha1" in config && "NetDev" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].NetDev : {}), netdevs.map(n, !has(netdevs[n].spec.netDev) ? [] : (type(netdevs[n].spec.netDev) == list ? netdevs[n].spec.netDev : [netdevs[n].spec.netDev]).map(s, has(s.name) ? s.name : "")).flatten()), "the VRF " + documents.filter(n, n in made)[0] + " takes the name of a device a NetDev document creates; rename the VRF or the NetDev")
```

## Facts { #facts }

A fact is node state that change rules and the `when` of effects read as `facts.<name>`. A
file fact is the JSON content of a file, and null while the file is missing; a command fact is
the JSON a program prints.

### Every image { #facts-every-image }

The facts every image declares.

| Fact | Reads | Read by |
| --- | --- | --- |
| `disks`{ #fact-disks } | What `chalkos-storage-facts disks` prints. | The rules of `Volume`. |
| `fallback`{ #fact-fallback } | What `chalkos-storage-facts fallback` prints, given the fallback secret. | The rules of `Volume`. |
| `recorded`{ #fact-recorded } | File `/state/storage/storage.json`. | The rules of `SystemDisk` and `Volume`. |

### Images with Kubernetes { #facts-kubernetes }

The facts images with Kubernetes add.

| Fact | Reads | Read by |
| --- | --- | --- |
| `cluster`{ #fact-cluster } | File `/run/chalkos/kubernetes/cluster.json`. | The effects `chalkd-cluster`, `chalkd-share` and `kubernetes`. |
| `share`{ #fact-share } | The key `kind` of the file `/state/kubernetes/share.json`, and the SHA-256 of each other key. | The rules of `Cluster` and `Node`, and the effects `chalkd-share` and `kubernetes`. |

### Router images { #facts-router-images }

None.

## Outputs { #outputs }

An output is a set of config files that chalkd renders from the node's documents at every
delivery and boot. [Render config files](../build/render-config.md) describes them. Each table
gives the kinds an output reads, the directory services read its files in, its renderer, and
whether a check runs on the rendered files before a delivery changes anything.

### Every image { #outputs-every-image }

The outputs every image renders.

| Output | Inputs | Directory | Renderer | Check |
| --- | --- | --- | --- | --- |
| `chrony`{ #output-chrony } | `TimeSync` | `/run/chalkos/outputs/chrony` | Go template `servers.sources.tmpl` | yes |
| `networkd`{ #output-networkd } | `Link`, `NetDev`, `Network` | `/run/systemd/network` | Command `chalkos-render networkd chalkos-networkd-schemas` | no |

### Images with Kubernetes { #outputs-kubernetes }

None.

### Router images { #outputs-router-images }

The outputs router images add.

| Output | Inputs | Directory | Renderer | Check |
| --- | --- | --- | --- | --- |
| `frr`{ #output-frr } | `BGP`, `BGPPeer`, `PrefixList`, `CommunityList`, `RoutingPolicy`, `Network`, `VRF` | `/run/chalkos/outputs/frr` | Command `chalkos-render frr` | yes |
| `jool`{ #output-jool } | `NAT64` | `/run/chalkos/outputs/jool` | Command `chalkos-render jool` | yes |
| `router-nftables`{ #output-router-nftables } | `Router`, `FirewallZone`, `FirewallPolicy`, `AddressSet`, `NATRule`, `BGPPeer`, `Resolver` | `/run/chalkos/outputs/router-nftables` | Command `chalkos-render router-nftables 50000` | yes |
| `router-sysctl`{ #output-router-sysctl } | `Router` | `/run/sysctl.d` | Command `chalkos-render router-sysctl` | no |
| `unbound`{ #output-unbound } | `Resolver` | `/run/chalkos/outputs/unbound` | Command `chalkos-render unbound root.key ipv4only.arpa.zone` | yes |

## Effects { #effects }

An effect is what a delivery does to the node's services once it is applied.
[React to changes](../build/react-to-changes.md) describes them and the order chalkd takes them
in. A `when` too long for its row follows the table.

### Every image { #effects-every-image }

The effects every image declares.

| Effect | Fires when | Does |
| --- | --- | --- |
| `chrony`{ #effect-chrony } | The output `chrony` changed. | Runs `chalkos-chrony-reload-sources`. |
| `chrony-dhcp`{ #effect-chrony-dhcp } | The `when` after the table holds, which reads the documents of `TimeSync`. | Restarts `chalkos-chrony-dhcp.service`. |
| `networkd`{ #effect-networkd } | The output `networkd` changed. | Reloads `systemd-networkd.service`. |

`when` of `chrony-dhcp`:

```text
cel.bind(d, (("time.chalkos.dev/v1alpha1" in config && "TimeSync" in config["time.chalkos.dev/v1alpha1"]) ? config["time.chalkos.dev/v1alpha1"].TimeSync : {}), d.map(n, d[n].spec)) != cel.bind(d, (("time.chalkos.dev/v1alpha1" in oldConfig && "TimeSync" in oldConfig["time.chalkos.dev/v1alpha1"]) ? oldConfig["time.chalkos.dev/v1alpha1"].TimeSync : {}), d.map(n, d[n].spec))
```

### Images with Kubernetes { #effects-kubernetes }

The effects images with Kubernetes add.

| Effect | Fires when | Does |
| --- | --- | --- |
| `chalkd-cluster`{ #effect-chalkd-cluster } | The `when` after the table holds, which reads the documents of `Cluster` and `Flannel` and the fact `cluster`. | Runs chalkd's action `kubernetes:restart`. |
| `chalkd-share`{ #effect-chalkd-share } | The `when` after the table holds, which reads the documents of `Addon`, `Cluster`, `Flannel` and `Node` and the facts `cluster` and `share`. | Runs chalkd's action `kubernetes:reload`. |
| `controlplane-ports`{ #effect-controlplane-ports } | The `when` after the table holds, which reads the documents of `Node`. | Restarts `chalkos-controlplane.service`. |
| `kubernetes`{ #effect-kubernetes } | The `when` after the table holds, which reads the documents of `Cluster`, `Flannel`, `Host`, `Network` and `Node` and the facts `cluster` and `share`. | Restarts `chalkos-kubernetes.service` and `kubelet.service`, and starts them when they are stopped. |
| `registry-credentials`{ #effect-registry-credentials } | The documents of `Registry`, or a value of a Secret they reference, changed. | Restarts `chalkos-registry-credentials.service` and `kubelet.service`. |
| `registry-mirrors`{ #effect-registry-mirrors } | The `when` after the table holds, which reads the documents of `Mirror` and `Registry`. | Restarts `chalkos-registry-mirrors.service`. |

`when` of `chalkd-cluster`:

```text
facts.cluster != oldFacts.cluster || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Cluster" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Cluster" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Flannel" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Flannel" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec))
```

`when` of `chalkd-share`:

```text
(cel.bind(was, oldFacts.share, cel.bind(gets, facts.share, was == null || gets == null ? was != gets : was.filter(k, k != "nodeCA").map(k, k + "=" + was[k]) != gets.filter(k, k != "nodeCA").map(k, k + "=" + gets[k]))) || (cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Node" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.exists(n, d[n].spec.type == "controlplane")) && cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Addon" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Addon : {}), d.map(n, d[n].spec)).map(s, s.objects).flatten() != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Addon" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Addon : {}), d.map(n, d[n].spec)).map(s, s.objects).flatten())) && !(facts.cluster != oldFacts.cluster || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Cluster" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Cluster" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Flannel" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Flannel" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)))
```

`when` of `controlplane-ports`:

```text
cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Node" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Node" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.map(n, d[n].spec))
```

`when` of `kubernetes`:

```text
cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Node" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Node" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.map(n, d[n].spec)) || cel.bind(d, (("chalkos.dev/v1alpha1" in config && "Host" in config["chalkos.dev/v1alpha1"]) ? config["chalkos.dev/v1alpha1"].Host : {}), d.map(n, d[n].spec)) != cel.bind(d, (("chalkos.dev/v1alpha1" in oldConfig && "Host" in oldConfig["chalkos.dev/v1alpha1"]) ? oldConfig["chalkos.dev/v1alpha1"].Host : {}), d.map(n, d[n].spec)) || cel.bind(d, (("networkd.chalkos.dev/v1alpha1" in config && "Network" in config["networkd.chalkos.dev/v1alpha1"]) ? config["networkd.chalkos.dev/v1alpha1"].Network : {}), d.map(n, has(d[n].spec.network) ? (type(d[n].spec.network) == list ? d[n].spec.network : [d[n].spec.network]).map(s, type(s) == map && has(s.address) ? s.address : null) : [])) != cel.bind(d, (("networkd.chalkos.dev/v1alpha1" in oldConfig && "Network" in oldConfig["networkd.chalkos.dev/v1alpha1"]) ? oldConfig["networkd.chalkos.dev/v1alpha1"].Network : {}), d.map(n, has(d[n].spec.network) ? (type(d[n].spec.network) == list ? d[n].spec.network : [d[n].spec.network]).map(s, type(s) == map && has(s.address) ? s.address : null) : [])) || facts.cluster != oldFacts.cluster || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Cluster" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Cluster" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Flannel" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Flannel" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)) || cel.bind(was, oldFacts.share, cel.bind(gets, facts.share, was == null || gets == null ? was != gets : was.filter(k, k != "nodeCA").map(k, k + "=" + was[k]) != gets.filter(k, k != "nodeCA").map(k, k + "=" + gets[k])))
```

`when` of `registry-mirrors`:

```text
cel.bind(d, (("oci.chalkos.dev/v1alpha1" in config && "Mirror" in config["oci.chalkos.dev/v1alpha1"]) ? config["oci.chalkos.dev/v1alpha1"].Mirror : {}), d.map(n, d[n].spec)) != cel.bind(d, (("oci.chalkos.dev/v1alpha1" in oldConfig && "Mirror" in oldConfig["oci.chalkos.dev/v1alpha1"]) ? oldConfig["oci.chalkos.dev/v1alpha1"].Mirror : {}), d.map(n, d[n].spec)) || cel.bind(d, (("oci.chalkos.dev/v1alpha1" in config && "Registry" in config["oci.chalkos.dev/v1alpha1"]) ? config["oci.chalkos.dev/v1alpha1"].Registry : {}), d.map(n, d[n].spec)) != cel.bind(d, (("oci.chalkos.dev/v1alpha1" in oldConfig && "Registry" in oldConfig["oci.chalkos.dev/v1alpha1"]) ? oldConfig["oci.chalkos.dev/v1alpha1"].Registry : {}), d.map(n, d[n].spec))
```

### Router images { #effects-router-images }

The effects router images add.

| Effect | Fires when | Does |
| --- | --- | --- |
| `frr`{ #effect-frr } | The output `frr` changed. | Reloads `frr.service`. |
| `jool`{ #effect-jool } | The output `jool` changed. | Reloads `chalkos-nat64.service`. |
| `router-nftables`{ #effect-router-nftables } | The output `router-nftables` changed. | Reloads `chalkos-firewall.service`. |
| `router-sysctl`{ #effect-router-sysctl } | The output `router-sysctl` changed. | Restarts `systemd-sysctl.service`. |
| `unbound`{ #effect-unbound } | The output `unbound` changed. | Reloads `unbound.service`. |
| `unbound-start`{ #effect-unbound-start } | The `when` after the table holds, which reads the documents of `Resolver`. | Restarts `unbound.service`, and starts them when they are stopped. |

`when` of `unbound-start`:

```text
size((("router.chalkos.dev/v1alpha1" in config && "Resolver" in config["router.chalkos.dev/v1alpha1"]) ? config["router.chalkos.dev/v1alpha1"].Resolver : {})) != size((("router.chalkos.dev/v1alpha1" in oldConfig && "Resolver" in oldConfig["router.chalkos.dev/v1alpha1"]) ? oldConfig["router.chalkos.dev/v1alpha1"].Resolver : {}))
```

## Diagnostics { #diagnostics }

A diagnostic is a read-only command of the image that chalkd runs on the node for a client,
in a transient systemd unit with a sandbox of its own, as
[`chalkctl node diag`](cli/chalkctl_node_diag.md) does. Each table gives what a diagnostic
shows and the command it runs, its parameters, the least client role that runs it and the
capabilities its run gets. A script of several lines follows the table.

### Every image { #diagnostics-every-image }

The diagnostics every image declares.

| Diagnostic | Does | Parameters | Role | Capabilities |
| --- | --- | --- | --- | --- |
| `addresses`{ #diagnostic-addresses } | The addresses of the node's interfaces, as ip -json address show prints them. Runs `ip -json address show`. | — | reader | — |
| `dns-query`{ #diagnostic-dns-query } | Looks up a name in DNS, through the node's resolver or the server given. Runs `drill {name} {type} @{server}`. | `name`, hostname; `type`, enum of `A`, `AAAA`, `CAA`, `CNAME`, `DS`, `DNSKEY`, `MX`, `NS`, `PTR`, `SOA`, `SRV` or `TXT`, optional; `server`, address, optional | operator | — |
| `links`{ #diagnostic-links } | The node's network interfaces and their state, as ip -json link show prints them. Runs `ip -json link show`. | — | reader | — |
| `neighbours`{ #diagnostic-neighbours } | The neighbour table of IPv4 ARP and IPv6 neighbour discovery, as ip -json neighbour show prints it. Runs `ip -json neighbour show`. | — | reader | — |
| `nft-ruleset`{ #diagnostic-nft-ruleset } | The node's nftables ruleset, as nft list ruleset prints it. Runs `nft list ruleset`. | — | operator | `CAP_NET_ADMIN` |
| `ping`{ #diagnostic-ping } | Sends ICMP echo requests to an address and prints the replies, for at most 10 seconds. Runs `ping -n -w 10 -c {count} {address}`. | `address`, address; `count`, int from 1 to 10, default `4` | operator | `CAP_NET_RAW` |
| `route-get`{ #diagnostic-route-get } | The route the kernel takes to an address. Runs `ip -json route get {address}`. | `address`, address | reader | — |
| `routes`{ #diagnostic-routes } | The routes of one family in one routing table, as ip -json route show prints them. Runs `ip -json -family {family} route show table {table}`. | `family`, enum of `inet` or `inet6`, default `inet`; `table`, pattern `^(main|local|default|all|[0-9]+)$`, default `main` | reader | — |
| `tcp-connect`{ #diagnostic-tcp-connect } | Opens a TCP connection to an address and port, closes it again and says whether it connected. Runs `bash -c <script> tcp-connect {address} {port}`, with the script after the table. | `address`, address; `port`, int from 1 to 65535 | operator | — |

Script of `tcp-connect`:

```text
if timeout 5 bash -c ': <>"/dev/tcp/$1/$2"' tcp-connect "$1" "$2"; then
  echo "connected to $1 port $2"
else
  status=$?
  if [ "$status" = 124 ]; then
    echo "no connection to $1 port $2 within 5 seconds" >&2
  fi
  exit "$status"
fi
```

### Images with Kubernetes { #diagnostics-kubernetes }

None.

### Router images { #diagnostics-router-images }

The diagnostics router images add.

| Diagnostic | Does | Parameters | Role | Capabilities |
| --- | --- | --- | --- | --- |
| `bfd-peers`{ #diagnostic-bfd-peers } | FRR's BFD sessions and their state, as show bfd peers prints them. Runs `vtysh -c show bfd peers`. | — | reader | — |
| `bgp-neighbor`{ #diagnostic-bgp-neighbor } | One BGP session in detail, as show bgp neighbors prints it: its state, timers, capabilities and routes. Runs `vtysh -c show bgp neighbors {peer}`. | `peer`, pattern `^([0-9A-Fa-f:.]+|[A-Za-z0-9_.:@-]{1,15})$` | reader | — |
| `bgp-route`{ #diagnostic-bgp-route } | BGP's paths to a prefix, as show bgp prints them: each path's AS path, next hop and attributes, and the best. Runs `vtysh -c show bgp {family} unicast {prefix}`. | `prefix`, prefix; `family`, enum of `ipv4` or `ipv6`, default `ipv6` | reader | — |
| `bgp-summary`{ #diagnostic-bgp-summary } | FRR's BGP sessions of each address family, as show bgp summary prints them. Runs `vtysh -c show bgp summary`. | — | reader | — |
| `nat64-sessions`{ #diagnostic-nat64-sessions } | A NAT64 instance's sessions, as jool session display prints them: each translated TCP, UDP and ICMP connection's IPv6 and IPv4 addresses and ports, and when it expires. Runs `bash -c <script> nat64-sessions {instance}`, with the script after the table. | `instance`, pattern `^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$` | operator | `CAP_NET_ADMIN` |
| `traceroute`{ #diagnostic-traceroute } | The routers on the path to an address, as traceroute -n prints them: one probe a hop, 30 hops at most. Runs `traceroute -n -m 30 -q 1 -w 2 {address}`. | `address`, address | operator | — |

Script of `nat64-sessions`:

```text
for protocol in --tcp --udp --icmp; do
  jool -i "$1" session display --numeric "$protocol" || exit
done
```

Sources: the schemas, `facts.json`, `outputs.json`, `effects.json` and `diagnostics.json` that
an image holds in `/etc/chalkos`, of an image of the default variant, of one with
`kubernetes = false` and of one with `router = true`, which the node modules in `modules/node/`
declare.
