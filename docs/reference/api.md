---
title: "Node API"
description: "The API chalkd serves on every node, generated from its protobuf definition"
---

# Node API

chalkd serves this API on every node on TCP port 50000 over TLS, as a Connect service that also
answers gRPC and gRPC-Web; chalkctl is its client. This page is generated from the protobuf
definition with protoc-gen-doc.

The definition is `api/chalkos/node/v1/node.proto`, package `chalkos.node.v1`.

## NodeService

NodeService is the API chalkd serves on each node. Each method's comment names the modes it is
available in (maintenance, normal or both) and the least role a client certificate needs to call
it: reader, operator or admin, where each role may call what the roles before it may. Only
nodes call RenewNodeCertificate, with the node role.

| Method | Request | Response |
| ------ | ------- | -------- |
| [Info](#method-info) | [InfoRequest](#message-inforequest) | [InfoResponse](#message-inforesponse) |
| [Disks](#method-disks) | [DisksRequest](#message-disksrequest) | [DisksResponse](#message-disksresponse) |
| [Install](#method-install) | [stream InstallRequest](#message-installrequest) | [InstallResponse](#message-installresponse) |
| [ApplyIdentity](#method-applyidentity) | [ApplyIdentityRequest](#message-applyidentityrequest) | [ApplyIdentityResponse](#message-applyidentityresponse) |
| [ResetVolume](#method-resetvolume) | [ResetVolumeRequest](#message-resetvolumerequest) | [ResetVolumeResponse](#message-resetvolumeresponse) |
| [Status](#method-status) | [StatusRequest](#message-statusrequest) | [StatusResponse](#message-statusresponse) |
| [Logs](#method-logs) | [LogsRequest](#message-logsrequest) | [stream LogsResponse](#message-logsresponse) |
| [Reboot](#method-reboot) | [RebootRequest](#message-rebootrequest) | [RebootResponse](#message-rebootresponse) |
| [Bootstrap](#method-bootstrap) | [BootstrapRequest](#message-bootstraprequest) | [BootstrapResponse](#message-bootstrapresponse) |
| [EtcdMembers](#method-etcdmembers) | [EtcdMembersRequest](#message-etcdmembersrequest) | [EtcdMembersResponse](#message-etcdmembersresponse) |
| [EtcdRemoveMember](#method-etcdremovemember) | [EtcdRemoveMemberRequest](#message-etcdremovememberrequest) | [EtcdRemoveMemberResponse](#message-etcdremovememberresponse) |
| [EtcdLeave](#method-etcdleave) | [EtcdLeaveRequest](#message-etcdleaverequest) | [EtcdLeaveResponse](#message-etcdleaveresponse) |
| [RenewNodeCertificate](#method-renewnodecertificate) | [RenewNodeCertificateRequest](#message-renewnodecertificaterequest) | [RenewNodeCertificateResponse](#message-renewnodecertificateresponse) |
| [RotationStep](#method-rotationstep) | [RotationStepRequest](#message-rotationsteprequest) | [RotationStepResponse](#message-rotationstepresponse) |
| [Upgrade](#method-upgrade) | [stream UpgradeRequest](#message-upgraderequest) | [UpgradeResponse](#message-upgraderesponse) |
| [DrainNode](#method-drainnode) | [DrainNodeRequest](#message-drainnoderequest) | [DrainNodeResponse](#message-drainnoderesponse) |
| [UncordonNode](#method-uncordonnode) | [UncordonNodeRequest](#message-uncordonnoderequest) | [UncordonNodeResponse](#message-uncordonnoderesponse) |

### Info { #method-info }

Info describes the node and the agent. Available in both modes to readers.

### Disks { #method-disks }

Disks lists the node's disks and their partitions. Available in both modes to readers.

### Install { #method-install }

Install turns a node in maintenance mode into an installed node and reboots it. The first
message carries the header; when the node is the installer, the image's store data, hash
tree, UKI and boot loader follow as chunks. An install the installer did not finish
continues when run again.

### ApplyIdentity { #method-applyidentity }

ApplyIdentity replaces the identity of an installed node. Destructive storage changes are
refused; additive ones are applied live.

### ResetVolume { #method-resetvolume }

ResetVolume wipes one volume and creates it again as the delivered identity defines it.

### Status { #method-status }

Status reports the identity version, the storage, and failed units of an installed node.

### Logs { #method-logs }

Logs streams the journal of the current boot, or of one unit in it.

### Reboot { #method-reboot }

Reboot reboots the node once the response is sent.

### Bootstrap { #method-bootstrap }

Bootstrap initialises etcd on a control-plane node, starts the control plane and applies
the cluster's manifests. It is refused on a node that is bootstrapped or holds etcd data,
so no second cluster is ever initialised.

### EtcdMembers { #method-etcdmembers }

EtcdMembers lists etcd's members as the node's own member sees them, with their health.
Available on control-plane nodes that are etcd members.

### EtcdRemoveMember { #method-etcdremovemember }

EtcdRemoveMember removes another node's etcd member. It is refused when the voters left would
have fewer healthy members than their quorum, unless forced.

### EtcdLeave { #method-etcdleave }

EtcdLeave takes the node out of etcd: it releases the VIPs, removes its own member, if etcd
still has it, with the same quorum guard, stops its control plane, deletes its etcd data and
unpins its addresses. The node joins the cluster again only once it is reinstalled. A
bootstrapped node whose own member does not answer leaves only when forced.

### RenewNodeCertificate { #method-renewnodecertificate }

RenewNodeCertificate issues the calling node a node certificate from the node CA, for exactly
the names of the node certificate it authenticated with. Available on control-plane nodes, to
nodes only.

### RotationStep { #method-rotationstep }

RotationStep runs a step of a CA or key rotation that the node's Kubernetes side carries
out; chalkctl rotate calls it. Available to admins.

### Upgrade { #method-upgrade }

Upgrade installs a new image into the node's inactive slot, from which it boots next, counting
the image's boot tries until a boot is found healthy, and reboots the node when asked to.
The first message carries the header; the store, its hash tree and the UKI follow as
chunks. Available to operators. The node checks that the image is of its cluster and role,
not that it is one to trust: without Secure Boot, an operator can run any image as root,
with STATE and VAR unsealed, as their keys are sealed to PCR 7 alone. Secure Boot is what
limits upgrades to images signed for db.

### DrainNode { #method-drainnode }

DrainNode cordons a node of the cluster for an upgrade and evicts its pods through the
eviction API, which keeps them within their PodDisruptionBudgets. DaemonSet pods, static pods
and pods without a controller stay; they run again on the node once it is back. Pods with
emptyDir volumes are evicted only when the request accepts losing that data. Pods that
terminate already, as those an earlier drain evicted, are not evicted again, but the call
returns only once they and the pods it evicted have stopped. A node that was cordoned already
stays cordoned and unmarked; one this call cordons is marked chalkos.dev/upgrade-cordon.
Available on bootstrapped control-plane nodes, to operators. An eviction refused for good, or
one a PodDisruptionBudget holds back until the timeout, fails the call with
FAILED_PRECONDITION; pods that did not stop in time, or whose evictions the API server kept
failing, with DEADLINE_EXCEEDED.

### UncordonNode { #method-uncordonnode }

UncordonNode makes a node DrainNode cordoned schedulable again and removes the mark; it leaves
a node cordoned otherwise as it is.

## Messages

### ApplyIdentityRequest { #message-applyidentityrequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `identity` | `string` | The node's identity as the manifest renders it (JSON); empty keeps the node's identity and storage, for a request that delivers only a share or a node certificate. |
| `fallback_secret` | `string` | Enrolled on encrypted volumes the identity adds; required when it adds one and the node's fallback is not none. |
| `kubernetes_share` | `bytes` | A new Kubernetes share (JSON) replacing the node's, as InstallHeader carries it; empty keeps the node's share. |
| `node_certificate` | `bytes` | A new node certificate for the node, followed by the certificate of the node CA that issued it (PEM), and its key, which chalkd serves from then on; empty keeps the node's. |
| `node_key` | `bytes` |  |
| `trust` | `bytes` | The OS CAs the node trusts from now on (PEM), as InstallHeader.ca_certificate holds them; empty keeps the node's. They must verify the node's certificate, the caller's and a control plane's node CA. Not together with a node certificate. |

### ApplyIdentityResponse { #message-applyidentityresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `changes` | repeated [`StorageChange`](#message-storagechange) | Storage changes that were applied. |
| `restarted_units` | repeated `string` | Units restarted because identity keys they read changed. |

### BootStatus { #message-bootstatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `version` | `string` | The version of the image the node runs, and the file of the UKI it booted. |
| `entry` | `string` |  |
| `blessed` | `bool` | Whether the boot was found healthy and marked good, or not counted by the boot loader at all; false while the boot loader counts the boot's tries. |
| `staged` | `string` | The version an upgrade installed to boot next; empty without one. |
| `failed` | `string` | The version whose boots used up their tries without being found healthy, from which the node fell back; empty without one. The failed UKI stays until the next upgrade. |
| `journal` | repeated `string` | The last lines chalkd and the health check logged during the failed version's last boot, as the check recorded them on VAR; empty without that record. |
| `error` | `string` | Why the boot could not be read; empty when it was. |
| `root_hash` | `bytes` | The verity root hash of the store the node runs, as its kernel command line's usrhash= names it, which tells builds of one version apart; empty when it could not be read. |

### BootstrapRequest { #message-bootstraprequest }



No fields.

### BootstrapResponse { #message-bootstrapresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `applied` | `uint32` | Objects applied from the cluster's manifests. |

### CertificateStatus { #message-certificatestatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | `string` | What the certificate is: "node", "OS CA", "node CA", "Kubernetes CA", "front-proxy CA", "etcd CA", "Kubernetes control plane" (the control plane's leaf certificates, by the one that expires first), "kubelet client" or "kubelet serving"; "Kubernetes share" when the node's share cannot be read, so the certificates it holds are unknown. |
| `not_after` | `google.protobuf.Timestamp` | Unset when the expiry is unknown. |
| `problem` | `string` | Why a renewal fails, such as "renewal failing: <reason>; expires <date>", or a warning, such as "less than a third of its lifetime remains" or "node CA expires <date>; run chalkctl node-ca rotate"; empty when there is nothing to do. <node> stands for the node's name. |
| `fingerprint` | `string` | The SHA-256 fingerprint of the certificate, and of the certificate that issued it when the node holds that one, in lower-case hex. |
| `issuer` | `string` |  |

### Disk { #message-disk }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | `string` | Kernel name, such as nvme0n1. |
| `device` | `string` |  |
| `model` | `string` |  |
| `size` | `uint64` |  |
| `serial` | `string` |  |
| `wwn` | `string` |  |
| `type` | `string` | nvme, ssd or hdd. |
| `path` | `string` | udev's ID_PATH. |
| `partitions` | repeated [`Partition`](#message-partition) |  |
| `usage` | `string` | How chalkos uses the disk: "boot" for the boot disk, "disk <name>" for a pinned disk of the node's storage, empty when chalkos does not use it. |

### DiskReference { #message-diskreference }

A disk by /dev path, or by a selector whose set fields must all match.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `path` | `string` |  |
| `model` | `string` |  |
| `serial` | `string` |  |
| `wwn` | `string` |  |
| `size` | `string` |  |
| `type` | `string` |  |

### DiskStatus { #message-diskstatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | `string` |  |
| `device` | `string` |  |
| `error` | `string` | Why the disk's volumes could not be created or opened at boot; empty when they were. |

### DisksRequest { #message-disksrequest }



No fields.

### DisksResponse { #message-disksresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `disks` | repeated [`Disk`](#message-disk) |  |

### DrainNodeRequest { #message-drainnoderequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | `string` | The node's name in Kubernetes. |
| `timeout_seconds` | `uint32` | How long to wait for the pods to be evicted and gone, in seconds; zero waits five minutes. |
| `delete_emptydir_data` | `bool` | Evict pods with emptyDir volumes too, whose data is lost. |

### DrainNodeResponse { #message-drainnoderesponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `marked` | `bool` | Whether the node is marked as cordoned for an upgrade, by this call or an earlier one. |
| `evicted` | repeated `string` | The pods evicted, and those that stay, as namespace/name: DaemonSet, static and finished pods in kept, and pods without a controller in unmanaged, which nothing starts again elsewhere. The node lifecycle controller deletes those for good when the node stays down longer than their tolerations allow. |
| `kept` | repeated `string` |  |
| `unmanaged` | repeated `string` |  |

### EncryptedObjects { #message-encryptedobjects }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `resource` | `string` | The resource, such as secrets. |
| `key` | `string` | The name of the key the objects are encrypted with; empty for objects stored unencrypted. |
| `objects` | `uint64` |  |

### EtcdLeaveRequest { #message-etcdleaverequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `force` | `bool` | Leave through the other members also when the node's own etcd member does not answer, as on a node that lost its pinned address. |

### EtcdLeaveResponse { #message-etcdleaveresponse }



No fields.

### EtcdMember { #message-etcdmember }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | `uint64` |  |
| `name` | `string` | The node's name; empty while a member that was added has not started. |
| `peer_urls` | repeated `string` |  |
| `learner` | `bool` |  |
| `unhealthy` | `string` | Why the member did not answer; empty when it is healthy. |

### EtcdMembersRequest { #message-etcdmembersrequest }



No fields.

### EtcdMembersResponse { #message-etcdmembersresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `members` | repeated [`EtcdMember`](#message-etcdmember) |  |

### EtcdRemoveMemberRequest { #message-etcdremovememberrequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `member` | `string` | The member's name or its ID in hex. |
| `force` | `bool` | Remove the member even when the voters left would have no healthy quorum. |

### EtcdRemoveMemberResponse { #message-etcdremovememberresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `removed` | [`EtcdMember`](#message-etcdmember) |  |

### ImageChunk { #message-imagechunk }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `data` | `bytes` |  |

### ImageHeader { #message-imageheader }

An image as install and upgrade send it. Its parts follow the header as chunks, in this order:
the store data, the hash tree, the UKI and, when the header names one, the boot loader.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `version` | `string` | The image's version, ID, cluster and role, as its os-release names them. |
| `image_id` | `string` |  |
| `cluster` | `string` |  |
| `role` | `string` |  |
| `root_hash` | `bytes` | The verity root hash of the image's store. |
| `store` | [`ImagePart`](#message-imagepart) | The store data the root hash covers, its hash tree and the signed UKI. |
| `hash_tree` | [`ImagePart`](#message-imagepart) |  |
| `uki` | [`ImagePart`](#message-imagepart) |  |
| `boot_loader` | [`ImagePart`](#message-imagepart) | The signed systemd-boot, which firmware starts from the ESP's removable-media path. |
| `architecture` | `string` | The architecture the image runs on, as systemd and os-release name it: x86-64 or arm64. A node refuses an image of another architecture than its own. |
| `platform` | `string` | The platform the image is built for, as its os-release names it, such as metal or kvm. A node refuses an image of another platform than its own, or than its identity names. |

### ImagePart { #message-imagepart }

The size and SHA-256 of a part of an image.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `size` | `uint64` |  |
| `sha256` | `bytes` |  |

### InPlace { #message-inplace }



No fields.

### InfoRequest { #message-inforequest }



No fields.

### InfoResponse { #message-inforesponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `mode` | [`Mode`](#enum-mode) |  |
| `version` | `string` | chalkos version of the running image. |
| `image_id` | `string` | Image ID of the running image, such as "chalkos". |
| `installer` | `bool` | Whether the node runs the installer, which writes a role image to another disk. |
| `boot_disk` | `string` | Device of the disk the node booted from; empty on the installer booted from a CD. |
| `tpm` | `bool` |  |
| `secure_boot` | [`SecureBoot`](#enum-secureboot) |  |
| `fingerprint` | `string` | SHA-256 of the certificate chalkd serves, in lower-case hex. |
| `hostname` | `string` |  |
| `cluster` | `string` | The cluster and role the running image was built for; empty on the installer. |
| `role` | `string` |  |
| `platform` | `string` | The platform the running image was built for, such as metal or kvm; empty on the installer, which installs images of every platform. |

### InstallHeader { #message-installheader }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `identity` | `string` | The node's identity as the manifest renders it (JSON), including its storage section. |
| `node_certificate` | `bytes` | PEM certificate and key chalkd serves once the node is installed. |
| `node_key` | `bytes` |  |
| `ca_certificate` | `bytes` | PEM certificates of the OS CAs the node trusts, the one that issues client certificates and the node CA first, then those still trusted while it rotates. |
| `fallback_secret` | `string` | Recovery key or password enrolled as the second keyslot of every encrypted volume; empty when the node's fallback is none. |
| `in_place` | [`InPlace`](#message-inplace) | The role image already runs from the boot disk. |
| `disk` | [`DiskReference`](#message-diskreference) | The installer lays out this disk and writes the streamed image to it. |
| `image` | [`ImageHeader`](#message-imageheader) | The role image the installer writes to the target disk, whose parts follow as chunks; it must name the boot loader. Installer only. |
| `wipe_disk` | `bool` | Let the installer replace whatever the target disk holds, including an installed node. |
| `system_definitions` | repeated [`InstallHeader.SystemDefinitionsEntry`](#message-installheadersystemdefinitionsentry) | The role image's repart definitions of the system region (ESP, slots A and B, STATE) by file name, which the installer lays out the target disk with; installer only. Each role chooses its partition sizes, so the installer's own may not fit. |
| `kubernetes_share` | `bytes` | The node's Kubernetes share (JSON): the CAs and keys for a control-plane node, the CA certificate and a kubelet client certificate for a worker. Empty for a role without Kubernetes. |

### InstallHeader.SystemDefinitionsEntry { #message-installheadersystemdefinitionsentry }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `key` | `string` |  |
| `value` | `string` |  |

### InstallRequest { #message-installrequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `header` | [`InstallHeader`](#message-installheader) |  |
| `chunk` | [`ImageChunk`](#message-imagechunk) |  |

### InstallResponse { #message-installresponse }



No fields.

### KubernetesStatus { #message-kubernetesstatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `kind` | `string` | controlplane or worker. |
| `state` | `string` | What the node waits for or is: "no share", "preparing" while its Kubernetes files are prepared, or why their preparation failed, such as "preparation failed: no ipv4 node address matches validSubnets 10.0.0.0/8 (the node has ...)"; then on a control-plane node "waiting for bootstrap or for the cluster at <endpoint>", what joining that cluster does or waits for, such as "joining the cluster at <endpoint>: etcd member <id> catches up" or a stale member and how to remove it, "bootstrapped" or "etcd data missing: restore etcd or reinstall the node"; "joined" on a worker, followed by ": the Node has no <family> pod range; ..." when the control plane allocated it no pod range of one of the worker's families. |
| `node_ready` | `string` | The status of the Node's Ready condition (True, False or Unknown), or why it could not be read. |
| `vip` | `string` | On a bootstrapped control-plane node of a cluster with VIPs: "holder" while the node holds them, "standby" otherwise. Empty on other nodes. |
| `control_plane` | `string` | On a bootstrapped control-plane node: "current" once its API server and etcd serve the certificates the node holds now and the API server answers ready, otherwise what is not so, such as "the API server serves certificates the node replaced". Empty on other nodes. |

### LogsRequest { #message-logsrequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `unit` | `string` | Unit whose journal to stream; empty streams the whole boot. |
| `follow` | `bool` | Keep streaming new entries until the client cancels. |

### LogsResponse { #message-logsresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `line` | `string` |  |

### Partition { #message-partition }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `device` | `string` |  |
| `number` | `uint32` |  |
| `size` | `uint64` |  |
| `type` | `string` | GPT partition type UUID. |
| `label` | `string` |  |
| `uuid` | `string` |  |
| `content` | `string` | File system or other content blkid found, such as ext4 or crypto_LUKS. |

### RebootRequest { #message-rebootrequest }



No fields.

### RebootResponse { #message-rebootresponse }



No fields.

### RenewNodeCertificateRequest { #message-renewnodecertificaterequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_request` | `bytes` | PKCS #10 certificate request (DER) signed with the node's new key, which never leaves the node. Only its key is used; the names come from the caller's certificate. |

### RenewNodeCertificateResponse { #message-renewnodecertificateresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_chain` | `bytes` | The new node certificate followed by the certificate of the node CA that issued it (PEM). |

### ResetVolumeRequest { #message-resetvolumerequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `volume` | `string` |  |
| `identity` | `string` | The node's identity as the manifest renders it (JSON); the volume is created as it defines. |
| `fallback_secret` | `string` |  |

### ResetVolumeResponse { #message-resetvolumeresponse }



No fields.

### RotationStepRequest { #message-rotationsteprequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `step` | [`RotationStep`](#enum-rotationstep) |  |

### RotationStepResponse { #message-rotationstepresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `restarted` | repeated `string` | The workloads restarted, as namespace/kind/name. |
| `token_secrets` | repeated `string` | The Secrets of type kubernetes.io/service-account-token, as namespace/name. |
| `rewritten` | `uint64` | How many objects were updated. |
| `encrypted` | repeated [`EncryptedObjects`](#message-encryptedobjects) | The objects etcd holds by resource and key. |
| `not_waited` | repeated `string` | The workloads restarted but not waited for, as namespace/kind/name: their rollout does not complete by itself (an OnDelete update strategy, a StatefulSet's partition, a paused Deployment). |

### StatusRequest { #message-statusrequest }



No fields.

### StatusResponse { #message-statusresponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `identity_version` | `string` | SHA-256 of the identity document the node runs, in lower-case hex. |
| `disks` | repeated [`DiskStatus`](#message-diskstatus) |  |
| `volumes` | repeated [`VolumeStatus`](#message-volumestatus) |  |
| `failed_units` | repeated `string` |  |
| `kubernetes` | [`KubernetesStatus`](#message-kubernetesstatus) | Absent on a node of a role without Kubernetes. |
| `certificates` | repeated [`CertificateStatus`](#message-certificatestatus) | Every certificate the node holds or issues, with its expiry and what needs doing about it. |
| `time` | [`TimeStatus`](#message-timestatus) | How the node keeps its clock. |
| `trust` | repeated [`TrustStatus`](#message-truststatus) | What the node trusts and issues with, by fingerprint. |
| `boot` | [`BootStatus`](#message-bootstatus) | The image the node booted, and what upgrades installed beside it. |
| `platform` | `string` | The platform the running image was built for. |

### StorageChange { #message-storagechange }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `volume` | `string` |  |
| `reason` | `string` |  |
| `destructive` | `bool` |  |

### TimeStatus { #message-timestatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `synchronised` | `bool` | Whether chrony synchronises the clock with a server. |
| `source` | `string` | The server the clock follows. |
| `offset_seconds` | `double` | How far the clock is from the server's time, in seconds; positive when it is ahead. |
| `error` | `string` | Why chrony's state could not be read; empty when it was. |

### TrustStatus { #message-truststatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | `string` | What is trusted: "OS CA" on every node; "Kubernetes CA" on nodes with Kubernetes; on control-plane nodes also "front-proxy CA", "etcd CA", "service-account keys" and "encryption keys". |
| `fingerprints` | repeated `string` | The SHA-256 fingerprints of what is trusted, in lower-case hex: of certificates and of public keys in DER, and of encryption keys hashed apart from both. The one that issues or signs comes first. |
| `issuing` | `string` | The fingerprint of the value the node issues, signs or encrypts with; empty where it does none of these. |

### UncordonNodeRequest { #message-uncordonnoderequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | `string` | The node's name in Kubernetes. |

### UncordonNodeResponse { #message-uncordonnoderesponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `uncordoned` | `bool` | Whether the node was cordoned for an upgrade and is schedulable again. |

### UpgradeHeader { #message-upgradeheader }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `image` | [`ImageHeader`](#message-imageheader) | The image to install. The node refuses another ID, cluster or role than its own, and an image that names a boot loader: an upgrade leaves the boot loader as it is. |
| `reboot` | `bool` | Reboot the node into the image once it is installed. |

### UpgradeRequest { #message-upgraderequest }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `header` | [`UpgradeHeader`](#message-upgradeheader) |  |
| `chunk` | [`ImageChunk`](#message-imagechunk) |  |

### UpgradeResponse { #message-upgraderesponse }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `already_installed` | `bool` | The node runs the image already; nothing changed. |
| `entry` | `string` | The file name of the UKI that boots the image next, with its tries. |
| `rebooting` | `bool` | The node reboots into the image. |

### VolumeStatus { #message-volumestatus }



| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | `string` |  |
| `disk` | `string` |  |
| `mount_point` | `string` |  |
| `present` | `bool` | Whether the volume's partition exists. |
| `mounted` | `bool` | Whether the volume is mounted at its mount point. |

## Enums

### Mode { #enum-mode }



| Name | Number | Description |
| ---- | ------ | ----------- |
| `MODE_UNSPECIFIED` | 0 |  |
| `MODE_MAINTENANCE` | 1 | The node is not installed: chalkd serves a self-signed certificate. |
| `MODE_NORMAL` | 2 | The node is installed: chalkd serves its node certificate and requires client certificates. |

### RotationStep { #enum-rotationstep }



| Name | Number | Description |
| ---- | ------ | ----------- |
| `ROTATION_STEP_UNSPECIFIED` | 0 |  |
| `ROTATION_STEP_RESTART_ADDONS` | 1 | On a bootstrapped control-plane node: restart the Deployments, DaemonSets and StatefulSets of the cluster's manifests, chalkos's addons among them, once the kube-root-ca.crt ConfigMaps of their namespaces hold every Kubernetes CA the node trusts, and wait until they rolled out. |
| `ROTATION_STEP_LIST_TOKEN_SECRETS` | 2 | On a bootstrapped control-plane node: list the Secrets of type kubernetes.io/service-account-token, whose tokens nothing signs again. |
| `ROTATION_STEP_REWRITE_ENCRYPTED` | 3 | On a bootstrapped control-plane node: update every object of the resources the API server encrypts without changing it, so the API server encrypts it with its first key, then count the objects etcd holds under each key. |
| `ROTATION_STEP_COUNT_ENCRYPTED` | 4 | On a bootstrapped control-plane node: count the objects etcd holds under each key. |
| `ROTATION_STEP_RENEW_KUBELET_SERVING` | 5 | On a node with Kubernetes: remove the kubelet's serving certificate and restart the kubelet, which requests a new one. |

### SecureBoot { #enum-secureboot }



| Name | Number | Description |
| ---- | ------ | ----------- |
| `SECURE_BOOT_UNSPECIFIED` | 0 |  |
| `SECURE_BOOT_DISABLED` | 1 |  |
| `SECURE_BOOT_ENABLED` | 2 |  |
| `SECURE_BOOT_SETUP_MODE` | 3 | The firmware accepts new Secure Boot keys without authentication. |

