# Changelog

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
