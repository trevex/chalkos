---
title: "Node API"
description: "The API chalkd serves on every node, generated from its protobuf definition"
---

# Node API

This page is for authors of clients of the node API, the API chalkd serves on every node; most
readers want [chalkctl](cli/chalkctl.md), its client, instead. chalkd serves the API on TCP port
50000 over TLS, as a Connect service that also answers gRPC and gRPC-Web.

Beside NodeService, which every image serves, chalkd serves the API of each layer the image
carries: KubernetesService on images with the Kubernetes layer, RouterService on images with the
router layer. On an image without the layer their methods fail with UNIMPLEMENTED.

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
may. The API of each layer the image carries, such as chalkos.kubernetes.v1.KubernetesService,
is served beside it.

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
was built for, its layers, the schemas of the config kinds it accepts and its layers' facts,
such as the Kubernetes image.json, which chalkctl checks config against before it delivers
it. The installer refuses it: config is for the image it installs, whose contract comes with
that image. Available in maintenance and normal mode to readers.

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
| `layer_facts` | list of google.protobuf.Any | The facts of the image's layers, such as chalkos.kubernetes.v1.ImageFacts, which config is translated with; empty for an image whose layers have none. |
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

ApplyConfig replaces the config of an installed node, or delivers it payloads of its layers,
such as a Kubernetes share, a node certificate or OS CAs. Destructive storage changes are
refused; additive ones are applied live. With try, the config is on trial: the node rolls it
back by itself, as RollbackConfig does, unless ConfirmConfig confirms it in time. While a
config is on trial, the node refuses every ApplyConfig, an Upgrade and a RollbackImage with
FAILED_PRECONDITION. A trial whose node runs the config from before it again, as after a
rollback at the deadline that failed after it wrote the config, is finished first, by a
delivery of that config, and the change goes ahead; while an Upgrade, a RollbackImage or the
rollback at a trial's deadline holds the lock that delivery needs, the change is refused with
ABORTED. A trial whose node runs neither config ends, and the change goes ahead. A trial
record the node cannot read refuses the change with INTERNAL until RollbackConfig removes it.
Available in normal mode to admins.

Request: ApplyConfigRequest. Response: ApplyConfigResponse.

#### ApplyConfigRequest { #message-applyconfigrequest }

The request of ApplyConfig: a config, or layer payloads, a node certificate or OS CAs to deliver.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `fallback_secret` | string | Recovery key or password enrolled on the encrypted volumes the config adds; required when it adds one and the node's fallback is not none. |
| `node_certificate` | bytes | A new node certificate for the node, followed by the certificate of the node CA that issued it (PEM), which chalkd serves from then on; empty keeps the node's. |
| `node_key` | bytes | PEM key of node_certificate; empty keeps the node's. |
| `trust` | bytes | The OS CAs the node trusts from now on (PEM), as InstallHeader.ca_certificate holds them; empty keeps the node's. They must verify the node's certificate, the caller's and a control plane's node CA. Not together with a node certificate. |
| `config` | string | The node's config, as InstallHeader.config holds it, checked against the running image's schemas. It replaces the node's config as a whole. Empty keeps the node's config and storage, for a request that delivers only layer payloads, a node certificate or OS CAs. |
| `staged` | bool | Whether the config is for the image staged to boot next: chalkd checks it against that image and keeps it in that image's config slot, changing nothing the running image runs but the machine's storage. The config meets that image's change rules, compared with the running slot's config and the machine as for the running image, and the storage changes they accept are made at once, with fallback_secret. It takes the config and the fallback secret alone, and an image staged. |
| `layers` | list of google.protobuf.Any | Payloads of the image's layers, such as a new chalkos.kubernetes.v1.Share replacing the node's, each kept by the layer whose type it is; the node refuses a payload no layer of its image takes. Empty keeps the layers' payloads. |
| `try` | google.protobuf.Duration | How long the config is on trial: the node rolls it back by itself, as RollbackConfig does, unless ConfirmConfig confirms it within the duration, and a boot before that restores the config from before it. A trial takes config for the running image alone, without staged, layer payloads, a node certificate or OS CAs, and lasts at most 24 hours. The node refuses it with FAILED_PRECONDITION for a config that changes the node's storage, which a rollback cannot undo, for the config it runs already and on a node without config. Unset applies the config for good. |
| `ignore_units` | list of string | With try, systemd units the health check that ConfirmConfig waits for leaves out, besides those the image ignores, such as demo.service: a unit that failed before the trial and is not the trial's concern. Each must be a unit name. The node keeps them with the trial. |

#### ApplyConfigResponse { #message-applyconfigresponse }

The response of ApplyConfig: what the delivery changed on the node.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `changes` | list of StorageChange | Storage changes that were applied. |
| `restarted_units` | list of string | Units the delivery restarted, as the image's effects say. |
| `warnings` | list of string | Why the rule of an effect could not be evaluated, without the values it read; the delivery took the effect all the same. |

