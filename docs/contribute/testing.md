---
title: "Testing"
description: "Unit, namespace, evaluation and end-to-end tests, and when to write which"
---

# Testing

chalkos has five kinds of tests: Go unit tests, tests in a network namespace or on disk image
files, Nix evaluation and build checks, image tests of what an image makes of a config, and
end-to-end tests that boot virtual machines. Each change is proved at the cheapest level that
can prove it, because the VM tests take minutes and gigabytes of memory each, and the whole suite
runs on a developer's machine. This page explains each kind, where it lives and how to run it.

## Which test to write?

Start at the top of the table and go down only when the level above cannot show the behaviour.

| Kind | Where | Proves | Cost |
| --- | --- | --- | --- |
| Go unit test | next to the code, `*_test.go` in `pkg/` and `cmd/` | logic, parsing, error paths, command output | seconds |
| Namespace or disk-image test | `pkg/kubernetes/vip`, `pkg/install/parts_test.go`, `pkg/upgrade/lab_test.go`, the `vxlan-rule`, `router-firewall`, `router-policy-routing` and `router-nat64` checks | network rules, partition tables and file systems written by the real tools | seconds |
| Evaluation test | `test/nix/eval.nix`, `test/nix/kinds.nix` | the arguments of `mkImages`, option defaults, assertions, what a module sets in an image, config kinds | one evaluation |
| Image test | `chalkos.tests` of a module, with golden files such as those in `modules/node/router/tests` | the files an image renders from a config, the refusals it gives and the effects a change takes | one run of the image's renderers and validators |
| Build check | `nix/checks/` | what an image build produces: kernel modules, sizes, the bootspec | one image build |
| End-to-end test | `test/e2e/` | behaviour that needs a booted node: firmware, Secure Boot, the TPM, the initrd, a cluster | minutes and 2 GiB or more per VM |

## Go unit tests

Unit tests run with `go test` and take no privileges. Commands are tested in-process through
`NewCommand`, with their output captured. Run them in the [development shell](development.md):

```sh
go test ./pkg/... ./cmd/...
```

The `go-unit` check runs the same with `-v` in the Nix sandbox and fails when any test was
skipped or none ran, because a test skips when a tool or variable it needs is missing; a check
that passed on skipped tests would prove nothing.

