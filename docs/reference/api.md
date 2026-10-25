---
title: "Node API"
description: "The API chalkd serves on every node, generated from its protobuf definition"
---

# Node API

This page is for authors of clients of the node API, the API chalkd serves on every node; most
readers want [chalkctl](cli/chalkctl.md), its client, instead. chalkd serves the API on TCP port
50000 over TLS, as a Connect service that also answers gRPC and gRPC-Web.

Each method names the modes it is available in and the least role it needs, which
[Roles and credentials](access.md) explains, followed by its request and response messages. The
messages that fields of other messages hold are under [Messages](#messages), and the enumerations
under [Enums](#enums). proto3 fields have no defaults: a field a message leaves out reads as the
zero value of its type, such as an empty string, false, 0 or an unset message. Of the fields whose
type names a oneof, a message sets at most one.

The definition is `api/chalkos/node/v1/node.proto`, package `chalkos.node.v1`.

## NodeService { #nodeservice }

NodeService is the API chalkd serves on each node. Each method's comment names the modes it is
available in, maintenance mode, normal mode or both, and the least role a client certificate
needs to call it: reader, operator or admin, where each role may call what the roles before it
may. Only nodes call RenewNodeCertificate, with the node role.

### Info { #method-info }

Info describes the node and the agent. Available in maintenance and normal mode to readers.

Request: InfoRequest. Response: InfoResponse.

#### InfoRequest { #message-inforequest }

The request of Info.

No fields.

#### InfoResponse { #message-inforesponse }

The response of Info.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `mode` | Mode | Mode chalkd serves the node in. |
| `image_id` | string | Image ID of the running image, such as "chalkos". |
| `installer` | bool | Whether the node runs the installer, which writes a node's image to another disk. |
| `boot_disk` | string | Device of the disk the node booted from; empty on the installer booted from a CD. |
| `tpm` | bool | Whether the kernel found a TPM, as /sys/class/tpm lists one. |
| `secure_boot` | SecureBoot | State of Secure Boot. |
| `fingerprint` | string | SHA-256 of the certificate chalkd serves, in lower-case hex. |
| `hostname` | string | Host name of the node, as the kernel holds it. |
| `variant` | string | The mkImages variant the running image was built as; empty on the installer. |
| `platform` | string | The platform the running image was built for, such as metal or kvm; empty on the installer, which installs images of every platform. |
| `root_hash` | bytes | The verity root hash of the store the node runs, as its kernel command line's usrhash= names it, which identifies the running image; empty when it could not be read. |
| `revision` | string | The git revision the running image was built from, as the node's record of the images it installed names it; empty when it does not know it. |
| `tag` | string | The git tag the running image was built from, as the node's record of the images it installed names it; empty when it does not know it. |
| `layers` | list of string | The layers the running image carries beside the base every image has, such as "kubernetes", in name order; empty on the installer and on an image of the base alone. |

Field types: [Mode](#enum-mode), [SecureBoot](#enum-secureboot).

### Disks { #method-disks }

Disks lists the node's disks and their partitions. Available in maintenance and normal mode to
readers.

Request: DisksRequest. Response: DisksResponse.

#### DisksRequest { #message-disksrequest }

The request of Disks.

No fields.

#### DisksResponse { #message-disksresponse }

The response of Disks.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `disks` | list of Disk | The node's disks, by kernel name. |

Field types: [Disk](#message-disk).

### Contract { #method-contract }

Contract returns the config contract of the image the node runs: the variant and platform it
was built for, its layers, the schemas of the config kinds it accepts and its image.json, which
chalkctl checks config against before it delivers it. The installer refuses it: config is for
the image it installs, whose contract comes with that image. Available in maintenance and
normal mode to readers.

Request: ContractRequest. Response: ContractResponse.

#### ContractRequest { #message-contractrequest }

The request of Contract.

No fields.

#### ContractResponse { #message-contractresponse }

The response of Contract: the config contract of the image the node runs.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `schemas` | map of bytes | The image's schemas by file name, as /etc/chalkos/schemas holds them. |
| `variant` | string | The variant the image was built as, as its os-release names it. |
| `platform` | string | The platform the image was built for, as its os-release names it. |
| `kubernetes_image` | bytes | The image's image.json: the Kubernetes release, the images of its components and the pod network it ships. Empty on an image without Kubernetes. |
| `layers` | list of string | The layers the image carries beside the base every image has, such as "kubernetes", in name order. |

### Install { #method-install }

Install turns a node in maintenance mode into an installed node and reboots it. The first
message carries the header; when the node is the installer, the image's store data, hash
tree, UKI and boot loader follow as chunks, unless the header's source names an image in a
registry, which the installer pulls. An install the installer did not finish continues when
run again, from either. Available in maintenance mode to admins.

Request: a stream of InstallRequest. Response: InstallResponse.

#### InstallRequest { #message-installrequest }

A message of the Install stream: the header first, then the image's chunks.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `header` | InstallHeader, oneof message | The install's header, in the stream's first message. |
| `chunk` | ImageChunk, oneof message | The next bytes of the image's parts, in every message after the first. |

Field types: [InstallHeader](#message-installheader), [ImageChunk](#message-imagechunk).

#### InstallResponse { #message-installresponse }

The response of Install, which the node sends before it reboots.

No fields.

### ApplyConfig { #method-applyconfig }

ApplyConfig replaces the config of an installed node, or delivers it a Kubernetes share, a
node certificate or OS CAs. Destructive storage changes are refused; additive ones are
applied live. Available in normal mode to admins.

Request: ApplyConfigRequest. Response: ApplyConfigResponse.

#### ApplyConfigRequest { #message-applyconfigrequest }

The request of ApplyConfig: a config, or a share, a node certificate or OS CAs to deliver.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `fallback_secret` | string | Recovery key or password enrolled on the encrypted volumes the config adds; required when it adds one and the node's fallback is not none. |
| `kubernetes_share` | bytes | A new Kubernetes share (JSON) replacing the node's, as InstallHeader carries it; empty keeps the node's share. |
| `node_certificate` | bytes | A new node certificate for the node, followed by the certificate of the node CA that issued it (PEM), which chalkd serves from then on; empty keeps the node's. |
| `node_key` | bytes | PEM key of node_certificate; empty keeps the node's. |
| `trust` | bytes | The OS CAs the node trusts from now on (PEM), as InstallHeader.ca_certificate holds them; empty keeps the node's. They must verify the node's certificate, the caller's and a control plane's node CA. Not together with a node certificate. |
| `config` | string | The node's config, as InstallHeader.config holds it, checked against the running image's schemas. It replaces the node's config as a whole. Empty keeps the node's config and storage, for a request that delivers only a share, a node certificate or OS CAs. |
| `staged` | bool | Whether the config is for the image staged to boot next: chalkd checks it against that image and keeps it in that image's config slot, changing nothing the running image runs but the machine's storage. The config meets that image's change rules, compared with the running slot's config and the machine as for the running image, and the storage changes they accept are made at once, with fallback_secret. It takes the config and the fallback secret alone, and an image staged. |

#### ApplyConfigResponse { #message-applyconfigresponse }

The response of ApplyConfig: what the delivery changed on the node.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `changes` | list of StorageChange | Storage changes that were applied. |
| `restarted_units` | list of string | Units the delivery restarted, as the image's effects say. |
| `warnings` | list of string | Why the rule of an effect could not be evaluated, without the values it read; the delivery took the effect all the same. |

Field types: [StorageChange](#message-storagechange).

### GetConfig { #method-getconfig }

GetConfig returns the config a slot keeps, the running image's or the staged image's, as it
was delivered, with secret values replaced by their hashes. Available in normal mode to
admins.

Request: GetConfigRequest. Response: GetConfigResponse.

#### GetConfigRequest { #message-getconfigrequest }

The request of GetConfig.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `staged` | bool | Whether the response holds the staged image's config instead of the running one's. |

#### GetConfigResponse { #message-getconfigresponse }

The response of GetConfig: the config one slot keeps.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `documents` | string | The slot's documents as delivered, one JSON document each separated by ---, as ApplyConfigRequest.config holds them. The values of Secrets and of the Kubernetes Secrets an Addon applies are replaced by the lower-case hex SHA-256 of the value, as config_version hashes a Secret's. |
| `version` | string | The slot's version, as StatusResponse.config_version gives it. |

### ResetVolume { #method-resetvolume }

ResetVolume wipes one volume and creates it again as the delivered config defines it. It
refuses VAR, a volume the config no longer has and a volume the config moves to another
disk. Available in normal mode to admins.

Request: ResetVolumeRequest. Response: ResetVolumeResponse.

#### ResetVolumeRequest { #message-resetvolumerequest }

The request of ResetVolume.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `volume` | string | Name of the volume to reset. |
| `fallback_secret` | string | Recovery key or password enrolled on the volume when it is encrypted; required then, unless the node's fallback is none. |
| `config` | string | The node's config, as ApplyConfigRequest.config holds it, checked against the running image's schemas; the volume is created as it defines. Required. |

#### ResetVolumeResponse { #message-resetvolumeresponse }

The response of ResetVolume, once the volume is created again.

No fields.

### Status { #method-status }

Status reports the state of an installed node: its config versions, storage, failed units,
certificates, trust, clock, boot, outputs and Kubernetes. Available in normal mode to readers.

Request: StatusRequest. Response: StatusResponse.

#### StatusRequest { #message-statusrequest }

The request of Status.

No fields.

#### StatusResponse { #message-statusresponse }

The response of Status: the state of an installed node.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `disks` | list of DiskStatus | The disks of the node's storage, by name, as the boot's storage setup left them. |
| `volumes` | list of VolumeStatus | The volumes of the node's storage, by name. |
| `failed_units` | list of string | Names of the systemd units that failed. |
| `kubernetes` | KubernetesStatus | State of the node's Kubernetes; absent on a node whose image has no Kubernetes. |
| `certificates` | list of CertificateStatus | Every certificate the node holds or issues, with its expiry and what needs doing about it. |
| `time` | TimeStatus | How the node keeps its clock. |
| `trust` | list of TrustStatus | What the node trusts and issues with, by fingerprint. |
| `boot` | BootStatus | The image the node booted, and what upgrades installed beside it. |
| `platform` | string | The platform the running image was built for. |
| `config_version` | string | SHA-256 of the config the node runs, as it was delivered, in lower-case hex: of its documents in canonical JSON, ordered by apiVersion, kind and name, with each Secret value replaced by its own SHA-256. Empty on a node that runs no config. |
| `staged_config_version` | string | The version of the config kept for the image staged to boot next, as config_version gives the running one's; empty without a staged image or config for it. |
| `outputs` | list of OutputStatus | The image's outputs, by name. |

Field types: [DiskStatus](#message-diskstatus), [VolumeStatus](#message-volumestatus), [KubernetesStatus](#message-kubernetesstatus), [CertificateStatus](#message-certificatestatus), [TimeStatus](#message-timestatus), [TrustStatus](#message-truststatus), [BootStatus](#message-bootstatus), [OutputStatus](#message-outputstatus).

### Logs { #method-logs }

Logs streams the journal of the current boot, or of one unit in it. Available in maintenance
and normal mode to readers.

Request: LogsRequest. Response: a stream of LogsResponse.

#### LogsRequest { #message-logsrequest }

The request of Logs.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `unit` | string | Unit whose journal to stream; empty streams the whole boot. |
| `follow` | bool | Whether the stream goes on with new entries until the client cancels it. |

#### LogsResponse { #message-logsresponse }

A message of the Logs stream.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `line` | string | One journal entry, as journalctl's short-iso output prints it. |

### Reboot { #method-reboot }

Reboot reboots the node once the response is sent. Available in maintenance and normal mode
to operators.

Request: RebootRequest. Response: RebootResponse.

#### RebootRequest { #message-rebootrequest }

The request of Reboot.

No fields.

#### RebootResponse { #message-rebootresponse }

The response of Reboot, which the node sends before it reboots.

No fields.

### Bootstrap { #method-bootstrap }

Bootstrap initialises etcd on a control-plane node, starts the control plane and applies
chalkos's add-ons and the objects of the node's Addon documents. It is refused on a node that
is bootstrapped or holds etcd data, so no second cluster is ever initialised. Available in
normal mode to admins.

Request: BootstrapRequest. Response: BootstrapResponse.

#### BootstrapRequest { #message-bootstraprequest }

The request of Bootstrap.

No fields.

#### BootstrapResponse { #message-bootstrapresponse }

The response of Bootstrap, once the add-ons are applied.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `applied` | uint32 | Objects applied: chalkos's add-ons and those of the node's Addon documents. |

### EtcdMembers { #method-etcdmembers }

EtcdMembers lists etcd's members as the node's own member sees them, with their health. It
answers on control-plane nodes that are etcd members. Available in normal mode to readers.

Request: EtcdMembersRequest. Response: EtcdMembersResponse.

#### EtcdMembersRequest { #message-etcdmembersrequest }

The request of EtcdMembers.

No fields.

#### EtcdMembersResponse { #message-etcdmembersresponse }

The response of EtcdMembers.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `members` | list of EtcdMember | etcd's members, as etcd lists them. |

Field types: [EtcdMember](#message-etcdmember).

### EtcdRemoveMember { #method-etcdremovemember }

EtcdRemoveMember removes another node's etcd member. It is refused when the voters left would
have fewer healthy members than their quorum, unless forced, and for the node's own member,
which EtcdLeave removes. Available in normal mode to admins.

Request: EtcdRemoveMemberRequest. Response: EtcdRemoveMemberResponse.

#### EtcdRemoveMemberRequest { #message-etcdremovememberrequest }

The request of EtcdRemoveMember.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `member` | string | The member's name or its ID in hex. |
| `force` | bool | Whether the member is removed even when the voters left would have no healthy quorum. |

#### EtcdRemoveMemberResponse { #message-etcdremovememberresponse }

The response of EtcdRemoveMember.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `removed` | EtcdMember | The member that was removed, without its health. |

Field types: [EtcdMember](#message-etcdmember).

### EtcdLeave { #method-etcdleave }

EtcdLeave takes the node out of etcd: it releases the VIPs, removes its own member, if etcd
still has it, with the same quorum guard, stops its control plane, deletes its etcd data and
unpins its addresses. The node joins the cluster again only once it is reinstalled. A
bootstrapped node whose own member does not answer leaves only when forced. Available in
normal mode to admins.

Request: EtcdLeaveRequest. Response: EtcdLeaveResponse.

#### EtcdLeaveRequest { #message-etcdleaverequest }

The request of EtcdLeave.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `force` | bool | Whether the node leaves through the other members also when its own etcd member does not answer, as on a node that lost its pinned address. |

#### EtcdLeaveResponse { #message-etcdleaveresponse }

The response of EtcdLeave, once the node is out of etcd.

No fields.

### RenewNodeCertificate { #method-renewnodecertificate }

RenewNodeCertificate issues the calling node a node certificate from the node CA, for exactly
the names of the node certificate it authenticated with. It answers on control-plane nodes
and refuses a revoked node. Available in normal mode to nodes.

Request: RenewNodeCertificateRequest. Response: RenewNodeCertificateResponse.

#### RenewNodeCertificateRequest { #message-renewnodecertificaterequest }

The request of RenewNodeCertificate.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_request` | bytes | PKCS #10 certificate request (DER) signed with the node's new key, which never leaves the node. Only its key is used; the names come from the caller's certificate. |

#### RenewNodeCertificateResponse { #message-renewnodecertificateresponse }

The response of RenewNodeCertificate.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_chain` | bytes | The new node certificate followed by the certificate of the node CA that issued it (PEM). |

### RotationStep { #method-rotationstep }

RotationStep runs a step of a CA or key rotation that the node's Kubernetes side carries
out; chalkctl rotate calls it. Available in normal mode to admins.

Request: RotationStepRequest. Response: RotationStepResponse.

#### RotationStepRequest { #message-rotationsteprequest }

The request of RotationStep.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `step` | RotationStep | The step to run. |

Field types: [RotationStep](#enum-rotationstep).

#### RotationStepResponse { #message-rotationstepresponse }

The response of RotationStep; each step sets the fields it reports.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `restarted` | list of string | The workloads restarted, as namespace/kind/name. |
| `token_secrets` | list of string | The Secrets of type kubernetes.io/service-account-token, as namespace/name. |
| `rewritten` | uint64 | How many objects were updated. |
| `encrypted` | list of EncryptedObjects | The objects etcd holds by resource and key. |
| `not_waited` | list of string | The workloads restarted but not waited for, as namespace/kind/name: their rollout does not complete by itself (an OnDelete update strategy, a StatefulSet's partition, a paused Deployment). |

Field types: [EncryptedObjects](#message-encryptedobjects).

### Upgrade { #method-upgrade }

Upgrade installs a new image into the node's inactive slot, from which it boots next, counting
the image's boot tries until a boot is found healthy, and reboots the node when asked to.
The first message carries the header; the store, its hash tree and the UKI follow as chunks,
unless the header's source names an image in a registry, which the node pulls. Available in
normal mode to operators. Delivering config or a fallback secret with the upgrade needs the
admin role, as ApplyConfig does; without them the node carries the running image's config
over. The node checks that the image is of its platform, and, once the store is written and
before the image becomes the next boot, that the schemas in the image accept the config it
keeps for the image. It does not check that the image is one to trust: without Secure Boot,
an operator can run any image as root, with STATE and VAR unsealed, as their keys are sealed
to PCR 7 alone. Secure Boot is what limits upgrades to images signed for db.

Request: a stream of UpgradeRequest. Response: UpgradeResponse.

#### UpgradeRequest { #message-upgraderequest }

A message of the Upgrade stream: the header first, then the image's chunks.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `header` | UpgradeHeader, oneof message | The upgrade's header, in the stream's first message. |
| `chunk` | ImageChunk, oneof message | The next bytes of the image's parts, in every message after the first. |

Field types: [UpgradeHeader](#message-upgradeheader), [ImageChunk](#message-imagechunk).

#### UpgradeResponse { #message-upgraderesponse }

The response of Upgrade.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `already_installed` | bool | Whether the node runs the image already, so nothing changed. |
| `entry` | string | The file name of the UKI that boots the image next, with its tries. |
| `rebooting` | bool | Whether the node reboots into the image. |
| `already_staged` | bool | Whether the image boots next already, from entry, as an earlier upgrade without a reboot left it; the image was not written again, and the config the upgrade carries, or the running image's, replaced the config kept for it. |

### DrainNode { #method-drainnode }

DrainNode cordons a node of the cluster for an upgrade and evicts its pods through the
eviction API, which keeps them within their PodDisruptionBudgets. DaemonSet pods, static pods
and pods without a controller stay; they run again on the node once it is back. Pods with
emptyDir volumes are evicted only when the request accepts losing that data. Pods that
terminate already, as those an earlier drain evicted, are not evicted again, but the call
returns only once they and the pods it evicted have stopped. A node that was cordoned already
stays cordoned and unmarked; one this call cordons is marked chalkos.dev/upgrade-cordon. It
answers on bootstrapped control-plane nodes. Available in normal mode to operators. An
eviction refused for good, or one a PodDisruptionBudget holds back until the timeout, fails
the call with FAILED_PRECONDITION; pods that did not stop in time, or whose evictions the API
server kept failing, with DEADLINE_EXCEEDED.

Request: DrainNodeRequest. Response: DrainNodeResponse.

#### DrainNodeRequest { #message-drainnoderequest }

The request of DrainNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |
| `timeout_seconds` | uint32 | How long to wait for the pods to be evicted and gone, in seconds; zero waits five minutes. |
| `delete_emptydir_data` | bool | Whether pods with emptyDir volumes are evicted too, which loses their data. |

#### DrainNodeResponse { #message-drainnoderesponse }

The response of DrainNode: the pods evicted and the pods that stay.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `marked` | bool | Whether the node is marked as cordoned for an upgrade, by this call or an earlier one. |
| `evicted` | list of string | The pods evicted, as namespace/name. |
| `kept` | list of string | The DaemonSet, static and finished pods, which stay on the node, as namespace/name. |
| `unmanaged` | list of string | The pods without a controller, which stay and which nothing starts again elsewhere, as namespace/name. The node lifecycle controller deletes them for good when the node stays down longer than their tolerations allow. |

### UncordonNode { #method-uncordonnode }

UncordonNode makes a node DrainNode cordoned schedulable again and removes the mark; it leaves
a node cordoned otherwise as it is. It answers on bootstrapped control-plane nodes. Available
in normal mode to operators.

Request: UncordonNodeRequest. Response: UncordonNodeResponse.

#### UncordonNodeRequest { #message-uncordonnoderequest }

The request of UncordonNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |

#### UncordonNodeResponse { #message-uncordonnoderesponse }

The response of UncordonNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `uncordoned` | bool | Whether the node was cordoned for an upgrade and is schedulable again. |

### RevokeNode { #method-revokenode }

RevokeNode cuts a node off the cluster by name: it lists the node in the ConfigMap
kube-system/chalkos-revoked-nodes, applies the API server's admission policy
chalkos-revoked-nodes, which then denies the node's writes, and deletes its Node object.
Control planes then refuse to renew its node certificate. A node revoked already keeps the
time of its first revocation. A call that fails after listing the node says so; calling again
finishes it. A node not revoked yet is refused with FAILED_PRECONDITION while the policy's
map of revoked nodes would grow too long for the API server to compile with it. It answers on
bootstrapped control-plane nodes. Available in normal mode to admins.

Request: RevokeNodeRequest. Response: RevokeNodeResponse.

#### RevokeNodeRequest { #message-revokenoderequest }

The request of RevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes, which its node certificate carries too. |

#### RevokeNodeResponse { #message-revokenoderesponse }

The response of RevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked: now, or at an earlier revocation. |
| `node_deleted` | bool | Whether the call deleted the node's Node object; false when the cluster had none. |

### UnrevokeNode { #method-unrevokenode }

UnrevokeNode removes a node from the revoked nodes and applies the admission policy without
it, so a node of that name may join again. The old node's certificates work again too, unless
the CAs they chain to were rotated after the revocation: the call is refused with
FAILED_PRECONDITION while the OS CA or the Kubernetes CA predates it, unless forced. A call
that fails after removing the node says so; calling again applies the policy. It answers on
bootstrapped control-plane nodes. Available in normal mode to admins.

Request: UnrevokeNodeRequest. Response: UnrevokeNodeResponse.

#### UnrevokeNodeRequest { #message-unrevokenoderequest }

The request of UnrevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |
| `force` | bool | Whether the node is unrevoked although the CAs its old certificates chain to predate the revocation. |

#### UnrevokeNodeResponse { #message-unrevokenoderesponse }

The response of UnrevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked; unset when the time it is listed with cannot be read, which counts every CA as made before the revocation. |
| `stale_cas` | list of string | The CAs issued before the revocation, by the names chalkctl rotate takes: os-ca, kubernetes-ca. The old node's certificates work again until they expire or these CAs are rotated. Empty when both were rotated after the revocation. |

## Messages { #messages }

These messages are held by fields of other messages.

### BootImage { #message-bootimage }

An image a UKI on the ESP boots.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `root_hash` | bytes | The verity root hash of the image's store, from the usrhash= of the UKI or, for the running image, of the kernel command line; it identifies the image. |
| `generation` | uint64 | The generation of the UKI's boot entry, from its file name, &lt;image ID>_&lt;generation>.efi; systemd-boot boots the highest first. Zero when the name gives none or the entry is unknown. |
| `revision` | string | The git revision the image was built from, as the node's record of the images it installed names it; empty when it does not know it. |
| `tag` | string | The git tag the image was built from, as the node's record of the images it installed names it; empty when it does not know it. |

### BootStatus { #message-bootstatus }

The images on the node's ESP and how the running one booted.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `running` | BootImage | The image the node runs. |
| `entry` | string | File name of the UKI the node booted, in the ESP's /EFI/Linux, with its tries counter while it has one. |
| `blessed` | bool | Whether the boot was found healthy and marked good, or not counted by the boot loader at all; false while the boot loader counts the boot's tries. |
| `staged` | BootImage | The image an upgrade installed to boot next; unset without one. |
| `failed` | BootImage | The image whose boots used up their tries without being found healthy, from which the node fell back; unset without one. The failed UKI stays until the next upgrade. |
| `journal` | list of string | The last lines chalkd and the health check logged during the failed image's last boot, as the check recorded them on VAR; empty without that record. |
| `error` | string | Why the boot could not be read; empty when it was. The running image's root hash is set when the kernel command line could be read. |

Field types: [BootImage](#message-bootimage).

### CertificateStatus { #message-certificatestatus }

A certificate the node holds or issues with.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | What the certificate is: "node", "OS CA", "node CA", "Kubernetes CA", "front-proxy CA", "etcd CA", "Kubernetes control plane" (the control plane's leaf certificates, by the one that expires first), "kubelet client" or "kubelet serving"; "Kubernetes share" when the node's share cannot be read, so the certificates it holds are unknown. |
| `not_after` | google.protobuf.Timestamp | When the certificate expires; unset when the expiry is unknown. |
| `problem` | string | Why a renewal fails, such as "renewal failing: &lt;reason>; expires &lt;date>", or a warning, such as "less than a third of its lifetime remains" or "node CA expires &lt;date>; run chalkctl node-ca rotate"; empty when there is nothing to do. A command it advises names the node, or says &lt;node> when the node cannot tell its name. |
| `fingerprint` | string | The SHA-256 fingerprint of the certificate, in lower-case hex. |
| `issuer` | string | The SHA-256 fingerprint of the certificate that issued it, in lower-case hex; empty when the node does not hold that one. |

### Disk { #message-disk }

A disk of the node, as sysfs and the udev database describe it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Kernel name, such as nvme0n1. |
| `device` | string | Device node, such as /dev/nvme0n1. |
| `model` | string | Model, from udev's ID_MODEL with spaces for underscores, or else from sysfs. |
| `size` | uint64 | Size in bytes. |
| `serial` | string | Serial number, from udev's ID_SERIAL_SHORT, or else ID_SERIAL. |
| `wwn` | string | World Wide Name, from udev's ID_WWN; empty for a disk without one. |
| `type` | string | Kind of disk: nvme, ssd or hdd. |
| `path` | string | Path of the disk's connection, from udev's ID_PATH. |
| `partitions` | list of Partition | The disk's partitions, by number. |
| `usage` | string | How chalkos uses the disk: "boot" for the boot disk, "disk &lt;name>" for a pinned disk of the node's storage, empty when chalkos does not use it. |

Field types: [Partition](#message-partition).

### DiskReference { #message-diskreference }

A disk by /dev path, or by a selector whose set fields must all match.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `path` | string | Path of the disk below /dev, such as /dev/disk/by-id/…; set without the selector fields. |
| `model` | string | Glob the disk's model matches, such as "Samsung SSD 870*". |
| `serial` | string | The disk's serial number. |
| `wwn` | string | The disk's World Wide Name. |
| `size` | string | Comparison the disk's size meets, such as ">= 1T"; without an operator, the size it equals. |
| `type` | string | Kind of disk: nvme, ssd or hdd. |

### DiskStatus { #message-diskstatus }

A disk of the node's storage, as the boot's storage setup left it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The disk's name in the node's storage: system for the system disk, or the name of the Volume that has the disk to itself. |
| `device` | string | Device node the disk was found at, such as /dev/nvme0n1; empty when it was not found. |
| `error` | string | Why the disk's volumes could not be created or opened at boot; empty when they were. |

### EncryptedObjects { #message-encryptedobjects }

How many objects of one resource etcd holds under one key.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `resource` | string | The resource, such as secrets. |
| `key` | string | The name of the key the objects are encrypted with; empty for objects stored unencrypted. |
| `objects` | uint64 | Number of objects. |

### EtcdMember { #message-etcdmember }

A member of etcd.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | uint64 | The member's ID. |
| `name` | string | The node's name; empty while a member that was added has not started. |
| `peer_urls` | list of string | URLs the other members reach the member at. |
| `learner` | bool | Whether the member is a learner: a member without a vote, as a joining control-plane node is until it catches up. |
| `unhealthy` | string | Why the member did not answer; empty when it is healthy. |

### ImageChunk { #message-imagechunk }

A piece of an image's parts, which follow the header back to back in the header's order.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `data` | bytes | The next bytes of the parts; a chunk may end inside a part or span two. chalkctl sends 1 MiB per chunk. |

### ImageContract { #message-imagecontract }

The contract of the image an installer writes, as its image directory holds it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `system_definitions` | map of string | The image's repart definitions of the system region (ESP, slots A and B, STATE) by file name, which the installer lays out the target disk with. Each image chooses its partition sizes, so the installer's own may not fit. |
| `schemas` | map of bytes | The image's schemas by file name, as its image directory's schemas/ holds them; the config is checked against them. |
| `kubernetes_image` | bytes | The image's image.json; empty for an image without Kubernetes. |

### ImageHeader { #message-imageheader }

An image as install and upgrade send it. Its parts follow the header as chunks, in this order:
the store data, the hash tree, the UKI and, when the header names one, the boot loader, unless
the request's source names where the node pulls them from.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `image_id` | string | The image's ID, as its os-release names it. |
| `root_hash` | bytes | The verity root hash of the image's store. |
| `store` | ImagePart | The store data, which the root hash covers. |
| `hash_tree` | ImagePart | The store's verity hash tree. |
| `uki` | ImagePart | The signed UKI that boots the image. |
| `boot_loader` | ImagePart | The signed systemd-boot, which firmware starts from the ESP's removable-media path. |
| `architecture` | string | The architecture the image runs on, as systemd and os-release name it: x86-64 or arm64. A node refuses an image of another architecture than its own. |
| `platform` | string | The platform the image is built for, as its os-release names it, such as metal or kvm. A node refuses an upgrade to an image of another platform than its own, and the installer translates the config for the platform the image names. |
| `revision` | string | The git revision the image was built from, as its directory's metadata.json names it; empty when the build named none. The node records it to show beside the image's root hash; nothing compares it. |
| `tag` | string | The git tag the image was built from, as its directory's metadata.json names it; empty when the build named none. The node records it as it does revision. |

Field types: [ImagePart](#message-imagepart).

### ImagePart { #message-imagepart }

The size and SHA-256 of a part of an image.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `size` | uint64 | Size in bytes. |
| `sha256` | bytes | SHA-256 of the part's bytes. |

### ImageSource { #message-imagesource }

Where a node takes an image's parts from when they do not follow the header as chunks.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `oci` | RegistryImage, oneof source | A chalkos image in an OCI registry, which the node pulls. |

Field types: [RegistryImage](#message-registryimage).

### InPlace { #message-inplace }

The target of an install in place, which the node's own image runs from its boot disk.

No fields.

### InstallHeader { #message-installheader }

What an install delivers: the node's credentials and config, the target and the image.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node_certificate` | bytes | PEM certificate chalkd serves once the node is installed. |
| `node_key` | bytes | PEM key of node_certificate. |
| `ca_certificate` | bytes | PEM certificates of the OS CAs the node trusts, the one that issues client certificates and the node CA first, then those still trusted while it rotates. |
| `fallback_secret` | string | Recovery key or password enrolled as the second keyslot of every encrypted volume; empty when the node's fallback is none. |
| `in_place` | InPlace, oneof target | Target of a node whose image already runs from its boot disk. |
| `disk` | DiskReference, oneof target | Target disk, which the installer lays out and writes the streamed image to. |
| `image` | ImageHeader | The image the installer writes to the target disk; it must name the boot loader. Installer only. |
| `wipe_disk` | bool | Whether the installer may replace what the target disk holds, including an installed node. |
| `kubernetes_share` | bytes | The node's Kubernetes share (JSON): the CAs and keys for a control-plane node, the CA certificate and a kubelet client certificate for a worker. Empty for an image without Kubernetes. |
| `config` | string | The node's config: YAML documents separated by ---. chalkd checks them as a set against the schemas of the image it installs, fills in their defaults, keeps them on STATE and translates them into what the node applies at boot. Required. |
| `contract` | ImageContract, oneof origin | The contract of an image whose parts follow the header as chunks. Installer only: in place, chalkd checks the config against the running image. |
| `source` | ImageSource, oneof origin | An image in a registry, which the installer pulls. Its contract layer holds the image's contract, and the request carries nothing after the header. Installer only. |

Field types: [InPlace](#message-inplace), [DiskReference](#message-diskreference), [ImageHeader](#message-imageheader), [ImageContract](#message-imagecontract), [ImageSource](#message-imagesource).

### KubernetesStatus { #message-kubernetesstatus }

The state of the node's Kubernetes.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `kind` | string | The node's part in the cluster: controlplane or worker. |
| `state` | string | What the node waits for or is: "no share", "preparing" while its Kubernetes files are prepared, or why their preparation failed, such as "preparation failed: no ipv4 node address matches validSubnets 10.0.0.0/8 (the node has ...)"; then on a control-plane node "waiting for bootstrap or for the cluster at &lt;endpoint>", what joining that cluster does or waits for, such as "joining the cluster at &lt;endpoint>: etcd member &lt;id> catches up" or a stale member and how to remove it, "bootstrapped" or "etcd data missing: restore etcd or reinstall the node"; "joined" on a worker, followed by ": the Node has no &lt;family> pod range; ..." when the control plane allocated it no pod range of one of the worker's families. |
| `phase` | KubernetesPhase | Where the node stands, as state describes it in words. Clients decide on the phase; state is for people to read. |
| `node_ready` | string | The status of the Node's Ready condition (True, False or Unknown), or why it could not be read. |
| `vip` | string | On a bootstrapped control-plane node of a cluster with VIPs: "holder" while the node holds them, "standby" otherwise. Empty on other nodes. |
| `control_plane` | string | On a bootstrapped control-plane node: "current" once its API server and etcd serve the certificates the node holds now and the API server answers ready, otherwise what is not so, such as "the API server serves certificates the node replaced". Empty on other nodes. |
| `control_plane_current` | bool | Whether control_plane is "current": the node's API server and etcd serve the certificates the node holds now and the API server answers ready. False on other nodes. |
| `revoked_nodes` | list of RevokedNode | On a bootstrapped control-plane node: the nodes revoked in the cluster, in name order, as the ConfigMap kube-system/chalkos-revoked-nodes lists them. Empty when none is revoked, and when the list was not reported; revoked_nodes_read tells the two apart. |
| `revoked_nodes_error` | string | On a bootstrapped control-plane node: why the revoked nodes could not be read; empty when they were. |
| `revoked_nodes_read` | bool | Whether the node read the revoked nodes, so that an empty revoked_nodes means none is revoked. False on other nodes, when revoked_nodes_error is set, and from a chalkd that predates node revocation. |

Field types: [KubernetesPhase](#enum-kubernetesphase), [RevokedNode](#message-revokednode).

### NotEtcdMember { #message-notetcdmember }

The error detail of a request that needs an etcd member, from a control-plane node that is
none, as before it joined the cluster or after it left etcd. The request fails with FAILED_PRECONDITION, and another
control-plane node may answer it. EtcdMembers, EtcdRemoveMember, RotationStep, DrainNode,
UncordonNode, RevokeNode and UnrevokeNode give it.

No fields.

### OutputStatus { #message-outputstatus }

The state of one output of the running image.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The output's name, as the image declares it. |
| `hash` | string | The SHA-256 of the names and contents of the files the node serves, in lower-case hex; empty while it serves none, as after a render that failed at boot. |
| `error` | string | Why the output's last render or check failed, at a delivery or at boot; empty once one passed. What the program wrote to standard error is in the node's journal alone. |

### Partition { #message-partition }

A partition of a disk, as sysfs and the udev database describe it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `device` | string | Device node, such as /dev/nvme0n1p3. |
| `number` | uint32 | Partition number. |
| `size` | uint64 | Size in bytes. |
| `type` | string | GPT partition type UUID, in lower case. |
| `label` | string | GPT partition name. |
| `uuid` | string | GPT partition UUID, in lower case. |
| `content` | string | File system or other content blkid found, such as ext4 or crypto_LUKS. |

### RegistryImage { #message-registryimage }

A chalkos image in an OCI registry.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `reference` | string | The image's repository and manifest digest, repository@sha256:…, as chalkctl resolved the inventory's reference. The node pulls that manifest and refuses it unless its config is the image the header describes and its store, hash-tree, UKI and, for an install, boot-loader layers have the header's sizes and SHA-256 sums. An upgrade pulls through the Mirror and authenticates with the Registry documents of the config the request delivers, or without one of the running config; an install through those of the config the request delivers. It tries each endpoint of the Mirror of the reference's host, then the host, each with the Registry of its own host, and pulls anonymously from a host without one. |

### RevokedNode { #message-revokednode }

A node revoked in the cluster.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The node's name in Kubernetes. |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked; unset when the time it is listed with cannot be read. |

### StorageChange { #message-storagechange }

A change a delivery made to the node's storage.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `volume` | string | Name of the volume that changed. |
| `reason` | string | What changed, such as "new volume", "new volume on new disk &lt;disk>", "larger: 200G instead of 100G" or "mounted at /data instead of null". |

### TimeStatus { #message-timestatus }

How the node keeps its clock, as chrony reports it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `synchronised` | bool | Whether chrony synchronises the clock with a server. |
| `source` | string | The server the clock follows. |
| `offset_seconds` | double | How far the clock is from the server's time, in seconds; positive when it is ahead. |
| `error` | string | Why chrony's state could not be read; empty when it was. |

### TrustStatus { #message-truststatus }

What the node trusts of one kind, and what it issues with.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | What is trusted: "OS CA" on every node; "Kubernetes CA" on nodes with Kubernetes; on control-plane nodes also "front-proxy CA", "etcd CA", "service-account keys" and "encryption keys". |
| `fingerprints` | list of string | The SHA-256 fingerprints of what is trusted, in lower-case hex: of certificates and of public keys in DER, and of encryption keys hashed apart from both. The one that issues or signs comes first. |
| `issuing` | string | The fingerprint of the value the node issues, signs or encrypts with; empty where it does none of these. |

### UpgradeHeader { #message-upgradeheader }

What an upgrade delivers: the image and, optionally, the node's config for it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `image` | ImageHeader | The image to install. The node refuses another ID or platform than its own, and an image that names a boot loader: an upgrade leaves the boot loader as it is. |
| `reboot` | bool | Whether the node reboots into the image once it is installed. |
| `config` | string | The node's config for the image, as ApplyConfigRequest.config holds it. The node checks it against the image's schemas and change rules as ApplyConfig does: it refuses what the rules refuse, such as a storage change that takes a reset or a reinstall, or a change of a fixed Kubernetes setting, makes the storage changes they accept, such as a new volume, at once, and keeps the config in the image's config slot. Empty carries the running image's config over, checked against the image the same way. Only the admin role may set it. |
| `fallback_secret` | string | Recovery key or password enrolled on the encrypted volumes the config adds, as ApplyConfigRequest.fallback_secret is; required when it adds one and the node's fallback is not none. Only the admin role may set it. |
| `source` | ImageSource | Where the node takes the image's parts from. Absent, they follow the header as chunks; for an image in a registry, the node pulls them and the request carries nothing after the header. |

Field types: [ImageHeader](#message-imageheader), [ImageSource](#message-imagesource).

### VolumeStatus { #message-volumestatus }

A volume of the node's storage.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The volume's name: var for VAR, or the name of its Volume document. |
| `disk` | string | Name of the disk the volume is on, as DiskStatus.name gives it. |
| `mount_point` | string | Where the config mounts the volume; empty for a volume it leaves unmounted. |
| `present` | bool | Whether the volume's partition exists. |
| `mounted` | bool | Whether the volume is mounted at its mount point. |

## Enums { #enums }

### KubernetesPhase { #enum-kubernetesphase }

Where a node stands in its Kubernetes cluster. On a control-plane node it follows the node's
etcd membership.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `KUBERNETES_PHASE_UNSPECIFIED` | 0 | No phase; chalkd never reports it. |
| `KUBERNETES_PHASE_PREPARING` | 1 | The node has no share yet, or its Kubernetes files are being prepared, or their preparation failed. |
| `KUBERNETES_PHASE_WAITING` | 2 | A control-plane node that is no etcd member and has not started joining. It waits for a bootstrap or for the cluster, or tries to reach the cluster to join it. |
| `KUBERNETES_PHASE_JOINING` | 3 | A control-plane node that started joining the cluster and has not finished. It may be an etcd member already. |
| `KUBERNETES_PHASE_BOOTSTRAPPED` | 4 | A control-plane node that is an etcd member: it initialised etcd or finished joining. |
| `KUBERNETES_PHASE_ETCD_DATA_MISSING` | 5 | A bootstrapped control-plane node whose etcd data is missing, as after VAR was reset. Its control plane does not start until etcd is restored or the node is reinstalled. |
| `KUBERNETES_PHASE_LEFT` | 6 | A control-plane node that left etcd. It joins the cluster again only once reinstalled. |
| `KUBERNETES_PHASE_JOINED` | 7 | A worker node whose Kubernetes files are prepared: its kubelet joins the cluster. |

### Mode { #enum-mode }

The mode chalkd serves the node in.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `MODE_UNSPECIFIED` | 0 | No mode; chalkd never reports it. |
| `MODE_MAINTENANCE` | 1 | The node is not installed: chalkd serves a self-signed certificate. |
| `MODE_NORMAL` | 2 | The node is installed: chalkd serves its node certificate and requires client certificates. |

### RotationStep { #enum-rotationstep }

A step of a CA or key rotation that RotationStep runs.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `ROTATION_STEP_UNSPECIFIED` | 0 | No step; chalkd refuses it. |
| `ROTATION_STEP_RESTART_ADDONS` | 1 | On a bootstrapped control-plane node: restart the Deployments, DaemonSets and StatefulSets of chalkos's add-ons and of the node's Addon documents, once the kube-root-ca.crt ConfigMaps of their namespaces hold every Kubernetes CA the node trusts, and wait until they rolled out. |
| `ROTATION_STEP_LIST_TOKEN_SECRETS` | 2 | On a bootstrapped control-plane node: list the Secrets of type kubernetes.io/service-account-token, whose tokens nothing signs again. |
| `ROTATION_STEP_REWRITE_ENCRYPTED` | 3 | On a bootstrapped control-plane node: update every object of the resources the API server encrypts without changing it, so the API server encrypts it with its first key, then count the objects etcd holds under each key. |
| `ROTATION_STEP_COUNT_ENCRYPTED` | 4 | On a bootstrapped control-plane node: count the objects etcd holds under each key. |
| `ROTATION_STEP_RENEW_KUBELET_SERVING` | 5 | On a node with Kubernetes: remove the kubelet's serving certificate and restart the kubelet, which requests a new one. |

### SecureBoot { #enum-secureboot }

The state of Secure Boot, as the firmware's EFI variables SecureBoot and SetupMode give it.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `SECURE_BOOT_UNSPECIFIED` | 0 | No state; chalkd never reports it. |
| `SECURE_BOOT_DISABLED` | 1 | Secure Boot is off, or its EFI variables cannot be read. |
| `SECURE_BOOT_ENABLED` | 2 | Secure Boot is on: the firmware starts only binaries that db allows. |
| `SECURE_BOOT_SETUP_MODE` | 3 | The firmware accepts new Secure Boot keys without authentication. |

Sources: `api/chalkos/node/v1/node.proto`, rendered by protoc-gen-doc with `api/markdown.tmpl`.
The modes and roles the methods name are those of the permissions in `pkg/chalkd/server.go`.
