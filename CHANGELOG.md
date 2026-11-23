# Changelog

## [0.7.1](https://github.com/trevex/chalkos/compare/v0.7.0...v0.7.1) (2026-11-23)


### Bug Fixes

* **client:** name the node when its own deadline ends the call first ([#149](https://github.com/trevex/chalkos/issues/149)) ([5ca6340](https://github.com/trevex/chalkos/commit/5ca6340ea51ef49f47808f657d33883fbfcaad29))


### Dependencies

* update the flake inputs ([#148](https://github.com/trevex/chalkos/issues/148)) ([6a55c8d](https://github.com/trevex/chalkos/commit/6a55c8dddace51127a9515f2a3358dc82f626f8b))

## [0.7.0](https://github.com/trevex/chalkos/compare/v0.6.0...v0.7.0) (2026-11-22)


### Features

* **kubernetes:** sign kubelet serving certificates with a CA of their own ([#136](https://github.com/trevex/chalkos/issues/136)) ([8ed39cf](https://github.com/trevex/chalkos/commit/8ed39cf08cf5a1d862cab8e13c543fd8e8715a10))
* start FRR only with a BGPSpeaker, narrow BFD and cap renderers ([#139](https://github.com/trevex/chalkos/issues/139)) ([7e4512e](https://github.com/trevex/chalkos/commit/7e4512e8412059f8c0048ae8194f714fe24a6686))


### Bug Fixes

* read the node's refusal behind a closed connection and stop the etcd join flake ([#141](https://github.com/trevex/chalkos/issues/141)) ([5f6d808](https://github.com/trevex/chalkos/commit/5f6d8083369b472ca5844b0391fd3ffd3d982764))
* resilience fixes from the November review ([#138](https://github.com/trevex/chalkos/issues/138)) ([933a366](https://github.com/trevex/chalkos/commit/933a3666fc325b6b3a6d13c757d9dab4fdb5b2ad))
* security fixes from the November review ([#134](https://github.com/trevex/chalkos/issues/134)) ([3e05e88](https://github.com/trevex/chalkos/commit/3e05e888012c3c237c94e44547698839d28784c0))

## [0.6.0](https://github.com/trevex/chalkos/compare/v0.5.0...v0.6.0) (2026-11-22)


### Features

* **kubernetes:** add the longhorn feature with iscsid, the NFS client and host tools ([#80](https://github.com/trevex/chalkos/issues/80)) ([31f99c3](https://github.com/trevex/chalkos/commit/31f99c366124f163a940e48aaa6a48b34da3dc5f))
* **kubernetes:** add the nvidia feature for the GPU operator's host-driver mode ([#82](https://github.com/trevex/chalkos/issues/82)) ([dbe6875](https://github.com/trevex/chalkos/commit/dbe68754ba010ec899622d4191a29bb141c9f652))
* **kubernetes:** carry Ceph's and KubeVirt's kernel modules by default ([#79](https://github.com/trevex/chalkos/issues/79)) ([2ae630d](https://github.com/trevex/chalkos/commit/2ae630d7af7779db8ec265be7fa02ac5a2a96623))
* **kubernetes:** link /lib/modules to the image's modules and add host tools below /usr ([#78](https://github.com/trevex/chalkos/issues/78)) ([dc95b59](https://github.com/trevex/chalkos/commit/dc95b5975a7348724127f3f63272a94a345b5bfb))
* **node:** load kernel modules from the image alone ([#76](https://github.com/trevex/chalkos/issues/76)) ([6f41a9e](https://github.com/trevex/chalkos/commit/6f41a9e1711a41e05fd3c7426d452869beb63220))

## [0.5.0](https://github.com/trevex/chalkos/compare/v0.4.0...v0.5.0) (2026-11-21)


### Features

* **node:** fail the build when a shell enters a node image ([#69](https://github.com/trevex/chalkos/issues/69)) ([b720109](https://github.com/trevex/chalkos/commit/b7201094306cb4bb160145e1bff479a6ba433e63))
* **node:** keep chalkos's units and the router's daemons from running programs outside the store ([#75](https://github.com/trevex/chalkos/issues/75)) ([69b0640](https://github.com/trevex/chalkos/commit/69b064058f1a6f65a06f40009be0c426753e559a))
* **node:** mount writable file systems noexec ([#70](https://github.com/trevex/chalkos/issues/70)) ([ed18d56](https://github.com/trevex/chalkos/commit/ed18d5640d117bb9d492f7716b305ba098ed86fe))
* **router:** run the layer's unit scripts as chalkd subcommands ([#68](https://github.com/trevex/chalkos/issues/68)) ([5725cd9](https://github.com/trevex/chalkos/commit/5725cd9edcf69f3eed32910a5fe424cfaeee3362))


### Bug Fixes

* **client:** report a connection the node reset as unavailable ([#73](https://github.com/trevex/chalkos/issues/73)) ([f1aac0f](https://github.com/trevex/chalkos/commit/f1aac0f17aeeed2691fea0a5ef1f1c2afee563fe))
* **node:** check that the console keymap loads without a shell ([#72](https://github.com/trevex/chalkos/issues/72)) ([973c6eb](https://github.com/trevex/chalkos/commit/973c6eb079ee5d5326ea064925a05fa3f1c2bef7))
* **router:** keep the delegated prefixes' records and their staging directory on one mount ([#74](https://github.com/trevex/chalkos/issues/74)) ([44e266f](https://github.com/trevex/chalkos/commit/44e266f898f038ef4481cbb812385bc5704dca13))

## [0.4.0](https://github.com/trevex/chalkos/compare/v0.3.2...v0.4.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **outputs:** chalkos.outputs.<name>.check takes a program, not shell lines. Replace a check such as   check = "${bird}/bin/bird -p -c $OUT/bird.conf"; with   check = chalkosLib.check.run [ "${bird}/bin/bird" "-p" "-c" "$OUT/bird.conf" ]; a loop over files with chalkosLib.check.each "*.conf" [ ... "$FILE" ], and anything else with a program of your own, such as a pkgs.writeShellScript that sets PATH itself, since the check's PATH holds no coreutils, grep or sed any more.

### Features

* **frr:** run FRR's daemons as chalkos units, without watchfrr or a shell ([#65](https://github.com/trevex/chalkos/issues/65)) ([1da8b00](https://github.com/trevex/chalkos/commit/1da8b00c32038a0700c952aa8d33ea7dd9376f72))
* **kubernetes:** run the layer's unit scripts as chalkd subcommands ([#66](https://github.com/trevex/chalkos/issues/66)) ([41eff81](https://github.com/trevex/chalkos/commit/41eff81e9960fff0554cbc5ee5db451b01c61b2b))
* **lib:** give every image its SBOM ([#59](https://github.com/trevex/chalkos/issues/59)) ([111a9a8](https://github.com/trevex/chalkos/commit/111a9a8b454b583fed90f2d70ac98f91eb422d48))
* **node:** activate images without a shell ([#62](https://github.com/trevex/chalkos/issues/62)) ([b200bf2](https://github.com/trevex/chalkos/commit/b200bf2830aed512d04bbae6ddb14323375bb14c))
* **node:** run the common unit scripts as chalkd subcommands ([#64](https://github.com/trevex/chalkos/issues/64)) ([bbabaa0](https://github.com/trevex/chalkos/commit/bbabaa08379de5b8b5e844eb0ac62f617b5a59a9))
* **outputs:** run an output's check as a program ([#63](https://github.com/trevex/chalkos/issues/63)) ([2361a2e](https://github.com/trevex/chalkos/commit/2361a2ed08022af4b87329eaf9f26c4b6925c6b5))


### Bug Fixes

* **kubernetes:** let CNI DaemonSets install their plugins ([#61](https://github.com/trevex/chalkos/issues/61)) ([2a7048b](https://github.com/trevex/chalkos/commit/2a7048b48fc4d552ad2f4de92f47fccb499a88f6))

## [0.3.2](https://github.com/trevex/chalkos/compare/v0.3.1...v0.3.2) (2026-11-21)


### Bug Fixes

* **rules:** keep the rules' cost estimates on cel-go 0.33 and update the vendor hash ([#54](https://github.com/trevex/chalkos/issues/54)) ([b112e1c](https://github.com/trevex/chalkos/commit/b112e1cec20a946357648a93cab01619d6fa4623))


### Dependencies

* update the go modules ([dae95ee](https://github.com/trevex/chalkos/commit/dae95ee30c5e002f3582cf6f929d51228babe84c))
* upgrade bubbletea and lipgloss to v2 ([45d75a2](https://github.com/trevex/chalkos/commit/45d75a2d9a343b4e70a8006a8981eaf3ad375290))
* upgrade connect to v2 ([dc92600](https://github.com/trevex/chalkos/commit/dc92600f9a77ffa04d4aaa6f3d2a31846b28dee6))
* upgrade go.yaml.in/yaml to v4 ([c326195](https://github.com/trevex/chalkos/commit/c326195d7f82f88ec577e4b33f50bed452d1fe53))

## [0.3.1](https://github.com/trevex/chalkos/compare/v0.3.0...v0.3.1) (2026-11-20)


### Bug Fixes

* **installer:** leave the CD-ROM's loop device to systemd ([#42](https://github.com/trevex/chalkos/issues/42)) ([33c27e2](https://github.com/trevex/chalkos/commit/33c27e2f1fd9bcbece6c83cb802261b64591e166))
* **release:** give the programs' SBOMs a serial number so they can be attested ([#40](https://github.com/trevex/chalkos/issues/40)) ([5ea5b21](https://github.com/trevex/chalkos/commit/5ea5b21cf653e8e89c293f20be561b7abe61a702))

## [0.3.0](https://github.com/trevex/chalkos/compare/v0.2.2...v0.3.0) (2026-11-20)


### Features

* **release:** publish SBOMs of the images and programs ([#37](https://github.com/trevex/chalkos/issues/37)) ([561aa04](https://github.com/trevex/chalkos/commit/561aa04f0baaef7d6b7cce191313df9843cf14b8))


### Bug Fixes

* **deps:** update the Go modules with vulnerabilities chalkos calls ([61f9ff5](https://github.com/trevex/chalkos/commit/61f9ff5644d4ad10af07c87a650c2e350ab94850))
* **deps:** update the vendor hash for the merged module updates ([#39](https://github.com/trevex/chalkos/issues/39)) ([0afd247](https://github.com/trevex/chalkos/commit/0afd247a98b463dac0c8e53c21116a227324bd30))

## [0.2.2](https://github.com/trevex/chalkos/compare/v0.2.1...v0.2.2) (2026-11-20)


### Bug Fixes

* **docs:** name the v0.2.1 images in the quick starts' lab output ([08e520b](https://github.com/trevex/chalkos/commit/08e520b98a1db64d8316b7377c4a04bc25c53f6b))

## [0.2.1](https://github.com/trevex/chalkos/compare/v0.2.0...v0.2.1) (2026-11-20)


### Bug Fixes

* **templates:** name the v0.2.0 images in the lab templates ([c5ed227](https://github.com/trevex/chalkos/commit/c5ed227f57ab7cedaad7cc7c3cd58b375e0e5738))

## [0.2.0](https://github.com/trevex/chalkos/compare/v0.1.0...v0.2.0) (2026-11-20)


### Features

* **chalkctl:** add image pull, which writes an image from a registry into a directory ([261cde5](https://github.com/trevex/chalkos/commit/261cde5cd43fd952695aa6e6403a8a8333fd87d3))
* **fatfs:** format, read and write a FAT file system at an offset in a file ([17a3331](https://github.com/trevex/chalkos/commit/17a3331c45ea223b3d9b5999db2412bda5404347))
* **gpt:** write the GPT of a disk image ([cc96c7b](https://github.com/trevex/chalkos/commit/cc96c7bc31cf2677a328019fdafa3e3daedb9052))
* **imagepull:** give a pulled disk image repart's identifiers and the same bytes on every pull, and name missing mtools before fetching ([57ccb42](https://github.com/trevex/chalkos/commit/57ccb425924567be7fcb94c052bef4182862898b))
* **imagepull:** pull a chalkos image into an image directory or its OCI layout ([ea2acb6](https://github.com/trevex/chalkos/commit/ea2acb61f50213045dc6dd13d992533245073d96))
* **secureboot:** sign boot loaders and UKIs in Go instead of with sbsign ([ea334c0](https://github.com/trevex/chalkos/commit/ea334c0408770bc1815e4cc2a2eb6e3d29572b8e))

## 0.1.0 (2026-11-20)


### Features

* **release:** publish each release with release-please and a release workflow ([054973f](https://github.com/trevex/chalkos/commit/054973f7f94562a71999bbf46977ea1a3486885c))
* **templates:** pin the templates' and examples' chalkos input to the release's tag ([0607735](https://github.com/trevex/chalkos/commit/060773596f4a93e57db1ac525c4d41dec0e5e5da))


### Bug Fixes

* **release:** keep IP addresses out of the blocks release-please versions ([affce74](https://github.com/trevex/chalkos/commit/affce7420204ed247f6dd8206064b8fe1c2b03d8))