The `go-lint` check fails when gofmt would change a Go file, or when go vet or staticcheck
reports a finding in `pkg/`, `cmd/` or `test/`. It compiles the end-to-end tests without running
them, so it also catches their dead code. [Development setup](development.md#format-and-lint)
shows how to run the same tools in the development shell.

## Tests in a namespace or on a disk image

Some behaviour depends on the kernel or on real tools, but not on a booted node. These tests
run them in isolation:

- The VIP tests in `pkg/kubernetes/vip` re-run themselves in
  a new user and network namespace, where they add addresses to interfaces and capture the
  frames that announce them.
- The install tests in `pkg/install/parts_test.go` and the upgrade tests in
  `pkg/upgrade/lab_test.go` partition a disk image file with the real sfdisk, and
  systemd-repart for an install, and stand in for the ESP's
  mount, LUKS, the TPM and the firmware's variables with directories and fakes. They stop an
  install or upgrade before each change it makes and check that the disk still boots, so they
  cover interruptions that a VM test could reach only one at a time.
- The `vxlan-rule` check runs `chalkd kubernetes vxlan-rule` as a worker's unit does, with the
  worker's firewall as NixOS renders it, in a network namespace, with a peer and a pod in
  namespaces of their own, and sends UDP to check what the VXLAN rule accepts. The unit tests of
  `pkg/kubernetes/vxlan` cover what the rule makes of its files.
- The `router-firewall` check runs a router image's `chalkos-firewall.service` with the rulesets
  that chalkos-render's golden test keeps, in a network namespace with a LAN peer and a WAN peer
  in namespaces of their own, and the VRF example's ruleset, also with the strict reverse-path
  filter, with peers on links in VRFs. It connects, pings and spoofs between them to check what
  the ruleset accepts, forwards and translates, that zones and conntrack zones of VRFs keep apart
  what they should, and that a reload replaces the table alone.
- The `router-policy-routing` check loads the policy example's ruleset with the kernel's rules
  and next hops' tables that networkd would install from the example's units, in network
  namespaces with LAN peers, two uplinks and a peer in a VRF. It probes which peer answers each
  source, port and DSCP, and that a next hop's traffic is dropped while its link is down.
- The `router-nat64` check runs a router image's `chalkos-nat64.service` over the NAT64s of the
  golden test, one in the default VRF with an explicit pool and one in a VRF with an empty pool.
  Jool's module cannot load there, so a stub of `jool` keeps the instances per network namespace.
  The check proves each instance's namespace with forwarding off, its link in its VRF, the
  addresses and routes, the removal of an older image's instance, routes and namespaces that no
  file names, the 30-second wait for a VRF's table, once for two NAT64s of one VRF, a second run
  that changes nothing, and `stop`. A run fails without changing anything when a link holds an
  address of an explicit pool, and leaves a device of another kind under a link's name alone. With the VRF example's ruleset, it sends a guest's packet into the NAT64's namespace and
  a connection out of it, masqueraded to the uplink's address.
- The `router-dhcp` check runs a router image's `kea-dhcp4.service` over the DHCP example's
  server of the default VRF, behind the example's ruleset, with busybox's `udhcpc` as a client in
  a peer namespace. It proves a lease of the pool with the subnet's options, a reservation by MAC
  address, the boot files of BIOS, UEFI and iPXE clients, the diagnostic `dhcp-leases` by subnet
  and MAC address, the lease file, and leases kept across a reload. Kea's raw sockets need no port
  of the ruleset.
- The `router-dhcp6` check runs a router image's `kea-dhcp6` over the DHCPv6 example's servers of
  the default VRF and of a VRF, the latter bound to its VRF's device, with their units'
  capabilities, behind the example's ruleset, with dhcpcd as clients in peer namespaces. It proves
  a reservation by MAC address with the subnet's options, a delegated prefix that
  `chalkos-dhcp6-pd` records and `chalkd router dhcp6-routes` routes in the VRF's table, the
  route's removal at a release and records that route nothing under the unit's capabilities:
  ended, foreign, links, FIFOs, records too long and a linked directory. It also proves the
  diagnostic `dhcp-leases` of both leases and that a ruleset without DHCPv6's opening keeps the
  client from its address.
- The `router-dhcp-relay` check runs a router image's `dnsmasq-relay.service` and
  `dnsmasq-relay@guest.service` over the relay example's configs, with their units'
  capabilities, behind its ruleset, between clients and Kea's servers in peer namespaces. It
  proves that the relays of the default VRF and of a VRF run beside each other in either order, a
  lease of each family through the relays, the servers' answers let in as replies and that a
  ruleset without the relay's openings keeps the client from both. dnsmasq runs as the
  namespace's root, its only user, with the units' capabilities alone, so the check does not run
  it as the user `dnsmasq`; `router-dhcp6` runs Kea and `chalkd router dhcp6-routes` the same way.

Prefer this level for anything about partitions, firewall rules or interfaces.

## Evaluation tests and build checks

`test/nix/eval.nix` holds evaluation tests in the form of `lib.runTests`: each `test*`
attribute has an `expr` and an `expected` value. They evaluate images with `mkImages` and check
its arguments, the images' options and assertions, and what the images set; a test that an
evaluation fails uses `builtins.tryEval`. The `eval` check fails with the list of failures, and
`eval-kinds` does the same for `test/nix/kinds.nix`, the tests of config kinds. Both take
`fails`, `worker`, `router` and `routerWithout` from `test/nix/lib.nix`.

```nix
testVariantSetsDiskOptions = {
  expr = (worker [ { chalkos.disk.espSize = "256M"; } ]).chalkos.disk.espSize;
  expected = "256M";
};
```

Build checks test what an image build produces, without booting it. They live in `nix/checks/`:
`image.nix` checks an image's files, `units/` runs an image's unit scripts in namespaces, one file
a check, and `repo.nix` validates the lab template and the examples. `units/` also holds
`image-checker`, which boots one NixOS VM, as an image's checker needs systemd. `e2e.nix` holds
the end-to-end checks, `eval.nix` the evaluation tests, and `lib.nix` what they share, with the
shell functions `fail`, `retry` and `refuses` the check scripts call. The test scripts of
`units/` are `writeShellApplication` scripts, so shellcheck runs on them when they build.

| Check | What it checks |
| --- | --- |
| `kernel-modules` | An image's module tree holds its groups' modules and no others, and the tool refuses unknown modules and missing dependencies. |
| `image-size` | The test images stay within the ceilings in `nix/testing/image-sizes.nix`, and the fit check every image build runs refuses images that leave no room. Its measure of each image agrees with `pkg/verity`. |
| `bootspec` | An image's bootspec names the UKI on the ESP and no initrd. |
| `vxlan-rule` | The VXLAN firewall rule, in network namespaces. |
| `router-firewall` | A router's firewall unit and rulesets, in network namespaces. |
| `router-policy-routing` | A router's policy routing: the example's ruleset with the kernel's rules and next-hop tables, in network namespaces. |
| `router-nat64` | A router's `chalkos-nat64.service`, with a stub of `jool`, in network namespaces. |
| `router-dhcp` | A router's `kea-dhcp4.service` and the diagnostic `dhcp-leases`, in network namespaces. |
| `router-dhcp6` | A router's `kea-dhcp6` servers, the records and routes of delegated prefixes and the firewall's DHCPv6 opening, in network namespaces. |
| `router-dhcp-relay` | A router's `dnsmasq-relay.service` and `dnsmasq-relay@<vrf>.service` and the firewall's openings of the relays, in network namespaces. |
| `frr-credentials` | A router's `frr.service` fills its config's passwords in from its credentials, and refuses those FRR would not take. |
| `image-checker` | An image's `chalkd check` in its sandbox on a NixOS VM, with a second store: the image's renderer and command fact run from it, a renderer's refusal comes back, the sandbox hides `/run/chalkos`, and another user reaches neither the facts socket nor the result. |
| `rules` | The images' rules compile, the outputs' checks pass the files they should and refuse broken ones, and a broken rule, effect, output or diagnostic fails its image. |
| `image-tests` | The image tests of the test image, the router images and the examples' image pass: their outputs render the golden files, and their refusals and effects are those expected. |
| `template-lab` | The lab template's image builds, and `chalkctl validate` accepts the template's YAML with it. |
| `example-homelab` | The homelab example's image builds, and `chalkctl validate` accepts its YAML, `Peer` documents included, with it. |
| `example-extensions` | The extensions example's image builds, and `chalkctl validate` accepts its YAML, `Rack` and `Hardware` documents included, with it. |

## Image tests

An image test, a test of `chalkos.tests`, runs a config file through the checks chalkd runs for a
delivery, at build time and without a node: the kinds' schemas, the change rules, the validators,
every output's renderer and check, and the plan of effects. It compares the files an output
renders with golden files, the refusals with those it expects, and for a change from `before`, the
effects taken. [Test an extension](../build/test-an-extension.md) describes the fields and the
results. `cmd/chalkos-rules test` runs the tests, and `modules/node/runtime/tests.nix` declares
them and builds `chalkosTestResults` and `chalkosTests`.

chalkos's own modules carry image tests:

- `modules/node/kinds/networkd.nix` and `modules/node/time.nix` turn each file of
  `modules/node/examples/networkd` and `modules/node/examples/chrony` into a test that the
  image accepts it, renders every output and passes their checks, through
  `chalkosLib.internal.exampleTests`.
- The router layer turns each file `<name>.yaml` of `modules/node/examples/router` into the test
  `router-<name>`, which runs as the node `r1`. On an image with every router feature it compares
  each router output with the golden files of `modules/node/router/tests/<name>/<output>`. A
  missing directory stands for an output that renders no files, as git keeps no empty directory.
  `router-vrf-strict` renders the VRF example under the strict reverse-path filter.
- The test image's `Greeting` output has a test per file of `modules/testing/greeting-examples`.
  The examples' image carries the tests of the homelab's BGP extension and of the extensions
  example, with golden files below `examples/homelab/extensions/tests` and
  `examples/extensions/tests`.

The `image-tests` check builds `chalkosTests` of the test image, the router image, each router
image without a feature and the examples' image. A golden file that differs fails it with a diff
and a command that takes what was rendered:

```sh
nix build -L .#checks.x86_64-linux.image-tests
```

To take new golden files after a change of a renderer, or for a new router example, read each
diff, then run the command the log prints after it, with `DIR` the golden directory, such as
`modules/node/router/tests/lan/router-nftables`. A new example's outputs have no golden
directories yet, so each output that renders files fails with a diff against no files. Create
`modules/node/router/tests/<name>` first, and run each command without `rm -r DIR &&`, as there
is no directory to remove. The `rules` check and
the router's namespace checks, such as `router-firewall`, read the files the router image's tests
rendered, so they fail too when an example renders nothing.

## End-to-end tests

The end-to-end tests in `test/e2e` boot chalkos images in QEMU with OVMF, Secure Boot and a software
TPM, through the same `pkg/lab` code chalklab uses, and drive
the nodes with the real chalkctl. Most tests read the probe's facts on the serial console. A test
of a VM without a serial port reads its screen as text instead: `pkg/lab` takes QEMU's screendump
and matches each character cell against the console font that the test image loads and that
`CHALKLAB_CONSOLE_FONT` names, and it types on the VM's keyboard through QMP. Each check runs one
test or a group of them:

| Check | Test | VMs |
| --- | --- | --- |
| `e2e-firmware` | The firmware with a TPM starts and reaches the serial console. | 1 |
| `e2e-image` | The test image boots without Secure Boot, then without its serial port and TPM: STATE asks for the recovery key on the screen, which the test reads with the test image's console font and answers on the keyboard, and the console answers a password request after the boot. | 1 |
| `e2e-secureboot` | A signed image boots under Secure Boot and an unsigned one is refused. | 1 per test |
| `e2e-verity` | A tampered store fails to read. | 1 |
| `e2e-install` | An install in place, with a second disk. | 1 |
| `e2e-installer` | The installer installs a node under Secure Boot onto a disk that holds an LVM volume group: an install streamed from a directory that wipes the disk and is cancelled while it writes slot A, one whose config the image's validator refuses once the store is on the disk, and one that continues on the disk, which the installer pulls from a registry the test serves, with credentials. | 1 |
| `e2e-iso` | The installer's ISO, signed as chalkbuild signs it, boots. | 1 |
| `e2e-storage` | Volumes on the system disk and a data disk. | 1 |
| `e2e-upgrade` | Under Secure Boot, an upgrade that the node pulls through a mirror endpoint, a registry the test serves, with the endpoint's credentials, that boots from slot B and is found healthy, then one streamed from a directory that never becomes healthy and falls back. | 1 |
| `e2e-kubernetes` | A dual-stack cluster of a control plane and a worker through chalklab: networking, DNS, a private image pulled through a mirror with the node's credentials, renewals and rotations, a reboot. | 2 |
| `e2e-kubernetes-ha` | Three control planes of an IPv6-only cluster behind a VIP: joins, failover, reinstalls and a rolling upgrade. | 3 |
| `e2e-router` | Two routers through chalklab, peering unnumbered over BGP with BFD and a password: the session in their status, a default route and the NAT64 prefix learned, a diagnostic, a delivery FRR reloads without ending the session, the Kubernetes API unimplemented, DNS64's answer for `ipv4only.arpa`, and a connection through a NAT64 with an empty pool, in its own namespace, and QEMU's user-mode network to a listener of the test, with its session in the status and in `nat64-sessions`, a rotation of the node CA that gives both routers certificates of the new one, and both routers' session in a VRF with its table, a route imported into the default VRF, the unreachable defaults that end its table, a resolver of the VRF running before and after a change of the VRF's table, chalkd still reachable, and a policy's rules, next-hop table and chain on a router before and after its boot, and a DHCP server in a VRF that leases the other router its reserved address on a macvlan link, with the lease kept and renewed across the server's boot, and a DHCPv6 server there that leases it an address and a prefix, which the server's router routes to it before and after its boot. | 2 |

A VM gets 2 CPUs and 2 GiB of memory unless its test asks for less, on a 16 GiB sparse disk.
The Kubernetes test gives its control plane 2048 MiB and its worker 1024 MiB; the HA test gives
each control plane 1536 MiB, 4.5 GiB for the three. The router test gives each router 768 MiB.

The Kubernetes checks pull their container images from a registry the test serves from the Nix
store. The `e2e-kubernetes-online` app runs the Kubernetes test with images pulled from the
upstream registries instead, so it needs network access and runs on the host rather than as a
check:

```sh
nix run .#e2e-kubernetes-online
```

### Keep the VM tests few and small

Every VM test adds minutes to the suite and memory to the machine that runs it, and the whole
suite runs on a developer's machine, not only in CI. Hold to these rules:

- Prove behaviour with unit, namespace, disk-image and evaluation tests first.
- Add VM coverage only for what needs a booted node: firmware, Secure Boot, the TPM, the
  initrd, systemd units, or several nodes together.
- Add assertions to an existing end-to-end test rather than a new check. The Kubernetes and HA
  tests already bring a cluster up; a new cluster feature is checked there.
- Keep a VM's memory at what the test needs, and state the memory, CPUs and running time of a
  new or changed VM test in its change.

## Flake checks

Every check builds with `nix build`, and a change runs the checks of the parts it touches:

| Part changed | Checks |
| --- | --- |
| Go code in `pkg/` or `cmd/` | `go-unit`, `go-lint`, and the end-to-end checks covering the behaviour |
| End-to-end tests in `test/e2e` | `go-lint`, and the end-to-end checks of the changed tests |
| `api/chalkos/*/v1/*.proto` | `api-generated`, `docs-generated`, `go-unit`, `go-lint` |
| Command help, option descriptions | `docs-generated`, `docs` |
| Pages in `docs/` | `docs` |
| Modules in `modules/` or `lib/` | `eval`, `eval-kinds`, `image-tests`, and the build and end-to-end checks of what the module does |
| `examples/homelab` | `example-homelab`, `example-homelab-validator-docs`, `image-tests` |
| `examples/extensions` | `example-extensions`, `image-tests` |
| Kernel modules, closure, image size | `kernel-modules`, `image-size`, `bootspec` |
| The firewall or VXLAN rule | `vxlan-rule`, `go-unit`, `e2e-kubernetes` |
| The router's kinds, outputs or firewall | `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit` |
| The router's BGP: its kinds, `frr` output, FRR's unit, `pkg/router` | `rules`, `image-tests`, `frr-credentials`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The router's NAT64 and resolvers: their kinds, the `jool` and `unbound` outputs, their units, `pkg/router` | `router-nat64`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `image-size`, `e2e-router` |
| The router's VRFs: the `VRF` kind, `router-networkd`, `chalkos-vrf` | `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `router-vrf`, `router-firewall`, `e2e-router` |
| The router's policy routing: the `PolicyRoute` and `NextHop` kinds, their chain and units | `router-policy-routing`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The router's DHCP and relays: the `DHCPSubnet` and `DHCPRelay` kinds, the `kea` and `dhcp-relay` outputs, their units, `pkg/router` | `router-dhcp`, `router-dhcp6`, `router-dhcp-relay`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| An image's checker: `pkg/imagecheck`, `chalkd check` | `image-checker`, `go-unit`, `go-lint`, `e2e-upgrade` |
| `templates/lab` | `template-lab` |
| `.github/workflows` | `workflows` |

Run one check, with its log:

```sh
nix build -L .#checks.x86_64-linux.e2e-upgrade
```

Run the whole suite, which needs `/dev/kvm` and a Nix builder with the `kvm` system feature for
the end-to-end checks:

```sh
nix flake check
```

## When an end-to-end test fails

A failed test keeps its VM directory, with the console log, the firmware variables, the TPM's
state and the disk overlay, and logs the directory's path and the last 200 lines of the
console. In the Nix sandbox the directory is gone once the build ends, so the console lines in
the build log are what is left; `nix log` prints that log again afterwards. To keep the
directory, run the test binary on the host the way the `e2e-kubernetes-online` app does:
`nix build .#chalklab-e2e` builds it, and it reads the same variables the check sets.
