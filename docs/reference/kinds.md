---
title: "Config kinds"
description: "The fields of the config kinds of chalkos's images, of the inventory and of the lab file"
---

# Config kinds

This page is for operators who write a node's config documents, an inventory or a lab file. It
lists the config kinds that an image of `chalkos.lib.mkImages`'s default variant accepts, and
those a router image adds, by API group, and the fields of the inventory and the lab file that
chalkctl and chalklab read. An image whose modules declare kinds of their own under
[`chalkos.kinds`](extension-api.md#chalkoskinds) accepts those as well.

Every document has an `apiVersion`, a `kind`, a `metadata.name` of lower-case letters, digits,
`-` and `.`, and a `spec`, whose fields the tables list. A field within another is listed by its
whole path, where `<name>` stands for each key of a map and `[]` for each item of a list:
`spec.vip.addresses` is the field `addresses` of the object `spec.vip`. An object has a row of
its own only when it has a description, may be null or must be set; an object a document leaves
out takes the defaults of its fields. A default that reads "required" means the field must be
set, and `—` means it has no default. A field the tables do not list is refused, except within a
Kubernetes object or an object whose type reads "object, any fields".

## chalkos.dev { #chalkos-dev }

The kinds of `chalkos.dev` set the node's host name and hold the secrets that other documents name.

### Host { #host }

The host name the node sets at boot.

`apiVersion: chalkos.dev/v1alpha1`

A node's config holds at most one.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.hostname` | string or null | `null` | Host name set at boot; null takes the node's name in the inventory. |

### Secret { #secret }

Secret values that other documents name by reference, such as a Registry's password.

`apiVersion: chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.data` | map of string | required | Secret values by key, which secretRef fields of other documents name. |

## kubernetes.chalkos.dev { #kubernetes-chalkos-dev }

The kinds of `kubernetes.chalkos.dev` set up the Kubernetes cluster and the node's part in it. Only images that run Kubernetes accept them, and Flannel only those whose CNI is flannel.

### Addon { #addon }

Kubernetes objects that the control planes apply to the cluster.

`apiVersion: kubernetes.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.objects` | list of Kubernetes object | required | Kubernetes objects the control planes apply with server-side apply after chalkos's own, in this order; the objects of several Addon documents in the order of their names. chalkctl delivers them to control planes alone, and a worker applies none. |

### Cluster { #cluster }

The Kubernetes cluster the node belongs to: its API server's endpoint, its address families and ranges, its VIP and the flags of its control-plane components.

`apiVersion: kubernetes.chalkos.dev/v1alpha1`

A node's config holds at most one. It has 5 [change rules](builtins.md#rules-cluster).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.allowSchedulingOnControlPlanes` | boolean | `false` | Whether workloads run on control-plane nodes, which otherwise register with the taint `node-role.kubernetes.io/control-plane:NoSchedule`; it is fixed once the node holds a Kubernetes share. |
| `spec.dnsIPs.ipv4` | string or null | `null` | Service address of the cluster DNS in the family's service range; null is its 10th address. Pods use the primary family's. |
| `spec.dnsIPs.ipv6` | string or null | `null` | Service address of the cluster DNS in the family's service range; null is its 10th address. Pods use the primary family's. |
| `spec.domain` | string | `"cluster.local"` | DNS domain of the cluster. |
| `spec.endpoint` | string | required | URL of the cluster's Kubernetes API server, which every node of the cluster names alike. |
| `spec.extraArgs.etcd` | map of string | `{}` | Extra flags of etcd, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.extraArgs.kube-apiserver` | map of string | `{}` | Extra flags of the API server, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.extraArgs.kube-controller-manager` | map of string | `{}` | Extra flags of the controller-manager, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.extraArgs.kube-scheduler` | map of string | `{}` | Extra flags of the scheduler, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.ipFamilies` | list of one of `ipv4`, `ipv6` | `["ipv4"]` | Address families of the cluster, the primary one first, each once. They are fixed when the cluster is created. |
| `spec.nodeCIDRMaskSizes.ipv4` | integer from 1 to 128 | `24` | Prefix length of each node's part of the family's pod range. |
| `spec.nodeCIDRMaskSizes.ipv6` | integer from 1 to 128 | `64` | Prefix length of each node's part of the family's pod range. |
| `spec.nodeIP.timeout` | integer of at least 1 | `300` | Seconds a node waits at boot for its address. |
| `spec.nodeIP.validSubnets` | list of string | `[]` | Subnets in CIDR notation that nodes without a fixed address pick theirs from at boot, unless a Node gives its own; a leading `!` excludes one. Empty takes any global unicast address. |
| `spec.podCIDRs.ipv4` | string | `"10.244.0.0/16"` | Address range of the family's pods; each node gets a part of it. |
| `spec.podCIDRs.ipv6` | string | `"fd00:10:244::/56"` | Address range of the family's pods; each node gets a part of it. |
| `spec.serviceCIDRs.ipv4` | string | `"10.96.0.0/12"` | Address range of the family's services, at most 2^20 addresses. |
| `spec.serviceCIDRs.ipv6` | string | `"fd00:10:96::/112"` | Address range of the family's services, at most 2^20 addresses. |
| `spec.vip.addresses` | list of string | `[]` | Virtual addresses of the API server, at most one per family; empty means no virtual IP. |
| `spec.vip.mode` | `l2` | `"l2"` | Way the node holding the VIP announces its addresses: `l2` adds them to an interface and announces them to the neighbours with gratuitous ARP and unsolicited neighbour advertisements. |

### Flannel { #flannel }

The settings of flannel, the cluster's CNI, for the VXLAN between the nodes.

`apiVersion: kubernetes.chalkos.dev/v1alpha1`

A node's config holds at most one.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.mtu` | integer of at least 1 or null | `null` | MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN devices and the pods get 50 less. null takes the MTU of the interface holding the node's address. |
| `spec.vxlanSourceSubnets` | list of string | `[]` | Ranges flannel's VXLAN must come from, one of each family at least; empty takes VXLAN from any source. |

### Node { #node }

The node's part in the Kubernetes cluster: its type, the labels and taints it registers with, its address, the interface of the VIP and its kubelet's flags.

`apiVersion: kubernetes.chalkos.dev/v1alpha1`

A node's config holds at most one. It has 3 [change rules](builtins.md#rules-node).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.kubeletExtraArgs` | map of string | `{}` | Extra flags of the kubelet, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.labels` | map of string | `{}` | Kubernetes labels the node registers with; they are fixed once the node holds a Kubernetes share. |
| `spec.nodeIP.addresses` | list of string or null | `null` | Fixed addresses of the node, at most one per family; null takes the first static address of each family unless validSubnets apply. |
| `spec.nodeIP.validSubnets` | list of string or null | `null` | Subnets the node picks its address from, which replace the Cluster's nodeIP.validSubnets as a whole; null takes the Cluster's. |
| `spec.taints` | list of object | `[]` | Kubernetes taints the node registers with; they are fixed once the node holds a Kubernetes share. |
| `spec.taints[].effect` | one of `NoSchedule`, `PreferNoSchedule`, `NoExecute` | required | Effect of the taint on pods that do not tolerate it. |
| `spec.taints[].key` | string | required | Taint key. |
| `spec.taints[].value` | string or null | `null` | Taint value; null for a taint without a value. |
| `spec.type` | one of `controlplane`, `worker` | required | Type of the node in the cluster; it is fixed once the node holds a Kubernetes share. |
| `spec.vip.interface` | string or null | `null` | Interface the node adds the Cluster's VIP addresses to while it holds them; null means the one holding the node's address of their family. Only control planes hold the VIP, so a worker's stays null. |

## networkd.chalkos.dev { #networkd-chalkos-dev }

The kinds of `networkd.chalkos.dev` are the .link, .netdev and .network files of the image's systemd as documents. Their schemas are generated from that systemd, so they accept the sections and keys it reads.

### Link { #link }

A systemd .link file, which udev applies to the network devices it matches, such as to name them or set their offloads.

`apiVersion: networkd.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own.

Each field of `spec` is a section of the unit: an object, or a list of objects for a
section written more than once. Each key of a section is networkd's key in camelCase and takes
a value, or a list for a key written more than once. The table gives each section and key as
networkd spells it.

| Field | networkd section | Keys |
| --- | --- | --- |
| `spec.energyEfficientEthernet` | EnergyEfficientEthernet | `enable` (Enable), `linkMode` (LinkMode), `txLowPowerIdle` (TxLowPowerIdle), `txLowPowerIdleSec` (TxLowPowerIdleSec) |
| `spec.link` | Link | `advertise` (Advertise), `alias` (Alias), `alternativeName` (AlternativeName), `alternativeNamesPolicy` (AlternativeNamesPolicy), `autoNegotiation` (AutoNegotiation), `autoNegotiationFlowControl` (AutoNegotiationFlowControl), `bitsPerSecond` (BitsPerSecond), `coalescePacketRateHigh` (CoalescePacketRateHigh), `coalescePacketRateLow` (CoalescePacketRateLow), `coalescePacketRateSampleIntervalSec` (CoalescePacketRateSampleIntervalSec), `combinedChannels` (CombinedChannels), `description` (Description), `duplex` (Duplex), `genericReceiveOffload` (GenericReceiveOffload), `genericReceiveOffloadHardware` (GenericReceiveOffloadHardware), `genericReceiveOffloadList` (GenericReceiveOffloadList), `genericReceiveOffloadUDPForwarding` (GenericReceiveOffloadUDPForwarding), `genericSegmentOffloadMaxBytes` (GenericSegmentOffloadMaxBytes), `genericSegmentOffloadMaxSegments` (GenericSegmentOffloadMaxSegments), `genericSegmentationOffload` (GenericSegmentationOffload), `importProperty` (ImportProperty), `irqAffinity` (IRQAffinity), `irqAffinityNUMA` (IRQAffinityNUMA), `irqAffinityPolicy` (IRQAffinityPolicy), `largeReceiveOffload` (LargeReceiveOffload), `macAddress` (MACAddress), `macAddressPolicy` (MACAddressPolicy), `mdi` (MDI), `mtuBytes` (MTUBytes), `nTupleFilter` (NTupleFilter), `name` (Name), `namePolicy` (NamePolicy), `otherChannels` (OtherChannels), `partialGenericSegmentationOffload` (PartialGenericSegmentationOffload), `port` (Port), `property` (Property), `receiveAll` (ReceiveAll), `receiveChecksumOffload` (ReceiveChecksumOffload), `receiveFCS` (ReceiveFCS), `receivePacketSteeringCPUMask` (ReceivePacketSteeringCPUMask), `receiveQueues` (ReceiveQueues), `receiveVLANCTAGFilter` (ReceiveVLANCTAGFilter), `receiveVLANCTAGHardwareAcceleration` (ReceiveVLANCTAGHardwareAcceleration), `rxBufferSize` (RxBufferSize), `rxChannels` (RxChannels), `rxCoalesceHighSec` (RxCoalesceHighSec), `rxCoalesceIrqSec` (RxCoalesceIrqSec), `rxCoalesceLowSec` (RxCoalesceLowSec), `rxCoalesceSec` (RxCoalesceSec), `rxFlowControl` (RxFlowControl), `rxJumboBufferSize` (RxJumboBufferSize), `rxMaxCoalescedFrames` (RxMaxCoalescedFrames), `rxMaxCoalescedHighFrames` (RxMaxCoalescedHighFrames), `rxMaxCoalescedIrqFrames` (RxMaxCoalescedIrqFrames), `rxMaxCoalescedLowFrames` (RxMaxCoalescedLowFrames), `rxMiniBufferSize` (RxMiniBufferSize), `scatterGather` (ScatterGather), `scatterGatherFragmentList` (ScatterGatherFragmentList), `sriovVirtualFunctions` (SR-IOVVirtualFunctions), `statisticsBlockCoalesceSec` (StatisticsBlockCoalesceSec), `tcp6SegmentationOffload` (TCP6SegmentationOffload), `tcpMangleIdSegmentationOffload` (TCPMangleIdSegmentationOffload), `tcpSegmentationOffload` (TCPSegmentationOffload), `tcpecnSegmentationOffload` (TCPECNSegmentationOffload), `transmitChecksumOffload` (TransmitChecksumOffload), `transmitQueueLength` (TransmitQueueLength), `transmitQueues` (TransmitQueues), `transmitVLANCTAGHardwareAcceleration` (TransmitVLANCTAGHardwareAcceleration), `transmitVLANSTAGHardwareAcceleration` (TransmitVLANSTAGHardwareAcceleration), `txBufferSize` (TxBufferSize), `txChannels` (TxChannels), `txCoalesceHighSec` (TxCoalesceHighSec), `txCoalesceIrqSec` (TxCoalesceIrqSec), `txCoalesceLowSec` (TxCoalesceLowSec), `txCoalesceSec` (TxCoalesceSec), `txFlowControl` (TxFlowControl), `txMaxCoalescedFrames` (TxMaxCoalescedFrames), `txMaxCoalescedHighFrames` (TxMaxCoalescedHighFrames), `txMaxCoalescedIrqFrames` (TxMaxCoalescedIrqFrames), `txMaxCoalescedLowFrames` (TxMaxCoalescedLowFrames), `udpSegmentationOffload` (UDPSegmentationOffload), `unsetProperty` (UnsetProperty), `useAdaptiveRxCoalesce` (UseAdaptiveRxCoalesce), `useAdaptiveTxCoalesce` (UseAdaptiveTxCoalesce), `wakeOnLan` (WakeOnLan), `wakeOnLanPassword` (WakeOnLanPassword) |
| `spec.match` | Match | `architecture` (Architecture), `credential` (Credential), `driver` (Driver), `firmware` (Firmware), `host` (Host), `kernelCommandLine` (KernelCommandLine), `kernelVersion` (KernelVersion), `kind` (Kind), `macAddress` (MACAddress), `originalName` (OriginalName), `path` (Path), `permanentMACAddress` (PermanentMACAddress), `property` (Property), `type` (Type), `version` (Version), `virtualization` (Virtualization) |
| `spec.sriov` | SR-IOV | `linkState` (LinkState), `macAddress` (MACAddress), `macSpoofCheck` (MACSpoofCheck), `qualityOfService` (QualityOfService), `queryReceiveSideScaling` (QueryReceiveSideScaling), `trust` (Trust), `virtualFunction` (VirtualFunction), `vlanId` (VLANId), `vlanProtocol` (VLANProtocol) |

### NetDev { #netdev }

A systemd-networkd .netdev file, which creates a virtual network device, such as a VLAN, a bond or a bridge.

`apiVersion: networkd.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own.

Each field of `spec` is a section of the unit: an object, or a list of objects for a
section written more than once. Each key of a section is networkd's key in camelCase and takes
a value, or a list for a key written more than once. The table gives each section and key as
networkd spells it.

| Field | networkd section | Keys |
| --- | --- | --- |
| `spec.bareUDP` | BareUDP | `destinationPort` (DestinationPort), `etherType` (EtherType), `minSourcePort` (MinSourcePort) |
| `spec.batmanAdvanced` | BatmanAdvanced | `aggregation` (Aggregation), `bridgeLoopAvoidance` (BridgeLoopAvoidance), `distributedArpTable` (DistributedArpTable), `fragmentation` (Fragmentation), `gatewayBandwidthDown` (GatewayBandwidthDown), `gatewayBandwidthUp` (GatewayBandwidthUp), `gatewayBandwithDown` (GatewayBandwithDown), `gatewayBandwithUp` (GatewayBandwithUp), `gatewayMode` (GatewayMode), `hopPenalty` (HopPenalty), `originatorIntervalSec` (OriginatorIntervalSec), `routingAlgorithm` (RoutingAlgorithm) |
| `spec.bond` | Bond | `adActorSystem` (AdActorSystem), `adActorSystemPriority` (AdActorSystemPriority), `adSelect` (AdSelect), `adUserPortKey` (AdUserPortKey), `allSlavesActive` (AllSlavesActive), `arpAllTargets` (ARPAllTargets), `arpIntervalSec` (ARPIntervalSec), `arpMissedMax` (ARPMissedMax), `arpValidate` (ARPValidate), `arpipTargets` (ARPIPTargets), `downDelaySec` (DownDelaySec), `dynamicTransmitLoadBalancing` (DynamicTransmitLoadBalancing), `failOverMACPolicy` (FailOverMACPolicy), `gratuitousARP` (GratuitousARP), `lacpTransmitRate` (LACPTransmitRate), `learnPacketIntervalSec` (LearnPacketIntervalSec), `miiMonitorSec` (MIIMonitorSec), `minLinks` (MinLinks), `mode` (Mode), `packetsPerSlave` (PacketsPerSlave), `peerNotifyDelaySec` (PeerNotifyDelaySec), `primaryReselectPolicy` (PrimaryReselectPolicy), `resendIGMP` (ResendIGMP), `transmitHashPolicy` (TransmitHashPolicy), `upDelaySec` (UpDelaySec) |
| `spec.bridge` | Bridge | `ageingTimeSec` (AgeingTimeSec), `defaultPVID` (DefaultPVID), `fdbMaxLearned` (FDBMaxLearned), `forwardDelaySec` (ForwardDelaySec), `groupForwardMask` (GroupForwardMask), `helloTimeSec` (HelloTimeSec), `linkLocalLearning` (LinkLocalLearning), `maxAgeSec` (MaxAgeSec), `multicastIGMPVersion` (MulticastIGMPVersion), `multicastQuerier` (MulticastQuerier), `multicastSnooping` (MulticastSnooping), `priority` (Priority), `stp` (STP), `vlanFiltering` (VLANFiltering), `vlanProtocol` (VLANProtocol) |
| `spec.fooOverUDP` | FooOverUDP | `encapsulation` (Encapsulation), `local` (Local), `peer` (Peer), `peerPort` (PeerPort), `port` (Port), `protocol` (Protocol) |
| `spec.geneve` | GENEVE | `destinationPort` (DestinationPort), `flowLabel` (FlowLabel), `id` (Id), `inheritInnerProtocol` (InheritInnerProtocol), `ipDoNotFragment` (IPDoNotFragment), `remote` (Remote), `tos` (TOS), `ttl` (TTL), `udp6ZeroCheckSumRx` (UDP6ZeroCheckSumRx), `udp6ZeroCheckSumTx` (UDP6ZeroCheckSumTx), `udp6ZeroChecksumRx` (UDP6ZeroChecksumRx), `udp6ZeroChecksumTx` (UDP6ZeroChecksumTx), `udpChecksum` (UDPChecksum) |
| `spec.hsr` | HSR | `ports` (Ports), `protocol` (Protocol), `supervision` (Supervision) |
| `spec.ipoib` | IPoIB | `ignoreUserspaceMulticastGroups` (IgnoreUserspaceMulticastGroups), `mode` (Mode), `partitionKey` (PartitionKey) |
| `spec.ipvlan` | IPVLAN | `flags` (Flags), `mode` (Mode) |
| `spec.ipvtap` | IPVTAP | `flags` (Flags), `mode` (Mode) |
| `spec.l2tp` | L2TP | `encapsulationType` (EncapsulationType), `local` (Local), `peerTunnelId` (PeerTunnelId), `remote` (Remote), `tunnelId` (TunnelId), `udp6CheckSumRx` (UDP6CheckSumRx), `udp6CheckSumTx` (UDP6CheckSumTx), `udp6ChecksumRx` (UDP6ChecksumRx), `udp6ChecksumTx` (UDP6ChecksumTx), `udpCheckSum` (UDPCheckSum), `udpChecksum` (UDPChecksum), `udpDestinationPort` (UDPDestinationPort), `udpSourcePort` (UDPSourcePort) |
| `spec.l2tpSession` | L2TPSession | `layer2SpecificHeader` (Layer2SpecificHeader), `name` (Name), `peerSessionId` (PeerSessionId), `sessionId` (SessionId) |
| `spec.macsec` | MACsec | `encrypt` (Encrypt), `port` (Port) |
| `spec.macsecReceiveAssociation` | MACsecReceiveAssociation | `activate` (Activate), `key` (Key), `keyFile` (KeyFile), `keyId` (KeyId), `macAddress` (MACAddress), `packetNumber` (PacketNumber), `port` (Port) |
| `spec.macsecReceiveChannel` | MACsecReceiveChannel | `macAddress` (MACAddress), `port` (Port) |
| `spec.macsecTransmitAssociation` | MACsecTransmitAssociation | `activate` (Activate), `key` (Key), `keyFile` (KeyFile), `keyId` (KeyId), `packetNumber` (PacketNumber), `useForEncoding` (UseForEncoding) |
| `spec.macvlan` | MACVLAN | `broadcastMulticastQueueLength` (BroadcastMulticastQueueLength), `broadcastQueueThreshold` (BroadcastQueueThreshold), `mode` (Mode), `sourceMACAddress` (SourceMACAddress) |
| `spec.macvtap` | MACVTAP | `mode` (Mode), `sourceMACAddress` (SourceMACAddress) |
| `spec.match` | Match | `architecture` (Architecture), `credential` (Credential), `firmware` (Firmware), `host` (Host), `kernelCommandLine` (KernelCommandLine), `kernelVersion` (KernelVersion), `version` (Version), `virtualization` (Virtualization) |
| `spec.netDev` | NetDev | `description` (Description), `kind` (Kind), `macAddress` (MACAddress), `mtuBytes` (MTUBytes), `name` (Name) |
| `spec.peer` | Peer | `macAddress` (MACAddress), `name` (Name) |
| `spec.tap` | Tap | `group` (Group), `keepCarrier` (KeepCarrier), `multiQueue` (MultiQueue), `oneQueue` (OneQueue), `packetInfo` (PacketInfo), `user` (User), `vNetHeader` (VNetHeader) |
| `spec.tun` | Tun | `group` (Group), `keepCarrier` (KeepCarrier), `multiQueue` (MultiQueue), `oneQueue` (OneQueue), `packetInfo` (PacketInfo), `user` (User), `vNetHeader` (VNetHeader) |
| `spec.tunnel` | Tunnel | `allowLocalRemote` (AllowLocalRemote), `assignToLoopback` (AssignToLoopback), `copyDSCP` (CopyDSCP), `discoverPathMTU` (DiscoverPathMTU), `encapsulation` (Encapsulation), `encapsulationLimit` (EncapsulationLimit), `erspanDirection` (ERSPANDirection), `erspanHardwareId` (ERSPANHardwareId), `erspanIndex` (ERSPANIndex), `erspanVersion` (ERSPANVersion), `external` (External), `fooOverUDP` (FooOverUDP), `fouDestinationPort` (FOUDestinationPort), `fouSourcePort` (FOUSourcePort), `ignoreDontFragment` (IgnoreDontFragment), `independent` (Independent), `inputKey` (InputKey), `ipv6FlowLabel` (IPv6FlowLabel), `ipv6RapidDeploymentPrefix` (IPv6RapidDeploymentPrefix), `isatap` (ISATAP), `key` (Key), `local` (Local), `mode` (Mode), `outputKey` (OutputKey), `remote` (Remote), `serializeTunneledPackets` (SerializeTunneledPackets), `tos` (TOS), `ttl` (TTL) |
| `spec.vlan` | VLAN | `egressQOSMaps` (EgressQOSMaps), `gvrp` (GVRP), `id` (Id), `ingressQOSMaps` (IngressQOSMaps), `looseBinding` (LooseBinding), `mvrp` (MVRP), `protocol` (Protocol), `reorderHeader` (ReorderHeader) |
| `spec.vrf` | VRF | `table` (Table), `tableId` (TableId) |
| `spec.vxcan` | VXCAN | `peer` (Peer) |
| `spec.vxlan` | VXLAN | `arpProxy` (ARPProxy), `destinationPort` (DestinationPort), `external` (External), `fdbAgeingSec` (FDBAgeingSec), `flowLabel` (FlowLabel), `genericProtocolExtension` (GenericProtocolExtension), `group` (Group), `groupPolicyExtension` (GroupPolicyExtension), `id` (Id), `independent` (Independent), `ipDoNotFragment` (IPDoNotFragment), `l2MissNotification` (L2MissNotification), `l3MissNotification` (L3MissNotification), `local` (Local), `macLearning` (MacLearning), `maximumFDBEntries` (MaximumFDBEntries), `portRange` (PortRange), `reduceARPProxy` (ReduceARPProxy), `remote` (Remote), `remoteChecksumRx` (RemoteChecksumRx), `remoteChecksumTx` (RemoteChecksumTx), `routeShortCircuit` (RouteShortCircuit), `tos` (TOS), `ttl` (TTL), `udp6ZeroCheckSumRx` (UDP6ZeroCheckSumRx), `udp6ZeroCheckSumTx` (UDP6ZeroCheckSumTx), `udp6ZeroChecksumRx` (UDP6ZeroChecksumRx), `udp6ZeroChecksumTx` (UDP6ZeroChecksumTx), `udpCheckSum` (UDPCheckSum), `udpChecksum` (UDPChecksum), `vni` (VNI), `vniFilter` (VNIFilter) |
| `spec.wireGuard` | WireGuard | `firewallMark` (FirewallMark), `fwMark` (FwMark), `listenPort` (ListenPort), `privateKey` (PrivateKey), `privateKeyFile` (PrivateKeyFile), `routeMetric` (RouteMetric), `routeTable` (RouteTable) |
| `spec.wireGuardPeer` | WireGuardPeer | `allowedIPs` (AllowedIPs), `endpoint` (Endpoint), `persistentKeepalive` (PersistentKeepalive), `presharedKey` (PresharedKey), `presharedKeyFile` (PresharedKeyFile), `publicKey` (PublicKey), `publicKeyFile` (PublicKeyFile), `routeMetric` (RouteMetric), `routeTable` (RouteTable) |
| `spec.wlan` | WLAN | `physicalDevice` (PhysicalDevice), `type` (Type), `wds` (WDS) |
| `spec.xfrm` | Xfrm | `independent` (Independent), `interfaceId` (InterfaceId) |

### Network { #network }

A systemd-networkd .network file, which configures the network devices it matches: their addresses, routes, DHCP and the virtual devices on them.

`apiVersion: networkd.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own.

Each field of `spec` is a section of the unit: an object, or a list of objects for a
section written more than once. Each key of a section is networkd's key in camelCase and takes
a value, or a list for a key written more than once. The table gives each section and key as
networkd spells it.

| Field | networkd section | Keys |
| --- | --- | --- |
| `spec.address` | Address | `addPrefixRoute` (AddPrefixRoute), `address` (Address), `autoJoin` (AutoJoin), `broadcast` (Broadcast), `duplicateAddressDetection` (DuplicateAddressDetection), `homeAddress` (HomeAddress), `label` (Label), `manageTemporaryAddress` (ManageTemporaryAddress), `netLabel` (NetLabel), `nftSet` (NFTSet), `peer` (Peer), `preferredLifetime` (PreferredLifetime), `prefixRoute` (PrefixRoute), `routeMetric` (RouteMetric), `scope` (Scope) |
| `spec.bandMultiQueueing` | BandMultiQueueing | `handle` (Handle), `parent` (Parent) |
| `spec.bfifo` | BFIFO | `handle` (Handle), `limitBytes` (LimitBytes), `parent` (Parent) |
| `spec.bridge` | Bridge | `allowPortToBeRoot` (AllowPortToBeRoot), `cost` (Cost), `fastLeave` (FastLeave), `hairPin` (HairPin), `isolated` (Isolated), `learning` (Learning), `locked` (Locked), `macAuthenticationBypass` (MACAuthenticationBypass), `multicastFlood` (MulticastFlood), `multicastRouter` (MulticastRouter), `multicastToUnicast` (MulticastToUnicast), `neighborSuppression` (NeighborSuppression), `priority` (Priority), `proxyARP` (ProxyARP), `proxyARPWiFi` (ProxyARPWiFi), `unicastFlood` (UnicastFlood), `useBPDU` (UseBPDU), `vlanTunnel` (VLANTunnel) |
| `spec.bridgeFDB` | BridgeFDB | `associatedWith` (AssociatedWith), `destination` (Destination), `macAddress` (MACAddress), `outgoingInterface` (OutgoingInterface), `vlanId` (VLANId), `vni` (VNI) |
| `spec.bridgeMDB` | BridgeMDB | `multicastGroupAddress` (MulticastGroupAddress), `vlanId` (VLANId) |
| `spec.bridgeVLAN` | BridgeVLAN | `egressUntagged` (EgressUntagged), `pvid` (PVID), `vlan` (VLAN) |
| `spec.cake` | CAKE | `ackFilter` (AckFilter), `autoRateIngress` (AutoRateIngress), `bandwidth` (Bandwidth), `compensationMode` (CompensationMode), `firewallMark` (FirewallMark), `flowIsolationMode` (FlowIsolationMode), `handle` (Handle), `mpuBytes` (MPUBytes), `nat` (NAT), `overheadBytes` (OverheadBytes), `parent` (Parent), `priorityQueueingPreset` (PriorityQueueingPreset), `rttSec` (RTTSec), `splitGSO` (SplitGSO), `useRawPacketSize` (UseRawPacketSize), `wash` (Wash) |
| `spec.can` | CAN | `bitRate` (BitRate), `busErrorReporting` (BusErrorReporting), `classicDataLengthCode` (ClassicDataLengthCode), `dataBitRate` (DataBitRate), `dataPhaseBufferSegment1` (DataPhaseBufferSegment1), `dataPhaseBufferSegment2` (DataPhaseBufferSegment2), `dataPropagationSegment` (DataPropagationSegment), `dataSamplePoint` (DataSamplePoint), `dataSyncJumpWidth` (DataSyncJumpWidth), `dataTimeQuantaNSec` (DataTimeQuantaNSec), `fdMode` (FDMode), `fdNonISO` (FDNonISO), `listenOnly` (ListenOnly), `loopback` (Loopback), `oneShot` (OneShot), `phaseBufferSegment1` (PhaseBufferSegment1), `phaseBufferSegment2` (PhaseBufferSegment2), `presumeACK` (PresumeACK), `propagationSegment` (PropagationSegment), `restartSec` (RestartSec), `samplePoint` (SamplePoint), `syncJumpWidth` (SyncJumpWidth), `termination` (Termination), `timeQuantaNSec` (TimeQuantaNSec), `tripleSampling` (TripleSampling) |
| `spec.classfulMultiQueueing` | ClassfulMultiQueueing | `handle` (Handle), `parent` (Parent) |
| `spec.controlledDelay` | ControlledDelay | `ceThresholdSec` (CEThresholdSec), `ecn` (ECN), `handle` (Handle), `intervalSec` (IntervalSec), `packetLimit` (PacketLimit), `parent` (Parent), `targetSec` (TargetSec) |
| `spec.deficitRoundRobinScheduler` | DeficitRoundRobinScheduler | `handle` (Handle), `parent` (Parent) |
| `spec.deficitRoundRobinSchedulerClass` | DeficitRoundRobinSchedulerClass | `classId` (ClassId), `parent` (Parent), `quantumBytes` (QuantumBytes) |
| `spec.dhcp` | DHCP | `anonymize` (Anonymize), `clientIdentifier` (ClientIdentifier), `criticalConnection` (CriticalConnection), `duidRawData` (DUIDRawData), `duidType` (DUIDType), `forceDHCPv6PDOtherInformation` (ForceDHCPv6PDOtherInformation), `hostname` (Hostname), `iaid` (IAID), `listenPort` (ListenPort), `rapidCommit` (RapidCommit), `requestBroadcast` (RequestBroadcast), `routeMetric` (RouteMetric), `routeTable` (RouteTable), `sendHostname` (SendHostname), `useDNS` (UseDNS), `useDomainName` (UseDomainName), `useDomains` (UseDomains), `useHostname` (UseHostname), `useMTU` (UseMTU), `useNTP` (UseNTP), `useRoutes` (UseRoutes), `useTimezone` (UseTimezone), `userClass` (UserClass), `vendorClassIdentifier` (VendorClassIdentifier) |
| `spec.dhcpPrefixDelegation` | DHCPPrefixDelegation | `announce` (Announce), `assign` (Assign), `manageTemporaryAddress` (ManageTemporaryAddress), `netLabel` (NetLabel), `nftSet` (NFTSet), `routeMetric` (RouteMetric), `subnetId` (SubnetId), `token` (Token), `uplinkInterface` (UplinkInterface) |
| `spec.dhcpRelay` | DHCPRelay | `agentAddress` (AgentAddress), `circuitId` (CircuitId), `extraOption` (ExtraOption), `gatewayAddress` (GatewayAddress), `interfacePriority` (InterfacePriority), `virtualSubnetSelection` (VirtualSubnetSelection) |
| `spec.dhcpServer` | DHCPServer | `bindToInterface` (BindToInterface), `bootFilename` (BootFilename), `bootServerAddress` (BootServerAddress), `bootServerName` (BootServerName), `defaultLeaseTimeSec` (DefaultLeaseTimeSec), `dns` (DNS), `domain` (Domain), `emitDNS` (EmitDNS), `emitDomain` (EmitDomain), `emitLPR` (EmitLPR), `emitNTP` (EmitNTP), `emitPOP3` (EmitPOP3), `emitRouter` (EmitRouter), `emitSIP` (EmitSIP), `emitSMTP` (EmitSMTP), `emitTimezone` (EmitTimezone), `ipv6OnlyPreferredSec` (IPv6OnlyPreferredSec), `localLeaseDomain` (LocalLeaseDomain), `lpr` (LPR), `maxLeaseTimeSec` (MaxLeaseTimeSec), `ntp` (NTP), `persistLeases` (PersistLeases), `poolOffset` (PoolOffset), `poolSize` (PoolSize), `pop3` (POP3), `rapidCommit` (RapidCommit), `relayAgentCircuitId` (RelayAgentCircuitId), `relayAgentRemoteId` (RelayAgentRemoteId), `relayTarget` (RelayTarget), `router` (Router), `sendOption` (SendOption), `sendVendorOption` (SendVendorOption), `serverAddress` (ServerAddress), `sip` (SIP), `smtp` (SMTP), `timezone` (Timezone), `uplinkInterface` (UplinkInterface) |
| `spec.dhcpServerStaticLease` | DHCPServerStaticLease | `address` (Address), `hostname` (Hostname), `macAddress` (MACAddress) |
| `spec.dhcpv4` | DHCPv4 | `allowList` (AllowList), `anonymize` (Anonymize), `blackList` (BlackList), `bootp` (BOOTP), `clientIdentifier` (ClientIdentifier), `criticalConnection` (CriticalConnection), `denyList` (DenyList), `duidRawData` (DUIDRawData), `duidType` (DUIDType), `fallbackLeaseLifetimeSec` (FallbackLeaseLifetimeSec), `hostname` (Hostname), `iaid` (IAID), `initialAdvertisedReceiveWindow` (InitialAdvertisedReceiveWindow), `initialCongestionWindow` (InitialCongestionWindow), `ipServiceType` (IPServiceType), `ipv6OnlyMode` (IPv6OnlyMode), `label` (Label), `listenPort` (ListenPort), `maxAttempts` (MaxAttempts), `mudurl` (MUDURL), `netLabel` (NetLabel), `nftSet` (NFTSet), `quickAck` (QuickAck), `rapidCommit` (RapidCommit), `requestAddress` (RequestAddress), `requestBroadcast` (RequestBroadcast), `requestOptions` (RequestOptions), `routeMTUBytes` (RouteMTUBytes), `routeMetric` (RouteMetric), `routeTable` (RouteTable), `routesToDNS` (RoutesToDNS), `routesToNTP` (RoutesToNTP), `sendDecline` (SendDecline), `sendHostname` (SendHostname), `sendOption` (SendOption), `sendRelease` (SendRelease), `sendVendorOption` (SendVendorOption), `serverPort` (ServerPort), `socketPriority` (SocketPriority), `unassignedSubnetPolicy` (UnassignedSubnetPolicy), `use6RD` (Use6RD), `useCaptivePortal` (UseCaptivePortal), `useDNR` (UseDNR), `useDNS` (UseDNS), `useDomainName` (UseDomainName), `useDomains` (UseDomains), `useGateway` (UseGateway), `useHostname` (UseHostname), `useMTU` (UseMTU), `useNTP` (UseNTP), `useRoutes` (UseRoutes), `useSIP` (UseSIP), `useTimezone` (UseTimezone), `userClass` (UserClass), `vendorClassIdentifier` (VendorClassIdentifier) |
| `spec.dhcpv6` | DHCPv6 | `duidRawData` (DUIDRawData), `duidType` (DUIDType), `forceDHCPv6PDOtherInformation` (ForceDHCPv6PDOtherInformation), `hostname` (Hostname), `iaid` (IAID), `mudurl` (MUDURL), `netLabel` (NetLabel), `nftSet` (NFTSet), `prefixDelegationHint` (PrefixDelegationHint), `rapidCommit` (RapidCommit), `requestOptions` (RequestOptions), `routeMetric` (RouteMetric), `routeTable` (RouteTable), `sendHostname` (SendHostname), `sendOption` (SendOption), `sendRelease` (SendRelease), `sendVendorOption` (SendVendorOption), `unassignedSubnetPolicy` (UnassignedSubnetPolicy), `useAddress` (UseAddress), `useCaptivePortal` (UseCaptivePortal), `useDNR` (UseDNR), `useDNS` (UseDNS), `useDelegatedPrefix` (UseDelegatedPrefix), `useDomains` (UseDomains), `useHostname` (UseHostname), `useNTP` (UseNTP), `useSIP` (UseSIP), `userClass` (UserClass), `vendorClass` (VendorClass), `withoutRA` (WithoutRA) |
| `spec.dhcpv6PrefixDelegation` | DHCPv6PrefixDelegation | `announce` (Announce), `assign` (Assign), `manageTemporaryAddress` (ManageTemporaryAddress), `routeMetric` (RouteMetric), `subnetId` (SubnetId), `token` (Token) |
| `spec.enhancedTransmissionSelection` | EnhancedTransmissionSelection | `bands` (Bands), `handle` (Handle), `parent` (Parent), `priorityMap` (PriorityMap), `quantumBytes` (QuantumBytes), `strictBands` (StrictBands) |
| `spec.fairQueueing` | FairQueueing | `buckets` (Buckets), `ceThresholdSec` (CEThresholdSec), `flowLimit` (FlowLimit), `handle` (Handle), `initialQuantum` (InitialQuantum), `initialQuantumBytes` (InitialQuantumBytes), `maximumRate` (MaximumRate), `orphanMask` (OrphanMask), `pacing` (Pacing), `packetLimit` (PacketLimit), `parent` (Parent), `quantum` (Quantum), `quantumBytes` (QuantumBytes) |
| `spec.fairQueueingControlledDelay` | FairQueueingControlledDelay | `ceThresholdSec` (CEThresholdSec), `ecn` (ECN), `flows` (Flows), `handle` (Handle), `intervalSec` (IntervalSec), `memoryLimit` (MemoryLimit), `memoryLimitBytes` (MemoryLimitBytes), `packetLimit` (PacketLimit), `parent` (Parent), `quantum` (Quantum), `quantumBytes` (QuantumBytes), `targetSec` (TargetSec) |
| `spec.flowQueuePIE` | FlowQueuePIE | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.genericRandomEarlyDetection` | GenericRandomEarlyDetection | `defaultVirtualQueue` (DefaultVirtualQueue), `genericRIO` (GenericRIO), `handle` (Handle), `parent` (Parent), `virtualQueues` (VirtualQueues) |
| `spec.heavyHitterFilter` | HeavyHitterFilter | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.hierarchyTokenBucket` | HierarchyTokenBucket | `defaultClass` (DefaultClass), `handle` (Handle), `parent` (Parent), `rateToQuantum` (RateToQuantum) |
| `spec.hierarchyTokenBucketClass` | HierarchyTokenBucketClass | `bufferBytes` (BufferBytes), `ceilBufferBytes` (CeilBufferBytes), `ceilRate` (CeilRate), `classId` (ClassId), `mtuBytes` (MTUBytes), `overheadBytes` (OverheadBytes), `parent` (Parent), `priority` (Priority), `quantumBytes` (QuantumBytes), `rate` (Rate) |
| `spec.ipoib` | IPoIB | `ignoreUserspaceMulticastGroups` (IgnoreUserspaceMulticastGroups), `mode` (Mode) |
| `spec.ipv6AcceptRA` | IPv6AcceptRA | `blackList` (BlackList), `denyList` (DenyList), `dhcpv6Client` (DHCPv6Client), `netLabel` (NetLabel), `nftSet` (NFTSet), `prefixAllowList` (PrefixAllowList), `prefixDenyList` (PrefixDenyList), `quickAck` (QuickAck), `routeAllowList` (RouteAllowList), `routeDenyList` (RouteDenyList), `routeMetric` (RouteMetric), `routeTable` (RouteTable), `routerAllowList` (RouterAllowList), `routerDenyList` (RouterDenyList), `token` (Token), `useAutonomousPrefix` (UseAutonomousPrefix), `useCaptivePortal` (UseCaptivePortal), `useDNR` (UseDNR), `useDNS` (UseDNS), `useDomains` (UseDomains), `useGateway` (UseGateway), `useHopLimit` (UseHopLimit), `useICMP6RateLimit` (UseICMP6RateLimit), `useMTU` (UseMTU), `useOnLinkPrefix` (UseOnLinkPrefix), `usePREF64` (UsePREF64), `useReachableTime` (UseReachableTime), `useRedirect` (UseRedirect), `useRetransmissionTime` (UseRetransmissionTime), `useRoutePrefix` (UseRoutePrefix) |
| `spec.ipv6AddressLabel` | IPv6AddressLabel | `label` (Label), `prefix` (Prefix) |
| `spec.ipv6PREF64Prefix` | IPv6PREF64Prefix | `lifetimeSec` (LifetimeSec), `prefix` (Prefix) |
| `spec.ipv6Prefix` | IPv6Prefix | `addressAutoconfiguration` (AddressAutoconfiguration), `assign` (Assign), `onLink` (OnLink), `preferredLifetimeSec` (PreferredLifetimeSec), `prefix` (Prefix), `routeMetric` (RouteMetric), `token` (Token), `validLifetimeSec` (ValidLifetimeSec) |
| `spec.ipv6PrefixDelegation` | IPv6PrefixDelegation | `dns` (DNS), `dnsLifetimeSec` (DNSLifetimeSec), `domains` (Domains), `emitDNS` (EmitDNS), `emitDomains` (EmitDomains), `managed` (Managed), `otherInformation` (OtherInformation), `routerLifetimeSec` (RouterLifetimeSec), `routerPreference` (RouterPreference) |
| `spec.ipv6RoutePrefix` | IPv6RoutePrefix | `lifetimeSec` (LifetimeSec), `preference` (Preference), `route` (Route) |
| `spec.ipv6SendRA` | IPv6SendRA | `dns` (DNS), `dnsLifetimeSec` (DNSLifetimeSec), `domains` (Domains), `emitDNS` (EmitDNS), `emitDomains` (EmitDomains), `homeAgent` (HomeAgent), `homeAgentLifetimeSec` (HomeAgentLifetimeSec), `homeAgentPreference` (HomeAgentPreference), `hopLimit` (HopLimit), `managed` (Managed), `otherInformation` (OtherInformation), `reachableTimeSec` (ReachableTimeSec), `retransmitSec` (RetransmitSec), `routerLifetimeSec` (RouterLifetimeSec), `routerPreference` (RouterPreference), `uplinkInterface` (UplinkInterface) |
| `spec.link` | Link | `activationPolicy` (ActivationPolicy), `allMulticast` (AllMulticast), `arp` (ARP), `group` (Group), `macAddress` (MACAddress), `mtuBytes` (MTUBytes), `multicast` (Multicast), `promiscuous` (Promiscuous), `requiredFamilyForOnline` (RequiredFamilyForOnline), `requiredForOnline` (RequiredForOnline), `unmanaged` (Unmanaged) |
| `spec.lldp` | LLDP | `mudurl` (MUDURL) |
| `spec.match` | Match | `architecture` (Architecture), `bssid` (BSSID), `credential` (Credential), `driver` (Driver), `firmware` (Firmware), `host` (Host), `kernelCommandLine` (KernelCommandLine), `kernelVersion` (KernelVersion), `kind` (Kind), `macAddress` (MACAddress), `name` (Name), `path` (Path), `permanentMACAddress` (PermanentMACAddress), `property` (Property), `ssid` (SSID), `type` (Type), `version` (Version), `virtualization` (Virtualization), `wlanInterfaceType` (WLANInterfaceType) |
| `spec.mobileNetwork` | MobileNetwork | `allowRoaming` (AllowRoaming), `allowedAuthenticationMechanisms` (AllowedAuthenticationMechanisms), `apn` (APN), `ipFamily` (IPFamily), `operatorId` (OperatorId), `password` (Password), `pin` (PIN), `routeMetric` (RouteMetric), `useGateway` (UseGateway), `user` (User) |
| `spec.neighbor` | Neighbor | `address` (Address), `linkLayerAddress` (LinkLayerAddress), `macAddress` (MACAddress) |
| `spec.network` | Network | `activeSlave` (ActiveSlave), `address` (Address), `batmanAdvanced` (BatmanAdvanced), `bindCarrier` (BindCarrier), `bond` (Bond), `bridge` (Bridge), `configureWithoutCarrier` (ConfigureWithoutCarrier), `defaultRouteOnDevice` (DefaultRouteOnDevice), `description` (Description), `dhcp` (DHCP), `dhcpPrefixDelegation` (DHCPPrefixDelegation), `dhcpRelay` (DHCPRelay), `dhcpServer` (DHCPServer), `dhcpv6PrefixDelegation` (DHCPv6PrefixDelegation), `dns` (DNS), `dnsDefaultRoute` (DNSDefaultRoute), `dnsOverTLS` (DNSOverTLS), `dnssec` (DNSSEC), `dnssecNegativeTrustAnchors` (DNSSECNegativeTrustAnchors), `domains` (Domains), `emitLLDP` (EmitLLDP), `gateway` (Gateway), `ignoreCarrierLoss` (IgnoreCarrierLoss), `ipForward` (IPForward), `ipMasquerade` (IPMasquerade), `ipoib` (IPoIB), `ipv4AcceptLocal` (IPv4AcceptLocal), `ipv4DuplicateAddressDetectionTimeoutSec` (IPv4DuplicateAddressDetectionTimeoutSec), `ipv4Forwarding` (IPv4Forwarding), `ipv4LL` (IPv4LL), `ipv4LLRoute` (IPv4LLRoute), `ipv4LLStartAddress` (IPv4LLStartAddress), `ipv4ProxyARP` (IPv4ProxyARP), `ipv4ProxyARPPrivateVLAN` (IPv4ProxyARPPrivateVLAN), `ipv4ReversePathFilter` (IPv4ReversePathFilter), `ipv4RouteLocalnet` (IPv4RouteLocalnet), `ipv4SrcValidMark` (IPv4SrcValidMark), `ipv6AcceptRA` (IPv6AcceptRA), `ipv6AcceptRouterAdvertisements` (IPv6AcceptRouterAdvertisements), `ipv6DuplicateAddressDetection` (IPv6DuplicateAddressDetection), `ipv6Forwarding` (IPv6Forwarding), `ipv6HopLimit` (IPv6HopLimit), `ipv6LinkLocalAddressGenerationMode` (IPv6LinkLocalAddressGenerationMode), `ipv6MTUBytes` (IPv6MTUBytes), `ipv6PrefixDelegation` (IPv6PrefixDelegation), `ipv6PrivacyExtensions` (IPv6PrivacyExtensions), `ipv6ProxyNDP` (IPv6ProxyNDP), `ipv6ProxyNDPAddress` (IPv6ProxyNDPAddress), `ipv6RetransmissionTimeSec` (IPv6RetransmissionTimeSec), `ipv6SendRA` (IPv6SendRA), `ipv6StableSecretAddress` (IPv6StableSecretAddress), `ipv6Token` (IPv6Token), `ipvlan` (IPVLAN), `ipvtap` (IPVTAP), `keepConfiguration` (KeepConfiguration), `keepMaster` (KeepMaster), `l2tp` (L2TP), `linkLocalAddressing` (LinkLocalAddressing), `lldp` (LLDP), `llmnr` (LLMNR), `macsec` (MACsec), `macvlan` (MACVLAN), `macvtap` (MACVTAP), `mplsRouting` (MPLSRouting), `multicastDNS` (MulticastDNS), `multicastIGMPVersion` (MulticastIGMPVersion), `ntp` (NTP), `primarySlave` (PrimarySlave), `proxyARP` (ProxyARP), `tunnel` (Tunnel), `useDomains` (UseDomains), `vlan` (VLAN), `vrf` (VRF), `vxlan` (VXLAN), `xfrm` (Xfrm) |
| `spec.networkEmulator` | NetworkEmulator | `delayJitterSec` (DelayJitterSec), `delaySec` (DelaySec), `duplicateRate` (DuplicateRate), `handle` (Handle), `lossRate` (LossRate), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.nextHop` | NextHop | `blackhole` (Blackhole), `family` (Family), `gateway` (Gateway), `group` (Group), `id` (Id), `onLink` (OnLink) |
| `spec.pfifo` | PFIFO | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.pfifoFast` | PFIFOFast | `handle` (Handle), `parent` (Parent) |
| `spec.pfifoHeadDrop` | PFIFOHeadDrop | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.pie` | PIE | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.qDisc` | QDisc | `handle` (Handle), `parent` (Parent) |
| `spec.quickFairQueueing` | QuickFairQueueing | `handle` (Handle), `parent` (Parent) |
| `spec.quickFairQueueingClass` | QuickFairQueueingClass | `classId` (ClassId), `maxPacketBytes` (MaxPacketBytes), `parent` (Parent), `weight` (Weight) |
| `spec.route` | Route | `destination` (Destination), `fastOpenNoCookie` (FastOpenNoCookie), `gateway` (Gateway), `gatewayOnLink` (GatewayOnLink), `gatewayOnlink` (GatewayOnlink), `hopLimit` (HopLimit), `initialAdvertisedReceiveWindow` (InitialAdvertisedReceiveWindow), `initialCongestionWindow` (InitialCongestionWindow), `ipv6Preference` (IPv6Preference), `metric` (Metric), `mtuBytes` (MTUBytes), `multiPathRoute` (MultiPathRoute), `nextHop` (NextHop), `preferredSource` (PreferredSource), `protocol` (Protocol), `quickAck` (QuickAck), `scope` (Scope), `source` (Source), `table` (Table), `tcpAdvertisedMaximumSegmentSize` (TCPAdvertisedMaximumSegmentSize), `tcpCongestionControlAlgorithm` (TCPCongestionControlAlgorithm), `tcpRetransmissionTimeoutSec` (TCPRetransmissionTimeoutSec), `ttlPropagate` (TTLPropagate), `type` (Type) |
| `spec.routingPolicyRule` | RoutingPolicyRule | `destinationPort` (DestinationPort), `family` (Family), `firewallMark` (FirewallMark), `from` (From), `goTo` (GoTo), `incomingInterface` (IncomingInterface), `invertRule` (InvertRule), `ipProtocol` (IPProtocol), `l3MasterDevice` (L3MasterDevice), `outgoingInterface` (OutgoingInterface), `priority` (Priority), `sourcePort` (SourcePort), `suppressInterfaceGroup` (SuppressInterfaceGroup), `suppressPrefixLength` (SuppressPrefixLength), `table` (Table), `to` (To), `type` (Type), `typeOfService` (TypeOfService), `user` (User) |
| `spec.sriov` | SR-IOV | `linkState` (LinkState), `macAddress` (MACAddress), `macSpoofCheck` (MACSpoofCheck), `qualityOfService` (QualityOfService), `queryReceiveSideScaling` (QueryReceiveSideScaling), `trust` (Trust), `virtualFunction` (VirtualFunction), `vlanId` (VLANId), `vlanProtocol` (VLANProtocol) |
| `spec.stochasticFairBlue` | StochasticFairBlue | `handle` (Handle), `packetLimit` (PacketLimit), `parent` (Parent) |
| `spec.stochasticFairnessQueueing` | StochasticFairnessQueueing | `handle` (Handle), `parent` (Parent), `perturbPeriodSec` (PerturbPeriodSec) |
| `spec.tokenBucketFilter` | TokenBucketFilter | `burst` (Burst), `burstBytes` (BurstBytes), `handle` (Handle), `latencySec` (LatencySec), `limitBytes` (LimitBytes), `limitSize` (LimitSize), `mpuBytes` (MPUBytes), `mtuBytes` (MTUBytes), `parent` (Parent), `peakRate` (PeakRate), `rate` (Rate) |
| `spec.trafficControlQueueingDiscipline` | TrafficControlQueueingDiscipline | `networkEmulatorDelayJitterSec` (NetworkEmulatorDelayJitterSec), `networkEmulatorDelaySec` (NetworkEmulatorDelaySec), `networkEmulatorDuplicateRate` (NetworkEmulatorDuplicateRate), `networkEmulatorLossRate` (NetworkEmulatorLossRate), `networkEmulatorPacketLimit` (NetworkEmulatorPacketLimit), `parent` (Parent) |
| `spec.trivialLinkEqualizer` | TrivialLinkEqualizer | `handle` (Handle), `id` (Id), `parent` (Parent) |

## oci.chalkos.dev { #oci-chalkos-dev }

The kinds of `oci.chalkos.dev` set how the node reaches OCI registries: the credentials of each host and the mirrors of a registry.

### Mirror { #mirror }

The mirror endpoints that chalkd and containerd try, in order, before a registry itself.

`apiVersion: oci.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has a [change rule](builtins.md#rules-mirror).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.endpoints` | list of string | required | URLs of the mirror's endpoints, which chalkd and containerd try in order before the registry itself. A path is where the endpoint serves the registry API, below &lt;path>/v2 as containerd takes it. chalkd authenticates to an endpoint with the Registry of the endpoint's host, and an http:// endpoint needs that Registry to set insecure. |
| `spec.registry` | string | required | Host of the registry as image references name it, with its port unless it is 443; an IPv6 address in brackets. |

### Registry { #registry }

The credentials and transport of an OCI registry host, with which chalkd pulls OS images and, on images that run Kubernetes, containerd pulls container images.

`apiVersion: oci.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 2 [change rules](builtins.md#rules-registry).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.host` | string | required | Host of a registry or of a mirror's endpoint, as image references and endpoints name it, with its port unless it is 443; an IPv6 address in brackets. chalkd pulls OS images from this host with the Registry's credentials, over plain HTTP when insecure is set. On images that run Kubernetes, the kubelet hands these credentials to containerd for the container images of this host, and containerd sends them to every endpoint of the host's Mirror too, over plain HTTP to an endpoint whose host is insecure. The kubelet compares the host, in lower case and with its port, case-sensitively with the image's host as the pod spec writes it, so pods name the host in lower case. An IPv6 host with credentials needs its port, 443 included, for the kubelet to match it. |
| `spec.insecure` | boolean | `false` | Whether chalkd and containerd reach this host over plain HTTP, with credentials and images unencrypted, and a Mirror may name an http:// endpoint on it. Without it, chalkd pulls over HTTPS alone, also from private and loopback addresses. |
| `spec.password` | secret reference or null | `null` | Password or token of the user, from a Secret; null pulls anonymously. |
| `spec.username` | string or null | `null` | User chalkd authenticates to the host as, with the password, and containerd too on images that run Kubernetes; null pulls anonymously. |

## router.chalkos.dev { #router-chalkos-dev }

The kinds of `router.chalkos.dev` set how a router forwards and filters: its forwarding and reverse-path filter, its firewall's zones, policies and address sets, and its NAT rules. Only router images accept them.

### AddressSet { #addressset }

Address ranges of one family, which firewall rules name as `set:<name>`.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has a [change rule](builtins.md#rules-addressset).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.family` | one of `ipv4`, `ipv6` | required | Address family of the set. |
| `spec.prefixes` | list of string | required | Address ranges of the set. |

### BGP { #bgp }

A BGP instance of the router in one VRF: its AS number and router ID, how many equal paths it installs, its BFD timers, the networks it announces and the routes it imports from other VRFs.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 11 [change rules](builtins.md#rules-bgp).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.asn` | integer from 1 to 4294967295 | required | AS number of the instance. It stays while the BGP document keeps its name; a BGP document of another name replaces the old one, AS number included. |
| `spec.bfd.multiplier` | integer from 2 to 255 | `3` | BFD packets in a row a peer may miss before the router takes it for down and ends the session. |
| `spec.bfd.receiveMs` | integer from 10 to 60000 | `300` | Shortest interval in milliseconds at which the router accepts BFD packets of the peers with `bfd`. |
| `spec.bfd.transmitMs` | integer from 10 to 60000 | `300` | Shortest interval in milliseconds at which the router sends BFD packets. |
| `spec.gracefulRestart` | boolean | `false` | Whether the router keeps a peer's routes while the peer restarts and asks its peers to keep its own. |
| `spec.import` | object | `{}` | Routes the instance takes from the BGP instances of other VRFs, which the kernel installs in this VRF's table with their next hops in the other VRF. Static routes of a VRF's links are not leaked. |
| `spec.import.ipv4` | object or null | `null` | IPv4 routes the instance takes from other VRFs' instances; null takes none. |
| `spec.import.ipv4.policy` | string or null | `null` | RoutingPolicy that decides the routes the instance takes from all of `vrfs`; null takes every route. |
| `spec.import.ipv4.vrfs` | list of string | required | VRFs whose BGP instances' routes of the family the instance takes, each once, by the names of their VRF documents or `default`. |
| `spec.import.ipv6` | object or null | `null` | IPv6 routes the instance takes from other VRFs' instances; null takes none. |
| `spec.import.ipv6.policy` | string or null | `null` | RoutingPolicy that decides the routes the instance takes from all of `vrfs`; null takes every route. |
| `spec.import.ipv6.vrfs` | list of string | required | VRFs whose BGP instances' routes of the family the instance takes, each once, by the names of their VRF documents or `default`. |
| `spec.multipath.asPathRelax` | boolean | `false` | Whether paths through different neighbouring ASes count as equal when their AS paths are equally long, as for two leaves of a fabric. |
| `spec.multipath.ebgp` | integer from 1 to 64 | `1` | Equal paths from external peers the router installs for one prefix at most. |
| `spec.multipath.ibgp` | integer from 1 to 64 | `1` | Equal paths from internal peers the router installs for one prefix at most. |
| `spec.networks` | list of object | `[]` | Networks the router announces to its peers. |
| `spec.networks[].policy` | string or null | `null` | RoutingPolicy whose terms set the announcement's attributes; null announces it as it is. |
| `spec.networks[].prefix` | string | required | Address range the router announces while its table holds a route to it. |
| `spec.routerID` | string | required | Router ID, in IPv4 notation, which the router's peers know it by; give an IPv6-only router one too. |
| `spec.vrf` | string or null | `null` | VRF document whose instance this is; null for the default VRF, which holds every link no VRF holds. |

### BGPPeer { #bgppeer }

A BGP neighbour of the router, or the neighbours on a set of interfaces: where to reach them, their AS, and what the router accepts from them and sends them in each address family.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 15 [change rules](builtins.md#rules-bgppeer).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.address` | string or null | `null` | Unicast address of the neighbour; excludes `interface` and `interfaces`. |
| `spec.bfd` | boolean | `false` | Whether the router checks the session's link with BFD, at the timers of the BGP document, and ends the session when BFD finds it down. |
| `spec.description` | string or null | `null` | What the neighbour is, in up to 80 printable ASCII characters other than `@`, with a single space between two words. |
| `spec.extendedNextHop` | boolean | `false` | Whether the router sends and accepts IPv4 routes with IPv6 next hops (RFC 8950). FRR negotiates it on sessions of `interface` and `interfaces` whatever this says. |
| `spec.families` | object | `{}` | The address families the session exchanges routes of, and how. |
| `spec.families.ipv4` | object or null | `null` | IPv4 routes of the session; null exchanges none. |
| `spec.families.ipv4.defaultOriginate` | boolean | `false` | Whether the router sends the peer a default route of the family: whatever its own table holds, or while `defaultOriginateWhen` holds. |
| `spec.families.ipv4.defaultOriginateWhen` | string or null | `null` | PrefixList of the family that decides when the router sends the default route of `defaultOriginate`: while the peer's VRF holds a route the list matches, from BGP, from the kernel, such as a route from DHCP, from router advertisements or a `Network`'s `[Route]`, or a link's prefix. Blackhole, unreachable and prohibit routes do not count. The router does not announce the routes it tracks. null sends the default route whatever the table holds. |
| `spec.families.ipv4.export` | string or null | `null` | RoutingPolicy that decides the routes the router sends the peer; null sends every route the router has. |
| `spec.families.ipv4.import` | string or null | `null` | RoutingPolicy that decides the routes the router accepts from the peer; null accepts every route. |
| `spec.families.ipv4.maxPrefix` | integer from 1 to 4294967295 or null | `null` | Routes the router accepts from the peer at most; past them it ends the session. null sets no limit. |
| `spec.families.ipv6` | object or null | `null` | IPv6 routes of the session; null exchanges none. |
| `spec.families.ipv6.defaultOriginate` | boolean | `false` | Whether the router sends the peer a default route of the family: whatever its own table holds, or while `defaultOriginateWhen` holds. |
| `spec.families.ipv6.defaultOriginateWhen` | string or null | `null` | PrefixList of the family that decides when the router sends the default route of `defaultOriginate`: while the peer's VRF holds a route the list matches, from BGP, from the kernel, such as a route from DHCP, from router advertisements or a `Network`'s `[Route]`, or a link's prefix. Blackhole, unreachable and prohibit routes do not count. The router does not announce the routes it tracks. null sends the default route whatever the table holds. |
| `spec.families.ipv6.export` | string or null | `null` | RoutingPolicy that decides the routes the router sends the peer; null sends every route the router has. |
| `spec.families.ipv6.import` | string or null | `null` | RoutingPolicy that decides the routes the router accepts from the peer; null accepts every route. |
| `spec.families.ipv6.maxPrefix` | integer from 1 to 4294967295 or null | `null` | Routes the router accepts from the peer at most; past them it ends the session. null sets no limit. |
| `spec.interface` | string or null | `null` | Interface on whose link the router finds the neighbour by its IPv6 link-local address, without an address of its own on the link; excludes `address` and `interfaces`. A name that reads as an address is refused. |
| `spec.interfaces` | list of string or null | `null` | Interfaces, by name or glob, a glob being a prefix followed by `*`, with a session each, as for `interface`; a glob stands for the interfaces of the peer's VRF that the node's Network documents name in a `[Match]` section's `Name=`. Excludes `address` and `interface`. |
| `spec.password` | secret reference or null | `null` | Key of a Secret that holds the session's TCP MD5 password, of 80 characters at most and no white space; null for none. The Secret's name and key hold letters, digits, `.`, `_` and `-` alone. |
| `spec.remoteAS` | one of `external`, `internal` or integer from 1 to 4294967295 | required | AS of the neighbour: a number, `external` for any AS but the router's, or `internal` for the router's own. |
| `spec.shutdown` | boolean | `false` | Whether the router keeps the session down while the document stays. |
| `spec.vrf` | string or null | `null` | VRF document whose BGP instance runs the session; null for the default VRF. The peer's interfaces are links of that VRF. |

### CommunityList { #communitylist }

BGP communities that a RoutingPolicy term matches routes against: a route that carries one of them matches.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has a [change rule](builtins.md#rules-communitylist).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.communities` | list of string | required | The communities: standard communities `<AS>:<value>` of 16-bit numbers, or large ones `<AS>:<value>:<value>` of 32-bit numbers. |

### FirewallPolicy { #firewallpolicy }

What the router's firewall does with new connections from one zone to another, or to the router itself: rules in order, then a default.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 8 [change rules](builtins.md#rules-firewallpolicy).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.default` | one of `accept`, `drop`, `reject` | `"drop"` | What happens to a connection no rule matches; `reject` answers with an administratively prohibited error. |
| `spec.from` | string | required | FirewallZone the connections come from. |
| `spec.rules` | list of object | `[]` | Rules in order: the first that matches a connection decides. |
| `spec.rules[].action` | one of `accept`, `drop`, `reject` | required | What happens to a connection the rule matches; `reject` answers with an administratively prohibited error. |
| `spec.rules[].destination` | list of string | `[]` | Destination address ranges, or one `set:<name>` for the ranges of an AddressSet; empty matches every destination. |
| `spec.rules[].destinationPorts` | list of integer from 1 to 65535 or string | `[]` | Destination ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port. |
| `spec.rules[].family` | one of `ipv4`, `ipv6` or null | `null` | Address family the rule matches; null matches each family its protocol and addresses leave. |
| `spec.rules[].icmpTypes` | list of one of `echo-request`, `echo-reply`, `destination-unreachable`, `time-exceeded`, `parameter-problem`, `packet-too-big` | `[]` | ICMP or ICMPv6 types, with the protocol `icmp` or `icmpv6`; empty matches every type. `packet-too-big` is ICMPv6's. |
| `spec.rules[].log` | boolean | `false` | Whether the kernel logs each packet the rule matches, prefixed with the policy's name and the rule's number. |
| `spec.rules[].protocol` | one of `tcp`, `udp`, `sctp`, `icmp`, `icmpv6`, `gre`, `esp`, `ah` or null | `null` | Protocol the rule matches; null matches every protocol. `icmp` is IPv4's and `icmpv6` IPv6's. |
| `spec.rules[].source` | list of string | `[]` | Source address ranges, or one `set:<name>` for the ranges of an AddressSet; empty matches every source. In each family the rule matches the ranges of that family. |
| `spec.rules[].sourcePorts` | list of integer from 1 to 65535 or string | `[]` | Source ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port. |
| `spec.to` | string | required | FirewallZone the connections go to, or `local` for connections to the router itself. |

### FirewallZone { #firewallzone }

A zone of the router's firewall: interfaces and VRFs whose connections the FirewallPolicy documents treat alike. The zone local is the router itself and has no document.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 6 [change rules](builtins.md#rules-firewallzone).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.interfaces` | list of string | `[]` | Interfaces of the zone, by name or glob, a glob being a prefix followed by `*`. A link a VRF holds may be named here without its VRF, so that one VRF's links fall into several zones; the VRF itself is then in no other zone. |
| `spec.vrfs` | list of string | `[]` | VRF documents whose links are all in the zone, those added later included; a VRF is in one zone at most. |

### NAT64 { #nat64 }

A stateful NAT64 of the router, an instance of Jool in a network namespace of its own that a link joins to its VRF: it translates IPv6 connections to addresses of its prefix into IPv4 connections to the address each embeds, from the addresses of its pool.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 13 [change rules](builtins.md#rules-nat64).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.pool4` | list of object | `[]` | IPv4 addresses, with their ports, that the translated connections leave from: addresses routed to the router, not addresses of its links, which the router answers itself. The rules refuse an address that a `Network` document gives a link. An address a link takes from DHCP passes them, and `chalkos-nat64.service` then fails without changing an instance; leave pool4 empty instead. Empty takes the address of the link a connection leaves through, with the ports 61001 to 65535, which the router masquerades. The firewall's NAT rules leave the translated connections alone. |
| `spec.pool4[].ports` | integer from 1 to 65535 or string | `"61001-65535"` | Ports, or ICMP identifiers, of the addresses the translated connections take: a port or a range `<first>-<last>` that leaves out the kernel's ephemeral ports, 32768 to 60999, which the router's own connections take. |
| `spec.pool4[].prefix` | string | required | IPv4 addresses the translated connections leave from, a range of 65536 addresses at most. |
| `spec.pool4[].protocols` | list of one of `tcp`, `udp`, `icmp` | `["tcp","udp","icmp"]` | Protocols whose connections take the addresses and ports. |
| `spec.prefix` | string | `"64:ff9b::/96"` | IPv6 range whose addresses the NAT64 translates, each embedding an IPv4 address as RFC 6052 says: 32, 40, 48, 56, 64 or 96 bits, by default the well-known prefix. The router routes it to the NAT64's link in the NAT64's VRF, a route that BGP announces when `BGP.spec.networks` of that VRF names the prefix. |
| `spec.vrf` | string or null | `null` | VRF document whose links route the prefix to the NAT64 and carry its translated connections; null for the default VRF. The NAT64 runs in the network namespace nat64-&lt;name>, which the link n64-&lt;name> joins to the VRF. |

### NATRule { #natrule }

An address translation of the router, which applies whether or not any FirewallPolicy exists: masquerade or source NAT of connections that leave, destination NAT of connections that arrive.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 7 [change rules](builtins.md#rules-natrule).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.family` | one of `ipv4`, `ipv6` | `"ipv4"` | Address family the rule translates. |
| `spec.in` | object or null | `null` | Where the connections arrive, for `dnat`. |
| `spec.in.interface` | string or null | `null` | Interface the connections use, by name or glob, a glob being a prefix followed by `*`; excludes `zone`. |
| `spec.in.zone` | string or null | `null` | FirewallZone whose interfaces the connections use; excludes `interface`. |
| `spec.match.destination` | list of string | `[]` | Destination address ranges of the connections; empty matches every destination. |
| `spec.match.destinationPorts` | list of integer from 1 to 65535 or string | `[]` | Destination ports, each a port or a range `<first>-<last>`, with `protocol`; empty matches every port. |
| `spec.match.protocol` | one of `tcp`, `udp`, `sctp` or null | `null` | Protocol of the connections; null matches every protocol. |
| `spec.match.source` | list of string | `[]` | Source address ranges of the connections; empty matches every source. |
| `spec.out` | object or null | `null` | Where the connections leave, for `masquerade` and `snat`. |
| `spec.out.interface` | string or null | `null` | Interface the connections use, by name or glob, a glob being a prefix followed by `*`; excludes `zone`. |
| `spec.out.zone` | string or null | `null` | FirewallZone whose interfaces the connections use; excludes `interface`. |
| `spec.to` | object or null | `null` | What `snat` and `dnat` translate to. |
| `spec.to.address` | string | required | Address the rule translates to. |
| `spec.to.port` | integer from 1 to 65535 or null | `null` | Port the rule translates to, with `match.protocol`; null keeps the port. |
| `spec.type` | one of `masquerade`, `snat`, `dnat` | required | `masquerade` translates the source of a leaving connection to the address of the interface it leaves through, `snat` to `to.address`, and `dnat` the destination of an arriving connection to `to.address`. |

### PolicyRoute { #policyroute }

Policy routing of the router: rules that send the forwarded traffic they match, coming in on a zone's links, on interfaces or on any link, to a VRF's routing table, another table or a next hop, whatever its destination's route. The first rule that matches decides; documents apply in the order of their names.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 16 [change rules](builtins.md#rules-policyroute).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.in` | object or null | `null` | Where the traffic comes in: a zone or interfaces; null for every link. |
| `spec.in.interfaces` | list of string | `[]` | Interfaces the traffic comes in on, by name or glob, a glob being a prefix followed by `*`; excludes `zone`. |
| `spec.in.zone` | string or null | `null` | FirewallZone whose links the traffic comes in on; excludes `interfaces`. |
| `spec.rules` | list of object | required | Rules in order: the first that matches a packet decides where it goes. |
| `spec.rules[].match` | object | required | What the rule matches, at least one term: a rule for every packet says so, such as with `source: [0.0.0.0/0]`. |
| `spec.rules[].match.destination` | list of string | `[]` | Destination address ranges, or one `set:<name>` for the ranges of an AddressSet; empty matches every destination. |
| `spec.rules[].match.destinationPorts` | list of integer from 1 to 65535 or string | `[]` | Destination ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port. |
| `spec.rules[].match.dscp` | list of integer from 0 to 63 | `[]` | DSCP values of the packets, each from 0 to 63; empty matches every value. |
| `spec.rules[].match.family` | one of `ipv4`, `ipv6` or null | `null` | Address family the rule matches; null matches each family its protocol and addresses leave. |
| `spec.rules[].match.protocol` | one of `tcp`, `udp`, `sctp`, `icmp`, `icmpv6`, `gre`, `esp`, `ah` or null | `null` | Protocol the rule matches; null matches every protocol. `icmp` is IPv4's and `icmpv6` IPv6's. |
| `spec.rules[].match.source` | list of string | `[]` | Source address ranges, or one `set:<name>` for the ranges of an AddressSet; empty matches every source. In each family the rule matches the ranges of that family. |
| `spec.rules[].match.sourcePorts` | list of integer from 1 to 65535 or string | `[]` | Source ports, each a port or a range `<first>-<last>`, with the protocol `tcp`, `udp` or `sctp`; empty matches every port. |
| `spec.rules[].to` | object | required | Where the traffic the rule matches goes: `vrf`, `table` or `nextHop`. |
| `spec.rules[].to.nextHop` | object or null | `null` | Router the traffic leaves through, by the default route of a table of its own; excludes `vrf` and `table`. |
| `spec.rules[].to.nextHop.address` | string | required | Address of the router the traffic leaves through, of the family the rule matches. |
| `spec.rules[].to.nextHop.interface` | string | required | Link the next hop is on, which a Network document names alone in its `[Match]` section; the route goes into that Network. |
| `spec.rules[].to.nextHop.table` | integer from 1 to 4294967295 | required | Routing table of the next hop: a default route through it and an unreachable default route after it, so that the traffic is dropped while the link is down. One next hop's alone; none of 253, 254 and 255, no VRF's table and no rule's `table`. |
| `spec.rules[].to.table` | integer from 1 to 4294967295 or null | `null` | Routing table that routes the traffic, by number: 254 is the main table. Not 255, the kernel's local table, and no VRF's table, which `vrf` names. A destination the table has no route to takes the route it would take without the policy; excludes `vrf` and `nextHop`. |
| `spec.rules[].to.vrf` | string or null | `null` | VRF document whose routing table routes the traffic; excludes `table` and `nextHop`. |

### PrefixList { #prefixlist }

Address ranges of one family that a RoutingPolicy term matches routes against: entries in order, the first that matches a route deciding.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 2 [change rules](builtins.md#rules-prefixlist).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.entries` | list of object | required | Entries in order; a route no entry matches does not match the list. |
| `spec.entries[].action` | one of `permit`, `deny` | `"permit"` | Whether a route the entry matches matches the list. |
| `spec.entries[].ge` | integer from 0 to 128 or null | `null` | Shortest prefix length of a route within `prefix` that the entry matches, from the prefix's length; null matches `prefix` alone, unless `le` is given. |
| `spec.entries[].le` | integer from 0 to 128 or null | `null` | Longest prefix length of a route within `prefix` that the entry matches; null matches `prefix` alone, unless `ge` is given. |
| `spec.entries[].prefix` | string | required | Address range the entry matches. |
| `spec.family` | one of `ipv4`, `ipv6` | required | Address family of the list. |

### Resolver { #resolver }

A DNS resolver of the router in one VRF, Unbound: the addresses it answers on, whom it answers, where it sends queries, and its DNS64 and local zones.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 19 [change rules](builtins.md#rules-resolver).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.allow` | list of string | required | Address ranges of the clients the resolver answers; it refuses others, and the firewall accepts DNS from these alone. A family's whole range, `0.0.0.0/0` or `::/0`, answers whoever reaches the addresses of `listen`: an open resolver, if one of them faces the internet. |
| `spec.dns64` | object or null | `null` | DNS64: AAAA records the resolver synthesises for IPv4-only names, and its answer for `ipv4only.arpa`; null synthesises none. |
| `spec.dns64.prefix` | string | `"64:ff9b::/96"` | NAT64 prefix the resolver synthesises AAAA records in, from the A records of names that have no AAAA record. |
| `spec.dnssec` | boolean | `true` | Whether the resolver validates DNSSEC with the root's trust anchor of the image. The forwarders then pass DNSSEC records on. |
| `spec.forwarders` | list of string | `[]` | Resolvers the router sends its queries to; empty resolves names itself, from the root servers. |
| `spec.listen` | list of string | required | Addresses of the router in the resolver's VRF on which it answers on port 53, over UDP and TCP; the firewall accepts DNS on them from `allow`. |
| `spec.localZones` | list of object | `[]` | Zones the resolver answers from its own records, 150 records in all at most. With `dns64`, no zone holds `ipv4only.arpa`, which DNS64 answers. |
| `spec.localZones[].name` | string | required | Name of the zone, in lower case and without the root's dot. |
| `spec.localZones[].records` | list of object | `[]` | Records the router answers for names of the zone. |
| `spec.localZones[].records[].name` | string | required | Name of the record: the zone's name or a name below it. |
| `spec.localZones[].records[].ttl` | integer from 0 to 2147483647 | `3600` | Seconds a resolver may keep the record. |
| `spec.localZones[].records[].type` | one of `A`, `AAAA`, `CNAME`, `PTR`, `MX`, `SRV`, `TXT` | required | Type of the record. |
| `spec.localZones[].records[].value` | string | required | Data of the record, of up to 255 printable ASCII characters other than quotes and backslashes: an address for `A` and `AAAA`, a name for `CNAME` and `PTR`, `<preference> <name>` for `MX`, `<priority> <weight> <port> <name>` for `SRV`, and text for `TXT`. |
| `spec.localZones[].type` | one of `static`, `transparent`, `typetransparent`, `redirect`, `refuse`, `deny`, `always_nxdomain` | `"static"` | How Unbound answers names of the zone, as its `local-zone` types say: `static` answers from the records alone, `transparent` resolves the names the records lack, `typetransparent` also the types they lack, `redirect` answers every name below the zone with the zone's own records, `refuse` and `deny` refuse or drop the queries the records do not answer, and `always_nxdomain` answers that no name exists. |
| `spec.vrf` | string or null | `null` | VRF document on whose links the resolver answers and through whose links it sends its queries; null for the default VRF. |

### Router { #router }

How the router forwards: the families it forwards, its reverse-path filter, how it spreads flows over equal-cost routes, its connection table and the interfaces that reach its node API.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config holds at most one. It has a [change rule](builtins.md#rules-router).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.conntrackMax` | integer of at least 1 or null | `null` | Connections the router tracks at most, `net.netfilter.nf_conntrack_max`; null keeps the kernel's default, which follows the memory. |
| `spec.ecmpHash` | one of `l3`, `l4` | `"l4"` | What the router hashes to pick one of several equal-cost routes for a flow, in both families: the addresses (`l3`), or the addresses, the protocol and the ports (`l4`). |
| `spec.forwarding.ipv4` | boolean | `true` | Whether the router forwards IPv4 packets between its interfaces. |
| `spec.forwarding.ipv6` | boolean | `true` | Whether the router forwards IPv6 packets between its interfaces. |
| `spec.management.interfaces` | list of string or null | `null` | Interfaces, by name or glob, a glob being a prefix followed by `*`, on which the firewall accepts the node API, chalkd's TCP port 50000; null accepts it on every interface. |
| `spec.rpFilter` | one of `off`, `loose`, `strict` | `"loose"` | Reverse-path filter of the packets the router receives, in both families: `strict` drops a packet whose source the router would not route back out of the interface it came in on, `loose` one whose source it has no route to, and `off` drops none. DHCPv4 between ports 67 and 68 passes. |

### RoutingPolicy { #routingpolicy }

What the router does with the routes of a peer's family or of a network: terms in order, the first that matches a route deciding, then a default.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 4 [change rules](builtins.md#rules-routingpolicy).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.default` | one of `accept`, `reject` | `"reject"` | What the router does with a route no term matches. |
| `spec.terms` | list of object | `[]` | Terms in order: the first whose match holds for a route decides. |
| `spec.terms[].match.asPath` | string or null | `null` | Regular expression the route's AS path matches, with `_` for the start, the end or a space between two ASes; null matches every path. It holds up to 200 digits, single spaces and `_^$.*+?()|{},-`. The schema and rules check these characters alone; the output frr refuses an expression that bgpd cannot compile. |
| `spec.terms[].match.communityList` | string or null | `null` | CommunityList one of whose communities the route carries; null matches every route. |
| `spec.terms[].match.prefixList` | string or null | `null` | PrefixList the route's prefix matches; null matches every prefix. |
| `spec.terms[].then` | object | required |  |
| `spec.terms[].then.action` | one of `accept`, `reject` | required | Whether the router takes a route the term matches. |
| `spec.terms[].then.addCommunities` | list of string | `[]` | Communities added to the route: standard communities `<AS>:<value>` of 16-bit numbers, or large ones `<AS>:<value>:<value>` of 32-bit numbers. |
| `spec.terms[].then.localPref` | integer from 0 to 4294967295 or null | `null` | Local preference the route gets; null keeps it. |
| `spec.terms[].then.med` | integer from 0 to 4294967295 or null | `null` | Multi-exit discriminator the route gets; null keeps it. |
| `spec.terms[].then.prepend` | integer from 1 to 10 or null | `null` | How often the router puts its own AS number in front of the route's AS path; null puts none. |
| `spec.terms[].then.removeCommunities` | list of string | `[]` | Communities removed from the route. |

### VRF { #vrf }

A VRF of the router: a kernel VRF device of the document's name, its routing table and the links it holds. Other router kinds name it by its name; the default VRF, of every link no VRF holds, has no document.

`apiVersion: router.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 11 [change rules](builtins.md#rules-vrf).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.conntrackZone` | integer from 1 to 65535 or null | `null` | Conntrack zone of the connections through the VRF's links, which no other VRF uses; null shares zone 0 with the default VRF and every VRF without one. A zone keeps the state and NAT of the VRF's connections apart from those of other VRFs with the same addresses. A VRF with a zone neither imports routes from another VRF nor gives its routes to one. |
| `spec.interfaces` | list of string | `[]` | Links the VRF holds, by name or glob, a glob being a prefix followed by `*`; a glob stands for the links the node's Network documents name in a `[Match]` section's `Name=`. A link is named by a Network document, which gets `VRF=`, and every link of that Network is in this VRF. |
| `spec.table` | integer from 1 to 4294967295 | required | Routing table of the VRF, which no other VRF uses; not 253, 254 or 255, the kernel's tables of the default VRF. A change creates the VRF anew, which ends the sessions in it. |

## storage.chalkos.dev { #storage-chalkos-dev }

The kinds of `storage.chalkos.dev` lay out the node's disks: the system disk and the volumes.

### SystemDisk { #systemdisk }

The disk that holds the node's system region and VAR, and the encryption of the node's storage.

`apiVersion: storage.chalkos.dev/v1alpha1`

A node's config holds at most one. It has a [change rule](builtins.md#rules-systemdisk).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.disk` | object | required | Disk holding the system region and VAR. |
| `spec.disk.model` | string or null | `null` | Glob the disk's model matches, such as `Samsung SSD 870*`. |
| `spec.disk.path` | string or null | `null` | Path of the disk below /dev, such as /dev/disk/by-id/…; excludes the selector keys. |
| `spec.disk.serial` | string or null | `null` | The disk's serial number. |
| `spec.disk.size` | string or null | `null` | Comparison the disk's size meets, such as `>= 1T`; the size has at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P. |
| `spec.disk.type` | one of `nvme`, `ssd`, `hdd` or null | `null` | Kind of disk. |
| `spec.disk.wwn` | string or null | `null` | The disk's World Wide Name. |
| `spec.encryption.fallback` | one of `recovery-key`, `password`, `none` | `"recovery-key"` | Second keyslot asked for on the console when TPM2 unsealing fails. |
| `spec.encryption.mode` | one of `tpm2`, `none` | `"tpm2"` | Encryption of STATE, VAR and volumes: `tpm2` seals a LUKS2 key to PCR 7, `none` uses no encryption. |
| `spec.var.encryption.mode` | one of `tpm2`, `none` or null | `null` | Encryption mode of VAR, over the node's; null takes the node's. |
| `spec.var.size` | string or null | `null` | Size of VAR, such as `20G`, with at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P; null fills the remaining space on the system disk. |

### Volume { #volume }

A partition for the node's data, on the system disk or on a disk of its own, which the node creates and mounts.

`apiVersion: storage.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 7 [change rules](builtins.md#rules-volume).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.disk` | object or null | `null` | Disk the volume occupies on its own; null places it on the system disk. |
| `spec.disk.model` | string or null | `null` | Glob the disk's model matches, such as `Samsung SSD 870*`. |
| `spec.disk.path` | string or null | `null` | Path of the disk below /dev, such as /dev/disk/by-id/…; excludes the selector keys. |
| `spec.disk.serial` | string or null | `null` | The disk's serial number. |
| `spec.disk.size` | string or null | `null` | Comparison the disk's size meets, such as `>= 1T`; the size has at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P. |
| `spec.disk.type` | one of `nvme`, `ssd`, `hdd` or null | `null` | Kind of disk. |
| `spec.disk.wwn` | string or null | `null` | The disk's World Wide Name. |
| `spec.encryption.mode` | one of `tpm2`, `none` or null | `null` | Encryption mode of the volume, over the node's; null takes the node's. |
| `spec.format` | one of `ext4`, `xfs`, `btrfs`, `swap`, `raw` | `"ext4"` | File system of the volume; `raw` leaves a raw block device, which has no mountPoint. |
| `spec.mountPoint` | string or null | `null` | Where the volume is mounted; null leaves it unmounted. |
| `spec.repart` | map of string | `{}` | Extra repart.d keys of the volume's partition. Type, Label, Encrypt, Format, SizeMinBytes, SizeMaxBytes and MountPoint come from the typed options. |
| `spec.size` | string or null | `null` | Size of the volume, such as `100G`, with at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P; null fills the remaining space on its disk. |

## time.chalkos.dev { #time-chalkos-dev }

The kinds of `time.chalkos.dev` set how the node synchronises its clock.

### TimeSync { #timesync }

The time servers the node synchronises its clock with.

`apiVersion: time.chalkos.dev/v1alpha1`

A node's config holds at most one.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.dhcp` | boolean | `false` | Whether the node also synchronises with the time servers the network announces through DHCP, which are not authenticated. |
| `spec.servers` | list of object | `[{"host":"ptbtime1.ptb.de"},{"host":"ptbtime2.ptb.de"},{"host":"ptbtime3.ptb.de"}]` | Time servers the node synchronises its clock with, authenticated with NTS unless `nts = false`. |
| `spec.servers[].host` | string | required | Host name or address of the time server. |
| `spec.servers[].nts` | boolean | `true` | Whether the node authenticates the server with Network Time Security (NTS). |
| `spec.servers[].port` | integer from 0 to 65535 or null | `null` | UDP port of the server's NTP; null is NTP's port, 123. |

## Inventory and lab files { #inventory-and-lab-files }

chalkctl reads an inventory and chalklab a lab file. Neither is part of a node's config.

### Inventory { #inventory }

The nodes of a cluster, where chalkctl reaches each, the image each runs and the config files each gets. Paths are relative to the inventory's directory, and file lists take globs.

`apiVersion: chalkctl.chalkos.dev/v1alpha1`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.all` | object | — | What every node gets: the image of the nodes that name none and whose groups name none, and the config files every node gets first. |
| `spec.all.files` | list of string | — | Config files the nodes under it get. |
| `spec.all.imageName` | string | — | The image of the nodes under it that name none of their own. |
| `spec.groups` | map of object | — | Groups of nodes by name, which a node joins by naming them in its groups. |
| `spec.groups.<name>.files` | list of string | — | Config files the nodes under it get. |
| `spec.groups.<name>.imageName` | string | — | The image of the nodes under it that name none of their own. |
| `spec.images` | map of object | — | The images the nodes run, by name. |
| `spec.images.<name>.source` | object | required | Where the image comes from: exactly one of directory and oci. |
| `spec.images.<name>.source.directory` | string | — | Directory of an image build as nix build makes it, holding its metadata.json; a relative directory is relative to the inventory's directory. |
| `spec.images.<name>.source.oci` | string | — | Reference of the image in an OCI registry, naming the registry and a tag or a digest, such as ghcr.io/example/node-metal:v1 or the repository@sha256:… chalkbuild push prints. chalkctl resolves a tag to its digest once per command and reads the image's header and contract layer with the credentials of docker's config; an upgrade, and an install on a node that runs the installer, have the node pull the image by that digest. |
| `spec.nodes` | map of object | — | The cluster's nodes by name. A node's name is fixed at install, which writes it into the node's certificate; its hostname defaults to it, and the Kubernetes node name, the storage seeds and the recovery key derive from it, so renaming a node takes a reinstall. |
| `spec.nodes.<name>.address` | string | — | Where chalkctl reaches the node's chalkd: a host or host:port. |
| `spec.nodes.<name>.files` | list of string | — | The node's own config files. |
| `spec.nodes.<name>.groups` | list of string | — | Groups whose files the node gets, in this order; the node runs their image unless it names its own. |
| `spec.nodes.<name>.imageName` | string | — | The image the node runs, over that of its groups and spec.all.imageName. |

### Lab { #lab }

The nodes of an inventory that chalklab runs as virtual machines, and their machines.

`apiVersion: chalklab.chalkos.dev/v1alpha1`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.inventory` | string | required | The inventory of the cluster, relative to the lab file. |
| `spec.nodes` | map of object | required | The VMs of the lab by the names of their nodes in the inventory. |
| `spec.nodes.<name>.cpus` | integer of at least 1 | `2` | Number of the VM's virtual CPUs. |
| `spec.nodes.<name>.disks` | list of object | `[{"size":"16G"}]` | The VM's disks: the first holds the node's image, the others start empty. |
| `spec.nodes.<name>.disks[].serial` | string | — | Serial number the disk reports to the node, up to 20 letters, digits, _ and -, for a SystemDisk or Volume to select it by. |
| `spec.nodes.<name>.disks[].size` | string | required | Size of the disk in bytes, or with a K, M, G or T suffix, such as 16G. |
| `spec.nodes.<name>.mac` | string | required | MAC address of the VM's NIC on the lab network, which the node's Network document must match. |
| `spec.nodes.<name>.memory` | integer of at least 512 | — | Memory of the VM in MiB; without it, 3072 for a control plane and 2048 for another node. |

Sources: the JSON Schemas in the `schemas` directory of an image
[build](image-layout.md#outputs-of-an-image-build) of the default variant and of a router
image, from `modules/node/kinds/builtin/` and the tables of the image's systemd-networkd, and
`pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json` and
`pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json`.