Field types: [StorageChange](#message-storagechange).

### GetConfig { #method-getconfig }

GetConfig returns the config a slot keeps, the running image's, its previous one or the staged
image's, as it was delivered, with secret values replaced by their hashes. Available in normal
mode to admins.

Request: GetConfigRequest. Response: GetConfigResponse.

#### GetConfigRequest { #message-getconfigrequest }

The request of GetConfig.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `staged` | bool | Whether the response holds the staged image's config instead of the running one's. |
| `previous` | bool | Whether the response holds the running image's previous config, which RollbackConfig delivers again, instead of the one it runs. Not together with staged. |

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
certificates, trust, clock, boot and outputs; each layer's own state is its service's Status.
Available in normal mode to readers.

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
| `certificates` | list of CertificateStatus | Every certificate the node holds or issues, with its expiry and what needs doing about it. |
| `time` | TimeStatus | How the node keeps its clock. |
| `trust` | list of TrustStatus | What the node trusts and issues with, by fingerprint. |
| `boot` | BootStatus | The image the node booted, and what upgrades installed beside it. |
| `platform` | string | The platform the running image was built for. |
| `config_version` | string | SHA-256 of the config the node runs, as it was delivered, in lower-case hex: of its documents in canonical JSON, ordered by apiVersion, kind and name, with each Secret value replaced by its own SHA-256. Empty on a node that runs no config. |
| `staged_config_version` | string | The version of the config kept for the image staged to boot next, as config_version gives the running one's; empty without a staged image or config for it. |
| `outputs` | list of OutputStatus | The image's outputs, by name. |
| `previous_config_version` | string | The version of the running image's previous config, which RollbackConfig delivers again, as config_version gives the running one's; empty while the node keeps none, as after an install or an upgrade. |
| `trial` | ConfigTrial | The config on trial, while one is; unset otherwise. |
| `trial_error` | string | Why the record of a config's trial cannot be read, while it cannot; trial is unset then. The node refuses every other change until RollbackConfig rolls the config back, which removes the record. Empty otherwise. |

Field types: [DiskStatus](#message-diskstatus), [VolumeStatus](#message-volumestatus), [CertificateStatus](#message-certificatestatus), [TimeStatus](#message-timestatus), [TrustStatus](#message-truststatus), [BootStatus](#message-bootstatus), [OutputStatus](#message-outputstatus), [ConfigTrial](#message-configtrial).

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

### Diagnostics { #method-diagnostics }

Diagnostics lists the diagnostics the image declares: read-only commands of the image, such as
ip route get, that RunDiagnostic runs. Available in maintenance and normal mode to readers.

Request: DiagnosticsRequest. Response: DiagnosticsResponse.

#### DiagnosticsRequest { #message-diagnosticsrequest }

The request of Diagnostics.

No fields.

#### DiagnosticsResponse { #message-diagnosticsresponse }

The response of Diagnostics.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `diagnostics` | list of Diagnostic | The image's diagnostics, in name order. |

Field types: [Diagnostic](#message-diagnostic).

### RunDiagnostic { #method-rundiagnostic }

RunDiagnostic runs one of the image's diagnostics with the arguments given and streams what
its command prints on standard output and standard error, then how it ended. It is refused
with PERMISSION_DENIED unless the caller also has the diagnostic's own role, and with
INVALID_ARGUMENT unless each argument is of its parameter's type and every parameter without
a default or optional is given. chalkd runs the command as a transient systemd unit under a
user of its own, on a read-only file system, with no capabilities but those the diagnostic
names, for at most its timeout, and stops a command that prints more than 1 MiB. Four run at
a time. Available in maintenance and normal mode to readers.

Request: RunDiagnosticRequest. Response: a stream of RunDiagnosticResponse.

#### RunDiagnosticRequest { #message-rundiagnosticrequest }

The request of RunDiagnostic.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Name of the diagnostic, as Diagnostics lists it. |
| `arguments` | map of string | The value of each parameter given, by parameter name. |

#### RunDiagnosticResponse { #message-rundiagnosticresponse }

A message of the RunDiagnostic stream: output as the command prints it, then how it ended.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `stdout` | bytes, oneof output | Bytes the command printed on standard output. |
| `stderr` | bytes, oneof output | Bytes the command printed on standard error. |
| `exit` | DiagnosticExit, oneof output | How the command ended; the stream's last message. |

Field types: [DiagnosticExit](#message-diagnosticexit).

### RollbackConfig { #method-rollbackconfig }

RollbackConfig delivers the running image's previous config again: the config the image ran
before the delivery that replaced it, which the node keeps until the next delivery replaces
it in turn. It goes through ApplyConfig's checks and steps: the image's change rules compared
with the config the node runs and the machine, storage changes, outputs and effects, so a
refusal names the rule. The config it replaces becomes the previous one, so a second rollback
undoes the first. Only the config documents return: the OS CAs, the node certificate and the
layers' payloads stay as they are. An upgrade's image starts without a previous config.
During a trial it takes only the version from before the trial, ConfigTrial.previous_version,
rolls the config on trial back at once and ends the trial. Once a rollback at the trial's
deadline wrote that config and failed after, the node runs it already: RollbackConfig then
delivers it again, which finishes the rollback, and ends the trial. A trial record the node
cannot read names no version: RollbackConfig rolls back to the version asked for, or delivers
it again when the node runs it already, and removes the record once that went through. Status
reports no trial then, so once a rollback at the deadline wrote the config from before the
trial, previous_config_version is the config on trial, and config_version is the version that
finishes the rollback. Available in normal mode to admins.

Request: RollbackConfigRequest. Response: RollbackConfigResponse.

#### RollbackConfigRequest { #message-rollbackconfigrequest }

The request of RollbackConfig.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `version` | string | The version of the previous config to deliver again, as StatusResponse.previous_config_version gives it, or during a trial its ConfigTrial.previous_version. The node refuses another with FAILED_PRECONDITION, so a request sent twice rolls back once. Required. |

#### RollbackConfigResponse { #message-rollbackconfigresponse }

The response of RollbackConfig: what delivering the previous config changed, as
ApplyConfigResponse says it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `changes` | list of StorageChange | Storage changes that were applied. |
| `restarted_units` | list of string | Units the delivery restarted, as the image's effects say. |
| `warnings` | list of string | Why the rule of an effect could not be evaluated, without the values it read; the delivery took the effect all the same. |

Field types: [StorageChange](#message-storagechange).

### RollbackImage { #method-rollbackimage }

RollbackImage makes the previous image boot next again: the image an upgrade replaced, whose
store the inactive slot still holds, whose UKI is on the ESP and whose config slot STATE
keeps, until the next upgrade writes the slot. The node writes no store: it checks the image's
UKI as an upgrade checks a new one and renames it to the next generation with the tries it
asks for, so boot counting falls back to the running image should its boots fail. While the
running image's boot has not been found healthy, the name carries no tries instead: the image
stays the node's known-good entry. The image boots with the config it last ran. The node
reboots into it when asked to. Refused with FAILED_PRECONDITION when the node keeps no
previous image, as while an upgrade's image is staged. Available in normal mode to operators.

Request: RollbackImageRequest. Response: RollbackImageResponse.

#### RollbackImageRequest { #message-rollbackimagerequest }

The request of RollbackImage.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `root_hash` | bytes | The verity root hash of the previous image's store, as BootStatus.previous gives it. The node refuses another, and answers already_installed while it runs the image and already_staged while the image boots next, so a request sent twice rolls back once. Required. |
| `reboot` | bool | Whether the node reboots into the image once it boots next. |

#### RollbackImageResponse { #message-rollbackimageresponse }

The response of RollbackImage.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `already_installed` | bool | Whether the node runs the image already, so nothing changed. |
| `entry` | string | The file name of the UKI that boots the image next, with its tries while it has them. |
| `rebooting` | bool | Whether the node reboots into the image. |
| `already_staged` | bool | Whether the image boots next already, from entry, as a rollback without a reboot left it. |

### ConfirmConfig { #method-confirmconfig }

ConfirmConfig ends the trial of a config that ApplyConfig delivered with try, and the node keeps
the config from then on. The node must be healthy as the health check of a boot finds it:
chalkd serves, no unit of the boot failed but those the image ignores, and the image's layers
find their services healthy. It answers UNAVAILABLE while the node is not, and when the trial
changed while the node's health was checked, as when the rollback at the deadline was tried
meanwhile, so a client asks again. It answers FAILED_PRECONDITION when no trial of the version
is open, unless the node runs that version, so a request sent twice confirms once. It answers
FAILED_PRECONDITION too when the node no longer runs the version on trial, and after the
deadline, unless the rollback at the deadline failed: while ConfigTrial.error is set, a
confirmation ends the trial and the rollback's retries. Once such a rollback failed after it
changed the node, it answers FAILED_PRECONDITION all the same: the node keeps trying to finish
the rollback, and RollbackConfig to the trial's previous version finishes it at once. It
answers INTERNAL when the node cannot read the trial's record, which RollbackConfig removes.
Available in normal mode to admins.

Request: ConfirmConfigRequest. Response: ConfirmConfigResponse.

#### ConfirmConfigRequest { #message-confirmconfigrequest }

The request of ConfirmConfig.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `version` | string | The version of the config on trial, as ConfigTrial.version gives it. Required. |

#### ConfirmConfigResponse { #message-confirmconfigresponse }

The response of ConfirmConfig, once the trial ended.

No fields.

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
| `staged` | BootImage | The image an upgrade installed or a rollback made to boot next; unset without one. |
| `failed` | BootImage | The image whose boots used up their tries without being found healthy, from which the node fell back; unset without one. The failed UKI stays until the next upgrade. |
| `journal` | list of string | The last lines chalkd and the health check logged during the failed image's last boot, as the check recorded them on VAR; empty without that record. |
| `error` | string | Why the boot could not be read; empty when it was. The running image's root hash is set when the kernel command line could be read. |
| `previous` | BootImage | The image RollbackImage boots: the one an upgrade replaced, whose UKI is on the ESP and whose config slot STATE keeps; unset without one, as after an install, while another image is staged, or after a fallback. |
| `rollback` | BootImage | The image the last RollbackImage rolled back to, while the node runs it or boots it next; unset otherwise, and once an upgrade replaced it. A client that rolls back several nodes to one image knows by it which of them a rollback got there already. |

Field types: [BootImage](#message-bootimage).

### CertificateStatus { #message-certificatestatus }

A certificate the node holds or issues with.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | What the certificate is: "node" and "OS CA" on every node; a layer adds its own, such as "node CA" on a node that issues node certificates or "kubelet client". |
| `not_after` | google.protobuf.Timestamp | When the certificate expires; unset when the expiry is unknown. |
| `problem` | string | Why a renewal fails, such as "renewal failing: &lt;reason>; expires &lt;date>", or a warning, such as "less than a third of its lifetime remains" or "the node certificate expires on &lt;date>; this image cannot renew it on its own; run chalkctl node renew &lt;node>"; empty when there is nothing to do. A command it advises names the node, or says &lt;node> when the node cannot tell its name. |
| `fingerprint` | string | The SHA-256 fingerprint of the certificate, in lower-case hex. |
| `issuer` | string | The SHA-256 fingerprint of the certificate that issued it, in lower-case hex; empty when the node does not hold that one. |

### ConfigTrial { #message-configtrial }

A config on trial, which the node rolls back by itself at the deadline unless ConfirmConfig
confirms it first.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `version` | string | The version of the config on trial, as StatusResponse.config_version gives it. |
| `previous_version` | string | The version of the config the node ran before, which the rollback delivers again. |
| `deadline` | google.protobuf.Timestamp | When the node rolls the config back, unless it is confirmed first. |
| `error` | string | Why the last attempt to roll the config back failed, which the node tries again each minute: the rollback at the deadline, or a delivery of previous_version, which the node runs already, that finishes a rollback or a trial whose delivery failed before it wrote the config. It also holds why the node could not read the trial's config slot at the deadline, which it tries again as well. Empty while none failed. While it is set, ConfirmConfig still confirms the config and stops the retries, unless rollback_changed is set. |
| `ignore_units` | list of string | The units the trial's health check leaves out besides those the image ignores, as ApplyConfigRequest.ignore_units named them. |
| `rollback_changed` | bool | Whether an attempt to roll the config back failed after it changed the node, as when a rollback at the deadline wrote the config and a restart failed after. A failed delivery of previous_version that finishes a rollback sets it too. ConfirmConfig refuses then; the node keeps trying to finish the rollback each minute, and RollbackConfig to previous_version finishes it at once. |

### Diagnostic { #message-diagnostic }

A diagnostic the image declares.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Name RunDiagnostic takes, such as "route-get". |
| `description` | string | One sentence on what the diagnostic shows. |
| `parameters` | list of DiagnosticParameter | Its parameters: those a call must give, then the others, each in the order its command names them. |
| `role` | string | Least role that runs it: reader, operator or admin. |
| `timeout` | string | How long it may run before systemd stops it, such as "10s". |

Field types: [DiagnosticParameter](#message-diagnosticparameter).

### DiagnosticExit { #message-diagnosticexit }

How a diagnostic's command ended.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `code` | int32 | Its exit status, while stopped is empty. |
| `stopped` | string | Why the command ended without exiting by itself, else empty: "timeout" once it ran longer than the diagnostic's timeout, "output-limit" once it printed more than 1 MiB, of which the stream holds the first 1 MiB, or systemd's result of its unit, such as "signal". |

### DiagnosticParameter { #message-diagnosticparameter }

A parameter of a diagnostic.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Name the arguments of RunDiagnostic give it by. |
| `type` | string | What its value is: address, prefix, hostname, interface, int, enum or pattern. |
| `description` | string | One sentence on the value. |
| `optional` | bool | Whether a call may leave it out. |
| `default_value` | string | The value it takes when a call leaves it out; empty when leaving it out drops the arguments of the command that name it. |
| `min` | int64 or unset | Least value of an int parameter, when it has one. |
| `max` | int64 or unset | Greatest value of an int parameter, when it has one. |
| `values` | list of string | The values of an enum parameter. |
| `regex` | string | The RE2 regular expression a pattern parameter's value matches whole. |

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
| `layer_facts` | list of google.protobuf.Any | The facts of the image's layers, such as chalkos.kubernetes.v1.ImageFacts; empty for an image whose layers have none. |

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
| `config` | string | The node's config: YAML documents separated by ---. chalkd checks them as a set against the schemas of the image it installs, fills in their defaults, keeps them on STATE and translates them into what the node applies at boot. Required. |
| `contract` | ImageContract, oneof origin | The contract of an image whose parts follow the header as chunks. Installer only: in place, chalkd checks the config against the running image. |
| `source` | ImageSource, oneof origin | An image in a registry, which the installer pulls. Its contract layer holds the image's contract, and the request carries nothing after the header. Installer only. |
| `layers` | list of google.protobuf.Any | Payloads of the installed image's layers, such as chalkos.kubernetes.v1.Share, which the node keeps on STATE. The node refuses a payload no layer takes. |

Field types: [InPlace](#message-inplace), [DiskReference](#message-diskreference), [ImageHeader](#message-imageheader), [ImageContract](#message-imagecontract), [ImageSource](#message-imagesource).

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
| `name` | string | What is trusted: "OS CA" on every node; a layer adds what it trusts, such as "Kubernetes CA" on a node with Kubernetes. |
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

### Mode { #enum-mode }

The mode chalkd serves the node in.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `MODE_UNSPECIFIED` | 0 | No mode; chalkd never reports it. |
| `MODE_MAINTENANCE` | 1 | The node is not installed: chalkd serves a self-signed certificate. |
| `MODE_NORMAL` | 2 | The node is installed: chalkd serves its node certificate and requires client certificates. |

### SecureBoot { #enum-secureboot }

The state of Secure Boot, as the firmware's EFI variables SecureBoot and SetupMode give it.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `SECURE_BOOT_UNSPECIFIED` | 0 | No state; chalkd never reports it. |
| `SECURE_BOOT_DISABLED` | 1 | Secure Boot is off, or its EFI variables cannot be read. |
| `SECURE_BOOT_ENABLED` | 2 | Secure Boot is on: the firmware starts only binaries that db allows. |
| `SECURE_BOOT_SETUP_MODE` | 3 | The firmware accepts new Secure Boot keys without authentication. |

The definition is `api/chalkos/kubernetes/v1/kubernetes.proto`, package `chalkos.kubernetes.v1`.

## KubernetesService { #kubernetesservice }

KubernetesService is the API of the node's Kubernetes side. Each method's comment names the
modes it is available in and the least role a client certificate needs, as on NodeService.
Only nodes call RenewNodeCertificate, with the node role. On an image without the Kubernetes
layer every method fails with UNIMPLEMENTED, before the mode and the role are checked.

### Status { #kubernetes-method-status }

Status reports the state of the node's Kubernetes: its part in the cluster, its preparation,
its etcd membership, its Node, VIPs, control plane and the revoked nodes. Available in normal
mode to readers, on images with the Kubernetes layer.

Request: StatusRequest. Response: StatusResponse.

#### StatusRequest { #kubernetes-message-statusrequest }

The request of Status.

No fields.

#### StatusResponse { #kubernetes-message-statusresponse }

The response of Status: the state of the node's Kubernetes.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `kind` | string | The node's part in the cluster: controlplane or worker. |
| `state` | string | What the node waits for or is: "no share", "preparing" while its Kubernetes files are prepared, or why their preparation failed, such as "preparation failed: no ipv4 node address matches validSubnets 10.0.0.0/8 (the node has ...)"; then on a control-plane node "waiting for bootstrap or for the cluster at &lt;endpoint>", what joining that cluster does or waits for, such as "joining the cluster at &lt;endpoint>: etcd member &lt;id> catches up" or a stale member and how to remove it, "bootstrapped" or "etcd data missing: restore etcd or reinstall the node"; "joined" on a worker, followed by ": the Node has no &lt;family> pod range; ..." when the control plane allocated it no pod range of one of the worker's families. |
| `phase` | Phase | Where the node stands, as state describes it in words. Clients decide on the phase; state is for people to read. |
| `node_ready` | string | The status of the Node's Ready condition (True, False or Unknown), or why it could not be read. |
| `vip` | string | On a bootstrapped control-plane node of a cluster with VIPs: "holder" while the node holds them, "standby" otherwise. Empty on other nodes. |
| `control_plane` | string | On a bootstrapped control-plane node: "current" once its API server and etcd serve the certificates the node holds now and the API server answers ready, otherwise what is not so, such as "the API server serves certificates the node replaced". Empty on other nodes. |
| `control_plane_current` | bool | Whether control_plane is "current": the node's API server and etcd serve the certificates the node holds now and the API server answers ready. False on other nodes. |
| `revoked_nodes` | list of RevokedNode | On a bootstrapped control-plane node: the nodes revoked in the cluster, in name order, as the ConfigMap kube-system/chalkos-revoked-nodes lists them. Empty when none is revoked, and when the list was not reported; revoked_nodes_read tells the two apart. |
| `revoked_nodes_error` | string | On a bootstrapped control-plane node: why the revoked nodes could not be read; empty when they were. |
| `revoked_nodes_read` | bool | Whether the node read the revoked nodes, so that an empty revoked_nodes means none is revoked. False on other nodes and when revoked_nodes_error is set. |

Field types: [Phase](#kubernetes-enum-phase), [RevokedNode](#kubernetes-message-revokednode).

### Bootstrap { #kubernetes-method-bootstrap }

Bootstrap initialises etcd on a control-plane node, starts the control plane and applies
chalkos's add-ons and the objects of the node's Addon documents. It is refused on a node that
is bootstrapped or holds etcd data, so no second cluster is ever initialised. Available in
normal mode to admins, on images with the Kubernetes layer.

Request: BootstrapRequest. Response: BootstrapResponse.

#### BootstrapRequest { #kubernetes-message-bootstraprequest }

The request of Bootstrap.

No fields.

#### BootstrapResponse { #kubernetes-message-bootstrapresponse }

The response of Bootstrap, once the add-ons are applied.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `applied` | uint32 | Objects applied: chalkos's add-ons and those of the node's Addon documents. |

### EtcdMembers { #kubernetes-method-etcdmembers }

EtcdMembers lists etcd's members as the node's own member sees them, with their health. It
answers on control-plane nodes that are etcd members. Available in normal mode to readers, on
images with the Kubernetes layer.

Request: EtcdMembersRequest. Response: EtcdMembersResponse.

#### EtcdMembersRequest { #kubernetes-message-etcdmembersrequest }

The request of EtcdMembers.

No fields.

#### EtcdMembersResponse { #kubernetes-message-etcdmembersresponse }

The response of EtcdMembers.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `members` | list of EtcdMember | etcd's members, as etcd lists them. |

Field types: [EtcdMember](#kubernetes-message-etcdmember).

### EtcdRemoveMember { #kubernetes-method-etcdremovemember }

EtcdRemoveMember removes another node's etcd member. It is refused when the voters left would
have fewer healthy members than their quorum, unless forced, and for the node's own member,
which EtcdLeave removes. Available in normal mode to admins, on images with the Kubernetes
layer.

Request: EtcdRemoveMemberRequest. Response: EtcdRemoveMemberResponse.

#### EtcdRemoveMemberRequest { #kubernetes-message-etcdremovememberrequest }

The request of EtcdRemoveMember.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `member` | string | The member's name or its ID in hex. |
| `force` | bool | Whether the member is removed even when the voters left would have no healthy quorum. |

#### EtcdRemoveMemberResponse { #kubernetes-message-etcdremovememberresponse }

The response of EtcdRemoveMember.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `removed` | EtcdMember | The member that was removed, without its health. |

Field types: [EtcdMember](#kubernetes-message-etcdmember).

### EtcdLeave { #kubernetes-method-etcdleave }

EtcdLeave takes the node out of etcd: it releases the VIPs, removes its own member, if etcd
still has it, with the same quorum guard, stops its control plane, deletes its etcd data and
unpins its addresses. The node joins the cluster again only once it is reinstalled. A
bootstrapped node whose own member does not answer leaves only when forced. Available in
normal mode to admins, on images with the Kubernetes layer.

Request: EtcdLeaveRequest. Response: EtcdLeaveResponse.

#### EtcdLeaveRequest { #kubernetes-message-etcdleaverequest }

The request of EtcdLeave.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `force` | bool | Whether the node leaves through the other members also when its own etcd member does not answer, as on a node that lost its pinned address. |

#### EtcdLeaveResponse { #kubernetes-message-etcdleaveresponse }

The response of EtcdLeave, once the node is out of etcd.

No fields.

### RenewNodeCertificate { #kubernetes-method-renewnodecertificate }

RenewNodeCertificate issues the calling node a node certificate from the node CA, for exactly
the names of the node certificate it authenticated with. It answers on control-plane nodes
and refuses a revoked node. Available in normal mode to nodes, on images with the Kubernetes
layer.

Request: RenewNodeCertificateRequest. Response: RenewNodeCertificateResponse.

#### RenewNodeCertificateRequest { #kubernetes-message-renewnodecertificaterequest }

The request of RenewNodeCertificate.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_request` | bytes | PKCS #10 certificate request (DER) signed with the node's new key, which never leaves the node. Only its key is used; the names come from the caller's certificate. |

#### RenewNodeCertificateResponse { #kubernetes-message-renewnodecertificateresponse }

The response of RenewNodeCertificate.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `certificate_chain` | bytes | The new node certificate followed by the certificate of the node CA that issued it (PEM). |

### RotationStep { #kubernetes-method-rotationstep }

RotationStep runs a step of a CA or key rotation that the node's Kubernetes side carries
out; chalkctl rotate calls it. Available in normal mode to admins, on images with the
Kubernetes layer.

Request: RotationStepRequest. Response: RotationStepResponse.

#### RotationStepRequest { #kubernetes-message-rotationsteprequest }

The request of RotationStep.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `step` | RotationStep | The step to run. |

Field types: [RotationStep](#kubernetes-enum-rotationstep).

#### RotationStepResponse { #kubernetes-message-rotationstepresponse }

The response of RotationStep; each step sets the fields it reports.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `restarted` | list of string | The workloads restarted, as namespace/kind/name. |
| `token_secrets` | list of string | The Secrets of type kubernetes.io/service-account-token, as namespace/name. |
| `rewritten` | uint64 | How many objects were updated. |
| `encrypted` | list of EncryptedObjects | The objects etcd holds by resource and key. |
| `not_waited` | list of string | The workloads restarted but not waited for, as namespace/kind/name: their rollout does not complete by itself (an OnDelete update strategy, a StatefulSet's partition, a paused Deployment). |

Field types: [EncryptedObjects](#kubernetes-message-encryptedobjects).

### DrainNode { #kubernetes-method-drainnode }

DrainNode cordons a node of the cluster for an upgrade and evicts its pods through the
eviction API, which keeps them within their PodDisruptionBudgets. DaemonSet pods, static pods
and pods without a controller stay; they run again on the node once it is back. Pods with
emptyDir volumes are evicted only when the request accepts losing that data. Pods that
terminate already, as those an earlier drain evicted, are not evicted again, but the call
returns only once they and the pods it evicted have stopped. A node that was cordoned already
stays cordoned and unmarked; one this call cordons is marked chalkos.dev/upgrade-cordon. It
answers on bootstrapped control-plane nodes. Available in normal mode to operators, on images
with the Kubernetes layer. An eviction refused for good, or one a PodDisruptionBudget holds
back until the timeout, fails the call with FAILED_PRECONDITION; pods that did not stop in
time, or whose evictions the API server kept failing, with DEADLINE_EXCEEDED.

Request: DrainNodeRequest. Response: DrainNodeResponse.

#### DrainNodeRequest { #kubernetes-message-drainnoderequest }

The request of DrainNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |
| `timeout_seconds` | uint32 | How long to wait for the pods to be evicted and gone, in seconds; zero waits five minutes. |
| `delete_emptydir_data` | bool | Whether pods with emptyDir volumes are evicted too, which loses their data. |

#### DrainNodeResponse { #kubernetes-message-drainnoderesponse }

The response of DrainNode: the pods evicted and the pods that stay.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `marked` | bool | Whether the node is marked as cordoned for an upgrade, by this call or an earlier one. |
| `evicted` | list of string | The pods evicted, as namespace/name. |
| `kept` | list of string | The DaemonSet, static and finished pods, which stay on the node, as namespace/name. |
| `unmanaged` | list of string | The pods without a controller, which stay and which nothing starts again elsewhere, as namespace/name. The node lifecycle controller deletes them for good when the node stays down longer than their tolerations allow. |

### UncordonNode { #kubernetes-method-uncordonnode }

UncordonNode makes a node DrainNode cordoned schedulable again and removes the mark; it leaves
a node cordoned otherwise as it is. It answers on bootstrapped control-plane nodes. Available
in normal mode to operators, on images with the Kubernetes layer.

Request: UncordonNodeRequest. Response: UncordonNodeResponse.

#### UncordonNodeRequest { #kubernetes-message-uncordonnoderequest }

The request of UncordonNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |

#### UncordonNodeResponse { #kubernetes-message-uncordonnoderesponse }

The response of UncordonNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `uncordoned` | bool | Whether the node was cordoned for an upgrade and is schedulable again. |

### RevokeNode { #kubernetes-method-revokenode }

RevokeNode cuts a node off the cluster by name: it lists the node in the ConfigMap
kube-system/chalkos-revoked-nodes, applies the API server's admission policy
chalkos-revoked-nodes, which then denies the node's writes, and deletes its Node object.
Control planes then refuse to renew its node certificate. A node revoked already keeps the
time of its first revocation. A call that fails after listing the node says so; calling again
finishes it. A node not revoked yet is refused with FAILED_PRECONDITION while the policy's
map of revoked nodes would grow too long for the API server to compile with it. It answers on
bootstrapped control-plane nodes. Available in normal mode to admins, on images with the
Kubernetes layer.

Request: RevokeNodeRequest. Response: RevokeNodeResponse.

#### RevokeNodeRequest { #kubernetes-message-revokenoderequest }

The request of RevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes, which its node certificate carries too. |

#### RevokeNodeResponse { #kubernetes-message-revokenoderesponse }

The response of RevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked: now, or at an earlier revocation. |
| `node_deleted` | bool | Whether the call deleted the node's Node object; false when the cluster had none. |

### UnrevokeNode { #kubernetes-method-unrevokenode }

UnrevokeNode removes a node from the revoked nodes and applies the admission policy without
it, so a node of that name may join again. The old node's certificates work again too, unless
the CAs they chain to were rotated after the revocation: the call is refused with
FAILED_PRECONDITION while the OS CA or the Kubernetes CA predates it, unless forced. A call
that fails after removing the node says so; calling again applies the policy. It answers on
bootstrapped control-plane nodes. Available in normal mode to admins, on images with the
Kubernetes layer.

Request: UnrevokeNodeRequest. Response: UnrevokeNodeResponse.

#### UnrevokeNodeRequest { #kubernetes-message-unrevokenoderequest }

The request of UnrevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `node` | string | The node's name in Kubernetes. |
| `force` | bool | Whether the node is unrevoked although the CAs its old certificates chain to predate the revocation. |

#### UnrevokeNodeResponse { #kubernetes-message-unrevokenoderesponse }

The response of UnrevokeNode.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked; unset when the time it is listed with cannot be read, which counts every CA as made before the revocation. |
| `stale_cas` | list of string | The CAs issued before the revocation, by the names chalkctl rotate takes: os-ca, kubernetes-ca. The old node's certificates work again until they expire or these CAs are rotated. Empty when both were rotated after the revocation. |

## Messages of chalkos.kubernetes.v1 { #kubernetes-messages }

These messages are held by fields of other messages.

### EncryptedObjects { #kubernetes-message-encryptedobjects }

How many objects of one resource etcd holds under one key.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `resource` | string | The resource, such as secrets. |
| `key` | string | The name of the key the objects are encrypted with; empty for objects stored unencrypted. |
| `objects` | uint64 | Number of objects. |

### EtcdMember { #kubernetes-message-etcdmember }

A member of etcd.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | uint64 | The member's ID. |
| `name` | string | The node's name; empty while a member that was added has not started. |
| `peer_urls` | list of string | URLs the other members reach the member at. |
| `learner` | bool | Whether the member is a learner: a member without a vote, as a joining control-plane node is until it catches up. |
| `unhealthy` | string | Why the member did not answer; empty when it is healthy. |

### ImageFacts { #kubernetes-message-imagefacts }

The Kubernetes facts of an image, as its contract carries them in its layer facts.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `image_json` | bytes | The image's image.json: the Kubernetes release, the images of its components and the pod network it ships. |

### NotEtcdMember { #kubernetes-message-notetcdmember }

The error detail of a request that needs an etcd member, from a control-plane node that is
none, as before it joined the cluster or after it left etcd. The request fails with FAILED_PRECONDITION, and another
control-plane node may answer it. EtcdMembers, EtcdRemoveMember, RotationStep, DrainNode,
UncordonNode, RevokeNode and UnrevokeNode give it.

No fields.

### RevokedNode { #kubernetes-message-revokednode }

A node revoked in the cluster.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The node's name in Kubernetes. |
| `revoked_at` | google.protobuf.Timestamp | When the node was revoked; unset when the time it is listed with cannot be read. |

### Share { #kubernetes-message-share }

A node's Kubernetes share, as ApplyConfig and Install deliver it in their layers.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `share` | bytes | The share (JSON): the CAs and keys for a control-plane node, the CA certificate and a kubelet client certificate for a worker. |

## Enums of chalkos.kubernetes.v1 { #kubernetes-enums }

### Phase { #kubernetes-enum-phase }

Where a node stands in its Kubernetes cluster. On a control-plane node it follows the node's
etcd membership.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `PHASE_UNSPECIFIED` | 0 | No phase; chalkd never reports it. |
| `PHASE_PREPARING` | 1 | The node has no share yet, or its Kubernetes files are being prepared, or their preparation failed. |
| `PHASE_WAITING` | 2 | A control-plane node that is no etcd member and has not started joining. It waits for a bootstrap or for the cluster, or tries to reach the cluster to join it. |
| `PHASE_JOINING` | 3 | A control-plane node that started joining the cluster and has not finished. It may be an etcd member already. |
| `PHASE_BOOTSTRAPPED` | 4 | A control-plane node that is an etcd member: it initialised etcd or finished joining. |
| `PHASE_ETCD_DATA_MISSING` | 5 | A bootstrapped control-plane node whose etcd data is missing, as after VAR was reset. Its control plane does not start until etcd is restored or the node is reinstalled. |
| `PHASE_LEFT` | 6 | A control-plane node that left etcd. It joins the cluster again only once reinstalled. |
| `PHASE_JOINED` | 7 | A worker node whose Kubernetes files are prepared: its kubelet joins the cluster. |

### RotationStep { #kubernetes-enum-rotationstep }

A step of a CA or key rotation that RotationStep runs.

| Name | Number | Description |
| ---- | ------ | ----------- |
| `ROTATION_STEP_UNSPECIFIED` | 0 | No step; chalkd refuses it. |
| `ROTATION_STEP_RESTART_ADDONS` | 1 | On a bootstrapped control-plane node: restart the Deployments, DaemonSets and StatefulSets of chalkos's add-ons and of the node's Addon documents, once the kube-root-ca.crt ConfigMaps of their namespaces hold every Kubernetes CA the node trusts, and wait until they rolled out. |
| `ROTATION_STEP_LIST_TOKEN_SECRETS` | 2 | On a bootstrapped control-plane node: list the Secrets of type kubernetes.io/service-account-token, whose tokens nothing signs again. |
| `ROTATION_STEP_REWRITE_ENCRYPTED` | 3 | On a bootstrapped control-plane node: update every object of the resources the API server encrypts without changing it, so the API server encrypts it with its first key, then count the objects etcd holds under each key. |
| `ROTATION_STEP_COUNT_ENCRYPTED` | 4 | On a bootstrapped control-plane node: count the objects etcd holds under each key. |
| `ROTATION_STEP_RENEW_KUBELET_SERVING` | 5 | On a node with Kubernetes: remove the kubelet's serving certificate and restart the kubelet, which requests a new one. |

The definition is `api/chalkos/router/v1/router.proto`, package `chalkos.router.v1`.

## RouterService { #routerservice }

RouterService is the API of the node's router side. Each method's comment names the modes it is
available in and the least role a client certificate needs, as on NodeService. On an image
without the router layer every method fails with UNIMPLEMENTED, before the mode and the role are
checked.

### Status { #router-method-status }

Status reports the state of the router's routing daemons, its NAT64 instances and its
resolver. Available in normal mode to readers, on images with the router layer.

Request: StatusRequest. Response: StatusResponse.

#### StatusRequest { #router-message-statusrequest }

The request of Status.

No fields.

#### StatusResponse { #router-message-statusresponse }

The response of Status: the state of a router's routing daemons, its NAT64 instances and its
resolver.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `peers` | list of BGPPeerStatus | The router's BGP sessions, by VRF, the default first, then by BGPPeer document and by interface or address. |
| `bgp_error` | string | Why FRR's BGP state could not be read, such as vtysh's error; empty when it was. peers is then empty. |
| `nat64` | list of NAT64Status | The router's NAT64 instances, one per Jool instance, which a NAT64 document names, by name. |
| `nat64_error` | string | Why Jool's state could not be read, such as jool's error; empty when it was. nat64 is then empty. |
| `resolver` | ResolverStatus | The resolver of the router's default VRF; unset on a router without one. |
| `vrf_resolvers` | list of ResolverStatus | The resolvers of the router's VRFs, one per VRF with a Resolver document, by VRF. |

Field types: [BGPPeerStatus](#router-message-bgppeerstatus), [NAT64Status](#router-message-nat64status), [ResolverStatus](#router-message-resolverstatus).

### Routes { #router-method-routes }

Routes lists the routes in the node's kernel, of the main table unless a VRF or another table
is asked for, and the paths the BGP instance of that VRF selected, each within an address
range when one is given. Available in normal mode to readers, on images with the router
layer.

Request: RoutesRequest. Response: RoutesResponse.

#### RoutesRequest { #router-message-routesrequest }

The request of Routes.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `prefix` | string | An address range, such as 2001:db8::/32: only the routes to it and to ranges within it. Empty for every route. |
| `protocol` | string | Only the kernel's routes of this protocol: kernel, boot, static, ra, dhcp, bgp or another the kernel names, or its number. BGP's paths are listed for bgp and for an empty protocol alone. |
| `table` | string | The kernel's routing table: main, local, default, all, or a table's number. Empty for main, or for the table of vrf. |
| `vrf` | string | The VRF whose routes to list, by the name of its VRF document, or default: the routes of its table, which the node's running VRF document gives, and the paths of its BGP instance. Empty for the default VRF. A request gives vrf or table, not both. |

#### RoutesResponse { #router-message-routesresponse }

The response of Routes.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `kernel` | list of KernelRoute | The kernel's routes, in the order of their tables, families and destinations. |
| `bgp` | list of BGPRoute | The paths BGP selected and installs, in the order of their families and prefixes. |
| `bgp_error` | string | Why BGP's paths could not be read, such as vtysh's error; empty when they were. |
| `vrf` | string | The VRF whose routes these are: the request's vrf, or default for a request without one. A node whose image does not know VRFs leaves it empty and lists the main table and the default instance's paths, whatever the request's vrf. |

Field types: [KernelRoute](#router-message-kernelroute), [BGPRoute](#router-message-bgproute).

## Messages of chalkos.router.v1 { #router-messages }

These messages are held by fields of other messages.

### BGPPeerStatus { #router-message-bgppeerstatus }

One BGP session of a router: a BGPPeer's address, or one of its interfaces.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Name of the BGPPeer document. |
| `neighbor` | string | The interface the session runs on, for a peer of interface or interfaces, or the neighbour's address. |
| `state` | string | The session's BGP state: Idle, Connect, Active, OpenSent, OpenConfirm or Established. |
| `uptime_seconds` | uint64 | How long the session has been established, in seconds; 0 while it is not. |
| `prefixes_received` | uint32 | Routes the router accepted from the neighbour, of every family. |
| `prefixes_sent` | uint32 | Routes the router sends the neighbour, of every family. |
| `bfd` | string | The state of the session's BFD: up, down, admin-down or unknown; empty for a peer without bfd. |
| `vrf` | string | The VRF the session runs in: the name of its VRF document, or default. |

### BGPRoute { #router-message-bgproute }

A path of BGP's for a prefix, which its best path selection took.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `prefix` | string | The prefix, such as ::/0. |
| `as_path` | string | The ASes of the path, separated by spaces; empty for a route the router originates. |
| `next_hops` | list of string | The next hop addresses FRR resolved for the path. |
| `peer` | string | The neighbour the path came from: its interface, for a session of an interface, or its address; empty for a route the router originates. |
| `local_pref` | uint32 or unset | The path's local preference, when it carries one. |
| `med` | uint32 | The path's multi-exit discriminator. |
| `communities` | list of string | The path's standard and large communities. |
| `best` | bool | Whether it is the best path; otherwise one of the equal paths BGP installs beside it. |
| `imported_from` | string | The VRF whose BGP instance the path came from, through the importing instance's spec.import, or the route distinguisher it came from when FRR names no VRF; empty for a path of the instance's own peers or networks. |

### KernelRoute { #router-message-kernelroute }

A route in the kernel's routing tables.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `destination` | string | Destination address range, such as ::/0. |
| `type` | string | The route's type: unicast, local, broadcast, anycast, multicast, blackhole, unreachable, prohibit or throw, or its number. |
| `protocol` | string | What installed it: kernel, boot, static, ra, dhcp, bgp and others the kernel names, or its number. |
| `table` | uint32 | The routing table, by number: 254 is main, 255 local and 253 default. |
| `metric` | uint32 | The route's metric. |
| `next_hops` | list of NextHop | Where the route leads; several for a route over equal-cost paths. |

Field types: [NextHop](#router-message-nexthop).

### NAT64PoolStatus { #router-message-nat64poolstatus }

A NAT64 instance's pool of one protocol.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `protocol` | string | The protocol: tcp, udp or icmp. |
| `size` | uint64 | IPv4 addresses and ports, or ICMP identifiers, the pool holds for the protocol; 0 when it holds none. An instance whose pool is empty takes the router's own addresses. |

### NAT64Status { #router-message-nat64status }

One NAT64 instance of a router, as Jool reports it.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | Name of the NAT64 document, which names its instance. |
| `sessions` | uint64 | Connections the instance translates, of all protocols. |
| `bindings` | uint64 | IPv4 addresses and ports of the pool that translated connections hold, of all protocols: one per IPv6 address and port, or ICMP identifier, that the instance translates. |
| `pools` | list of NAT64PoolStatus | The instance's pool for TCP, UDP and ICMP, in this order. |

Field types: [NAT64PoolStatus](#router-message-nat64poolstatus).

### NextHop { #router-message-nexthop }

A next hop of a kernel route.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `gateway` | string | The gateway's address, which may be of the other family; empty for a route on the link alone. |
| `interface` | string | The interface the route leaves through. |
| `weight` | uint32 | The next hop's weight among several, from 1; 0 for a route of one next hop. |

### ResolverStatus { #router-message-resolverstatus }

The state of a router's resolver.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `running` | bool | Whether Unbound runs: the default VRF's answers on its control socket, and a VRF's unit is active. |
| `error` | string | Why Unbound does not run, such as unbound-control's error or the state of a VRF's unit; empty while it runs. |
| `vrf` | string | The VRF whose resolver this is; empty for the default VRF's. |

Sources: `api/chalkos/node/v1/node.proto`, `api/chalkos/kubernetes/v1/kubernetes.proto` and
`api/chalkos/router/v1/router.proto`, rendered by protoc-gen-doc with `api/markdown.tmpl`. The
modes and roles the methods name are those of the permissions in `pkg/chalkd/server.go` and of
each layer's plugin in `pkg/chalkd/layers`.
