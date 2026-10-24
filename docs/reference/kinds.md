---
title: "Config kinds"
description: "The fields of the config kinds of chalkos's images, of the inventory and of the lab file"
---

# Config kinds

This page is for operators who write a node's config documents, an inventory or a lab file. It
lists the config kinds that an image of `chalkos.lib.mkImages`'s default variant accepts, by API
group, and the fields of the inventory and the lab file that chalkctl and chalklab read. An image
whose modules declare kinds of their own under [`chalkos.kinds`](options.md#chalkoskinds)
accepts those as well.

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

A node's config holds at most one. It has 5 [change rules](../build/guard-changes.md).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.allowSchedulingOnControlPlanes` | boolean | `false` | Let workloads run on control-plane nodes, which otherwise register with the taint `node-role.kubernetes.io/control-plane:NoSchedule`; it is fixed once the node holds a Kubernetes share. |
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
| `spec.vip.mode` | `l2` | `"l2"` | How the holder announces the addresses. |

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

A node's config holds at most one. It has 3 [change rules](../build/guard-changes.md).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.kubeletExtraArgs` | map of string | `{}` | Extra flags of the kubelet, named without the leading dashes with letters, digits, `_`, `.` and `-`, starting with a letter or digit; they override chalkos's own, except the flags chalkos protects, which are refused. |
| `spec.labels` | map of string | `{}` | Kubernetes labels the node registers with; they are fixed once the node holds a Kubernetes share. |
| `spec.nodeIP.addresses` | list of string or null | `null` | Fixed addresses of the node, at most one per family; null takes the first static address of each family unless validSubnets apply. |
| `spec.nodeIP.validSubnets` | list of string or null | `null` | Subnets the node picks its address from, which replace the Cluster's nodeIP.validSubnets as a whole; null takes the Cluster's. |
| `spec.taints` | list of object | `[]` | Kubernetes taints the node registers with; they are fixed once the node holds a Kubernetes share. |
| `spec.taints[].effect` | one of `NoSchedule`, `PreferNoSchedule`, `NoExecute` | required | What happens to pods that do not tolerate the taint. |
| `spec.taints[].key` | string | required | Taint key. |
| `spec.taints[].value` | string or null | `null` | Taint value; null for a taint without a value. |
| `spec.type` | one of `controlplane`, `worker` | required | What the node is in the cluster; it is fixed once the node holds a Kubernetes share. |
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

A node's config may hold several, each with a name of its own. It has a [change rule](../build/guard-changes.md).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.endpoints` | list of string | required | URLs of the mirror's endpoints, which chalkd and containerd try in order before the registry itself. A path is where the endpoint serves the registry API, below <path>/v2 as containerd takes it. chalkd authenticates to an endpoint with the Registry of the endpoint's host, and an http:// endpoint needs that Registry to set insecure. |
| `spec.registry` | string | required | Host of the registry as image references name it, with its port unless it is 443; an IPv6 address in brackets. |

### Registry { #registry }

The credentials and transport of an OCI registry host, with which chalkd pulls OS images and, on images that run Kubernetes, containerd pulls container images.

`apiVersion: oci.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 2 [change rules](../build/guard-changes.md).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.host` | string | required | Host of a registry or of a mirror's endpoint, as image references and endpoints name it, with its port unless it is 443; an IPv6 address in brackets. chalkd pulls OS images from this host with the Registry's credentials, over plain HTTP when insecure is set. On images that run Kubernetes, the kubelet hands these credentials to containerd for the container images of this host, and containerd sends them to every endpoint of the host's Mirror too, over plain HTTP to an endpoint whose host is insecure. The kubelet compares the host, in lower case and with its port, case-sensitively with the image's host as the pod spec writes it, so pods name the host in lower case. An IPv6 host with credentials needs its port, 443 included, for the kubelet to match it. |
| `spec.insecure` | boolean | `false` | Allow plain HTTP to this host: chalkd and containerd reach it over HTTP, and a Mirror may name an http:// endpoint on it. Otherwise chalkd pulls over HTTPS alone, also from private and loopback addresses. Credentials and images then travel unencrypted, so use it only on a trusted network, such as a lab. |
| `spec.password` | secret reference or null | `null` | Password or token of the user, from a Secret; null pulls anonymously. |
| `spec.username` | string or null | `null` | User chalkd authenticates to the host as, with the password, and containerd too on images that run Kubernetes; null pulls anonymously. |

## storage.chalkos.dev { #storage-chalkos-dev }

The kinds of `storage.chalkos.dev` lay out the node's disks: the system disk and the volumes.

### SystemDisk { #systemdisk }

The disk that holds the node's system region and VAR, and the encryption of the node's storage.

`apiVersion: storage.chalkos.dev/v1alpha1`

A node's config holds at most one. It has a [change rule](../build/guard-changes.md).

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
| `spec.var.encryption.mode` | one of `tpm2`, `none` or null | `null` | Overrides the node's encryption mode for VAR. |
| `spec.var.size` | string or null | `null` | Size of VAR, such as `20G`, with at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P; null fills the remaining space on the system disk. |

### Volume { #volume }

A partition for the node's data, on the system disk or on a disk of its own, which the node creates and mounts.

`apiVersion: storage.chalkos.dev/v1alpha1`

A node's config may hold several, each with a name of its own. It has 7 [change rules](../build/guard-changes.md).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `spec.disk` | object or null | `null` | Disk the volume occupies on its own; null places it on the system disk. |
| `spec.disk.model` | string or null | `null` | Glob the disk's model matches, such as `Samsung SSD 870*`. |
| `spec.disk.path` | string or null | `null` | Path of the disk below /dev, such as /dev/disk/by-id/…; excludes the selector keys. |
| `spec.disk.serial` | string or null | `null` | The disk's serial number. |
| `spec.disk.size` | string or null | `null` | Comparison the disk's size meets, such as `>= 1T`; the size has at most 18 digits without a suffix, and 15, 12, 9, 6 or 3 with K, M, G, T or P. |
| `spec.disk.type` | one of `nvme`, `ssd`, `hdd` or null | `null` | Kind of disk. |
| `spec.disk.wwn` | string or null | `null` | The disk's World Wide Name. |
| `spec.encryption.mode` | one of `tpm2`, `none` or null | `null` | Overrides the node's encryption mode for this volume. |
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
| `spec.dhcp` | boolean | `false` | Also synchronise with the time servers the network announces through DHCP, which are not authenticated. |
| `spec.servers` | list of object | `[{"host":"ptbtime1.ptb.de"},{"host":"ptbtime2.ptb.de"},{"host":"ptbtime3.ptb.de"}]` | Time servers the node synchronises its clock with, authenticated with NTS unless `nts = false`. |
| `spec.servers[].host` | string | required | Host name or address of the time server. |
| `spec.servers[].nts` | boolean | `true` | Authenticate the server with Network Time Security (NTS). |
| `spec.servers[].port` | integer from 0 to 65535 or null | `null` | UDP port of the server's NTP; null is NTP's port, 123. |

## Inventory and lab files { #inventory-and-lab-files }

chalkctl reads an inventory and chalklab a lab file. Neither is part of a node's config.

### Inventory { #inventory }

The nodes of a cluster, where chalkctl reaches each, the image each runs and the config files each gets. Paths are relative to the inventory's directory; globs are allowed in file lists.

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
| `spec.nodes.<name>.cpus` | integer of at least 1 | — | Virtual CPUs; default 2. |
| `spec.nodes.<name>.disks` | list of object | — | The VM's disks: the first holds the node's image, the others start empty; default one of 16G. |
| `spec.nodes.<name>.disks[].serial` | string | — | Serial number the disk reports to the node, up to 20 letters, digits, _ and -, for a SystemDisk or Volume to select it by; none by default. |
| `spec.nodes.<name>.disks[].size` | string | required | Size of the disk in bytes, or with a K, M, G or T suffix, such as 16G. |
| `spec.nodes.<name>.mac` | string | required | MAC address of the VM's NIC on the lab network, which the node's Network document must match. |
| `spec.nodes.<name>.memory` | integer of at least 512 | — | Memory in MiB; default 3072 for a control plane, 2048 for another node. |

Sources: the JSON Schemas in the `schemas` directory of an image
[build](image-layout.md#outputs-of-an-image-build) of the default variant, from
`modules/node/kinds/builtin.nix` and the tables of the image's systemd-networkd, and
`pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json` and
`pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json`.
