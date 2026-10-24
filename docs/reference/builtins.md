---
title: "Built-ins"
description: "The change rules, facts, outputs and effects that chalkos's images declare"
---

# Built-ins

This page is for image authors and operators who want to know why a node refused a config, or
what a delivery reloads and restarts. It lists the change rules, facts, outputs and effects that
chalkos's own modules declare: those of every image, and those that images with Kubernetes add.
An extension declares more with the [Extension API](extension-api.md), and its outputs and
effects take names other than these.

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
| `cluster`{ #fact-cluster } | File `/run/chalkos/kubernetes/cluster.json`. | The effects `chalkd-cluster` and `kubernetes`. |
| `share`{ #fact-share } | The key `kind` of the file `/state/kubernetes/share.json`, and the SHA-256 of each other key. | The rules of `Cluster` and `Node`, and the effects `chalkd-share` and `kubernetes`. |

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
| `chalkd-cluster`{ #effect-chalkd-cluster } | The `when` after the table holds, which reads the documents of `Cluster` and `Flannel` and the fact `cluster`. | Restarts chalkd's Kubernetes loops. |
| `chalkd-share`{ #effect-chalkd-share } | The `when` after the table holds, which reads the documents of `Addon` and `Node` and the fact `share`. | Reloads chalkd's control-plane loop. |
| `controlplane-ports`{ #effect-controlplane-ports } | The `when` after the table holds, which reads the documents of `Node`. | Restarts `chalkos-controlplane.service`. |
| `kubernetes`{ #effect-kubernetes } | The `when` after the table holds, which reads the documents of `Cluster`, `Flannel`, `Host`, `Network` and `Node` and the facts `cluster` and `share`. | Restarts `chalkos-kubernetes.service` and `kubelet.service`, and starts them when they are stopped. |
| `registry-credentials`{ #effect-registry-credentials } | The documents of `Registry` changed. | Restarts `chalkos-registry-credentials.service` and `kubelet.service`. |
| `registry-mirrors`{ #effect-registry-mirrors } | The `when` after the table holds, which reads the documents of `Mirror` and `Registry`. | Restarts `chalkos-registry-mirrors.service`. |

`when` of `chalkd-cluster`:

```text
facts.cluster != oldFacts.cluster || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Cluster" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Cluster" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Cluster : {}), d.map(n, d[n].spec)) || cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Flannel" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec)) != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Flannel" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Flannel : {}), d.map(n, d[n].spec))
```

`when` of `chalkd-share`:

```text
cel.bind(was, oldFacts.share, cel.bind(gets, facts.share, was == null || gets == null ? was != gets : was.filter(k, k != "nodeCA").map(k, k + "=" + was[k]) != gets.filter(k, k != "nodeCA").map(k, k + "=" + gets[k]))) || (cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Node" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Node : {}), d.exists(n, d[n].spec.type == "controlplane")) && cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in config && "Addon" in config["kubernetes.chalkos.dev/v1alpha1"]) ? config["kubernetes.chalkos.dev/v1alpha1"].Addon : {}), d.map(n, d[n].spec)).map(s, s.objects).flatten() != cel.bind(d, (("kubernetes.chalkos.dev/v1alpha1" in oldConfig && "Addon" in oldConfig["kubernetes.chalkos.dev/v1alpha1"]) ? oldConfig["kubernetes.chalkos.dev/v1alpha1"].Addon : {}), d.map(n, d[n].spec)).map(s, s.objects).flatten())
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

Sources: the schemas, `facts.json`, `outputs.json` and `effects.json` that an image holds in
`/etc/chalkos`, of an image of the default variant and of one with `kubernetes = false`, which
the node modules in `modules/node/` declare.
