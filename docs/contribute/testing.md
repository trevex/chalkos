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
| Evaluation test | `test/nix/eval.nix`, `test/nix/kinds.nix` | the arguments of `mkImage` and `mkImages`, option defaults, assertions, what a module sets in an image, config kinds | one evaluation |
| Image test | `chalkos.tests` of a module, with golden files such as those in `modules/node/router/tests` | the files an image renders from a config, the refusals it gives and the effects a change takes | one run of the image's renderers and validators |
| Build check | `nix/checks/` | what an image build produces: kernel modules, sizes, the bootspec | one image build |
| End-to-end test | `test/e2e/` | behaviour that needs a booted node: firmware, Secure Boot, the TPM, the initrd, a cluster | minutes and 2 GiB or more per VM |

## Run a subset

Run one package's Go tests, or one test of it, in the development shell. Build one flake check by
its name, with its log:

```sh
go test ./pkg/<pkg>/ -run TestX
nix build -L .#checks.x86_64-linux.<name>
```

`<pkg>` is a package below `pkg/`, `TestX` a test function's name and `<name>` a check of the
tables below. `go test ./...` from the repository's root also runs the end-to-end tests in
`test/e2e`, which boot VMs once the variables they need are set. The development shell sets those
of the firmware test.

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

### Compare output with a golden file

A test whose output is long, such as a rendered status or a list of Kubernetes objects, compares
it with a golden file through `pkg/internal/golden`. `golden.Check` compares the output with the
file and fails with the output when they differ. `golden.JSON` turns a value into indented JSON
first. Golden files live in the package's `testdata/`, such as
`pkg/statusview/testdata/status.golden`. After a change of the output, rewrite them and review
the diff before you commit it:

```sh
go test ./pkg/statusview/ -run TestX -update
```

The router's renderers in `cmd/chalkos-render` keep their golden files in
`cmd/chalkos-render/testdata/router` and take `-update` the same way.

## Wait in tests

A test waits for an event: no test calls `time.Sleep` to let something happen.
`pkg/internal/testwait` gives the two ways to wait:

| Wait | Use it for | How it works |
| --- | --- | --- |
| `testwait.Bubble(t, f)` | Timers, channels and polling loops inside the test's own process. | Runs `f` in a `testing/synctest` bubble with a fake clock that starts at the real time, so certificates the test issues are valid. A sleep in the bubble costs no wall time, and `synctest.Wait` returns once every other goroutine of the bubble is blocked. |
| `testwait.Eventually(t, what, check)` | Real I/O that no fake clock reaches: servers, sockets and child processes. | Calls `check` until it holds, after 1 ms and then twice as long each time up to 100 ms. It fails the test, naming `what`, after a minute. |

Inside a bubble, `f` must not call `t.Run` or `t.Parallel`. A test with subtests starts a bubble in
each subtest's body instead.

Production code keeps no clock seams and no timing knobs for tests. Durations are constants, and a
test that needs time to pass runs in a bubble. Add a seam only when production needs it.

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
  addresses and routes, the removal of routes and namespaces that no file names, the 30-second
  wait for a VRF's table, once for two NAT64s of one VRF, a second run that changes nothing, and
  `stop`. A run fails without changing anything when a link holds an address of an explicit pool,
  and leaves a device of another kind under a link's name alone. With the VRF example's ruleset,
  it sends a guest's packet into the NAT64's namespace and a connection out of it, masqueraded
  to the uplink's address.
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
their arguments, the images' options and assertions, and what the images set; a test that an
evaluation fails uses `builtins.tryEval`. The `eval` check fails with the list of failures, and
`eval-kinds` does the same for `test/nix/kinds.nix`, the tests of config kinds, outputs,
effects, facts and validators. Both take their helpers from `test/nix/lib.nix`: `fails`,
`refuses`, `worker`, `router`, `routerWithout`, `installer`, `etcJSON` and `hasPackage`. Here
`w` is the plain worker image, which eval.nix evaluates once:

```nix
testStateHasItsOwnPartitionType = {
  expr = w.systemd.repart.partitions."50-state".Type;
  expected = "480b7842-1236-1abb-0339-e5503b43122a";
};
```

Build checks test what an image build produces, without booting it. They live in `nix/checks/`:
`image.nix` checks an image's files, `units/` runs an image's unit scripts in namespaces, one file
a check, and `repo.nix` validates the lab template and the examples. `units/` also holds
`image-checker`, which boots one NixOS VM, as an image's checker needs systemd. `e2e.nix` holds
the end-to-end checks, `eval.nix` the evaluation tests, and `lib.nix` what they share, with the
shell functions the check scripts call: `fail`, `retry` and `refuses`, and for the unit checks
`netns`, `veth`, `forwarding`, `document` and `stub`. The test scripts of
`units/` are `writeShellApplication` scripts, so shellcheck runs on them when they build.

| Check | What it checks |
| --- | --- |
| `kernel-modules` | An image's module tree holds its groups' modules and no others, and the tool refuses unknown modules and missing dependencies. |
| `image-size` | The test images stay within the ceilings in `nix/testing/image-sizes.nix`, and the fit check every image build runs refuses images that leave no room. Its measure of each image agrees with `pkg/verity`. |
| `bootspec` | An image's bootspec names the UKI on the ESP and no initrd. |
| `vxlan-rule` | The VXLAN firewall rule, in network namespaces. |
| `router-firewall` | A router's firewall unit and rulesets, in network namespaces. |
| `router-policy-routing` | A router's policy routing: the example's ruleset with the kernel's rules and next-hop tables, in network namespaces. |
| `router-vrf` | A router's `chalkos-vrf` in a network namespace, covering the VRFs it removes and keeps and the unreachable defaults it flushes. |
| `router-nat64` | A router's `chalkos-nat64.service`, with a stub of `jool`, in network namespaces. |
| `router-dhcp` | A router's `kea-dhcp4.service` and the diagnostic `dhcp-leases`, in network namespaces. |
| `router-dhcp6` | A router's `kea-dhcp6` servers, the records and routes of delegated prefixes and the firewall's DHCPv6 opening, in network namespaces. |
| `router-dhcp-relay` | A router's `dnsmasq-relay.service` and `dnsmasq-relay@<vrf>.service` and the firewall's openings of the relays, in network namespaces. |
| `frr-credentials` | A router's `frr.service` fills its config's passwords in from its credentials, and refuses those FRR would not take. |
| `frr-reload` | `chalkos-frr-reload` on a NixOS VM that runs FRR: it reloads FRR from one golden `frr.conf` of the renderers to another in one pass, after which FRR runs what it runs when it starts on the second, and plans nothing for a config FRR runs. |
| `image-checker` | An image's `chalkd check` in its sandbox on a NixOS VM, with a second store: the image's renderer and command fact run from it, a renderer's refusal comes back, the sandbox hides `/run/chalkos`, and another user reaches neither the facts socket nor the result. |
| `rules` | The images' rules compile, the outputs' checks pass the files they should and refuse broken ones, and a broken rule, effect, output or diagnostic fails its image. |
| `image-tests` | The image tests of the test image, the router images and the examples' image pass: their outputs render the golden files, and their refusals and effects are those expected. |
| `template-lab` | The lab template's image builds, and `chalkctl validate` accepts the template's YAML with it. |
| `template-router-lab` | The router lab template's two images build, and `chalkctl validate` accepts the template's YAML with them. |
| `template-cluster-metal` | The metal cluster template's image and installer build, and `chalkctl validate` accepts the template's YAML with the image. |
| `example-homelab` | The homelab images build, and `chalkctl validate` accepts their inventory with the built-in `BGPSpeaker` and `BGPPeer` documents. |
| `example-bgp-extension` | The standalone BIRD image evaluates and its extension tests pass. |
| `example-extensions` | The extensions example's image builds, and `chalkctl validate` accepts its YAML, `Rack` and `Hardware` documents included, with it. |
| `image-contract` | An image directory describes itself and carries a schema for each kind it lists. |
| `image-oci` | The test image's OCI layout: one manifest of the chalkos artifact type, whose header names the image directory's root hash and platform, whose layers are the image's parts and contract in order, and whose contract holds the directory's `metadata.json`. |
| `chrony-dhcp` | The `chalkos-chrony-dhcp` unit's script: the time servers networkd learned are chrony's when the node's `TimeSync` takes them, and none are otherwise. chrony's effect reloads the sources of a chronyd that runs alone. |
| `containerd-config` | containerd's merged config takes the sandbox image from the file `chalkos-containerd-config` writes from the `Cluster` document, and keeps its own without one. |
| `kubernetes-bgp-nht` | A numbered peer over a `noprefixroute` fabric address and an RA subnet route: the old next-hop check fails, the rendered config establishes BGP, learns a default route and announces the node's address, and link loss removes the default, in network namespaces. |
| `controlplane-ports` | A peer reaches 6443, 2379 and 2380 over IPv4 and IPv6 through the firewall once the node's `Node` document makes it a control plane, and no longer once it makes it a worker, in a network namespace. |
| `registry-mirrors` | The `chalkos-registry-mirrors` unit writes the registry hosts containerd reads from `Mirror` and `Registry` documents, keeps them when a document cannot be read, and writes none without documents. |
| `fixtures` | Every node of the end-to-end fixtures has config its image accepts, as `chalkctl validate` checks it, and the inventories the tests want refused are refused with their message. |
| `nix-lint` | The Nix files are formatted as `nix fmt` formats them, hold no unused binding or argument (deadnix) and pass statix, except for the lints `statix.toml` leaves out. |
| `ci-tiers` | Every check is in exactly one tier of `nix/checks/tiers.nix`, and `.github/labels.yml` has a label for each end-to-end check and the label `ci-failure`. See [Continuous integration](ci.md#tiers). |

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
- The Kubernetes layer's BGP speaker carries the tests `kubernetes-bgp-<name>` of
  `modules/node/kubernetes/bgp/tests.nix`: the `frr.conf` each config of its `tests` directory
  renders, the refusals of its kinds' rules, and the reload that a new password takes.
- The router layer turns each file `<name>.yaml` of `modules/node/examples/router` into the test
  `router-<name>`, which runs as the node `r1`. On an image with every router feature it compares
  each router output with the golden files of `modules/node/router/tests/<name>/<output>`. A
  missing directory stands for an output that renders no files, as git keeps no empty directory.
  `router-vrf-strict` renders the VRF example under the strict reverse-path filter.
- The test image's `Greeting` output has a test per file of `modules/testing/greeting-examples`.
  The examples' image carries the BGP extension and the Rack and Hardware extension tests,
  with golden files below `examples/bgp-extension/extensions/tests` and
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
| `e2e-image` | The test image boots without Secure Boot, then without its serial port and TPM: STATE asks for the recovery key on the screen, which the test reads with the test image's console font and answers on the keyboard, and the console answers a password request after the boot. A second test installs volumes on the system disk and a data disk. | 1 per test |
| `e2e-secureboot` | Secure Boot refuses an unsigned image, and dm-verity a tampered store. | 1 per test |
| `e2e-install` | An install in place, with a second disk. | 1 |
| `e2e-installer` | The installer installs a node under Secure Boot onto a disk that holds an LVM volume group: an install streamed from a directory that wipes the disk and is cancelled while it writes slot A, one whose config the image's validator refuses once the store is on the disk, and one that continues on the disk, which the installer pulls from a registry the test serves, with credentials. | 1 |
| `e2e-iso` | The installer's ISO, signed as chalkbuild signs it, boots. | 1 |
| `e2e-upgrade` | Under Secure Boot, an upgrade that the node pulls through a mirror endpoint, a registry the test serves, with the endpoint's credentials, that boots from slot B and is found healthy, then one streamed from a directory that never becomes healthy and falls back. | 1 |
| `e2e-kubernetes` | A dual-stack cluster of a control plane and a worker through chalklab: networking, DNS, a private image pulled through a mirror with the node's credentials, an image pushed to the control plane's cluster registry and run on the worker, renewals and rotations, a reboot. | 2 |
| `e2e-kubernetes-ha` | Three control planes of an IPv6-only cluster behind a VIP: joins, failover, reinstalls and a rolling upgrade. | 3 |
| `e2e-router` | Two routers through chalklab, peering unnumbered over BGP with BFD and a password: the session in their status, a default route and the NAT64 prefix learned, a diagnostic, a delivery FRR reloads without ending the session, DNS64's answer for `ipv4only.arpa`, and a connection through a NAT64 with an empty pool, in its own namespace, and QEMU's user-mode network to a listener of the test, with its session in the status and in `nat64-sessions`, both routers' session in a VRF with its table, a route imported into the default VRF, the unreachable defaults that end its table, a resolver of the VRF running before and after a change of the VRF's table, chalkd still reachable, and a policy's rules, next-hop table and chain on a router before and after its boot, and a DHCP server in a VRF that leases the other router its reserved address on a macvlan link, with the lease kept and renewed across the server's boot, and a DHCPv6 server there that delegates it a prefix, which the server's router routes to it before and after its boot, and a Kubernetes control plane on the routers' switch whose BGP speaker announces its address and the VIP to one router over an address session with BFD and a password: the router's route to the VIP and a connection to the API server through it, the router's default route in the node's BGP table but not in its kernel, which keeps the default route of its router advertisements, and a delivery that adds an interface to announce, which `chalkos-frr-reload` applies in one pass without ending the session. | 3 |

Each test lives in a file of its own in `test/e2e`; the other files hold the steps the tests share:

| File | Holds | Run by |
| --- | --- | --- |
| `image_test.go`, `storage_test.go` | `TestImageBootsWithoutSecureBoot`, `TestStorageVolumes` | `e2e-image` |
| `secureboot_test.go` | `TestSecureBootRejectsUnsignedImage`, `TestVerityRejectsTamperedStore` | `e2e-secureboot` |
| `install_test.go` | `TestInstallInPlace` | `e2e-install` |
| `installer_test.go` | `TestInstallerInstallsOntoLVMDisk`, `TestInstallerISOBoots` | `e2e-installer`, `e2e-iso` |
| `upgrade_test.go` | `TestUpgrade` | `e2e-upgrade` |
| `kubernetes_test.go` | `TestKubernetesCluster` | `e2e-kubernetes` |
| `cluster_registry_test.go` | the cluster registry's phase of `TestKubernetesCluster`: a push, a list, a pod that pulls, a delete and a garbage collection | `e2e-kubernetes` |
| `kubernetes_ha_test.go` | `TestKubernetesHA` | `e2e-kubernetes-ha` |
| `router_test.go` | `TestRouterLab` | `e2e-router` |
| `helpers_test.go`, `chalkd_test.go` | `TestMain`, starting a node's VM, reaching its chalkd and running chalkctl | every check |
| `lab_test.go` | labs that chalklab runs | `e2e-kubernetes`, `e2e-router` |
| `registry_test.go` | registries the tests serve, of container images and of chalkos images | `e2e-installer`, `e2e-upgrade`, `e2e-kubernetes`, `e2e-kubernetes-ha` |
| `ntp_test.go` | an NTP server for the nodes' clocks | `e2e-kubernetes`, `e2e-router` |
| `certificates_test.go`, `rotation_test.go` | certificate renewals and CA rotations, and chalkctl run with a client file | `e2e-kubernetes`, `e2e-upgrade` |

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
| Pages in `docs/` | `docs`, and `example-bgp-extension-validator-docs` for `guides/bgp-extension.md` |
| Modules in `modules/` or `lib/` | `eval`, `eval-kinds`, `image-tests`, and the build and end-to-end checks of what the module does |
| `examples/homelab` | `example-homelab` |
| `examples/bgp-extension` | `example-bgp-extension`, `example-bgp-extension-validator-docs`, `image-tests` |
| `examples/extensions` | `example-extensions`, `image-tests` |
| Kernel modules, closure, image size | `kernel-modules`, `image-size`, `bootspec` |
| The firewall or VXLAN rule | `vxlan-rule`, `go-unit`, `e2e-kubernetes` |
| The router's kinds, outputs or firewall | `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit` |
| The router's BGP: its kinds, `frr` output, FRR's unit, `pkg/router` | `rules`, `image-tests`, `frr-credentials`, `frr-reload`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The Kubernetes layer's BGP: its kinds, `bgp` output and renderer, `modules/node/kubernetes/bgp`, `modules/node/frr` | `rules`, `image-tests`, `frr-credentials`, `frr-reload`, `eval`, `eval-kinds`, `go-unit`, `image-size`, `example-homelab`, `e2e-router`, `e2e-kubernetes` |
| FRR's package or reload: `nix/packages/frr.nix`, `pkg/frr`, `cmd/chalkos-frr-reload` | `frr-reload`, `go-unit`, `image-size`, `e2e-router` |
| The router's NAT64 and resolvers: their kinds, the `jool` and `unbound` outputs, their units, `pkg/router` | `router-nat64`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `image-size`, `e2e-router` |
| The router's VRFs: the `VRF` kind, `router-networkd`, `chalkos-vrf` | `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `router-vrf`, `router-firewall`, `e2e-router` |
| The router's policy routing: the `PolicyRoute` and `NextHop` kinds, their chain and units | `router-policy-routing`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The router's DHCP and relays: the `DHCPSubnet` and `DHCPRelay` kinds, the `kea` and `dhcp-relay` outputs, their units, `pkg/router` | `router-dhcp`, `router-dhcp6`, `router-dhcp-relay`, `router-firewall`, `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| The router's time server: the `TimeServer` kind, the `chrony-server` output, `modules/node/router/ntp` | `rules`, `image-tests`, `eval`, `eval-kinds`, `go-unit`, `e2e-router` |
| An image's checker: `pkg/imagecheck`, `chalkd check` | `image-checker`, `go-unit`, `go-lint`, `e2e-upgrade` |
| containerd or registries: `modules/node/kubernetes/containerd.nix`, `registries.nix`, `registry-mirrors.nix` | `containerd-config`, `registry-mirrors`, `eval`, `image-tests`, `e2e-kubernetes` |
| The node's clock: `modules/node/time.nix`, the `TimeSync` kind | `chrony-dhcp`, `eval`, `eval-kinds`, `image-tests` |
| OCI images: `pkg/ociimage`, `pkg/chalkbuild` | `go-unit`, `go-lint`, `image-oci`, `e2e-installer`, `e2e-upgrade` |
| The console: `pkg/console`, `cmd/chalkos-console` | `go-unit`, `go-lint`, `e2e-image` |
| Status: `pkg/statusview` and its `layers/<layer>` | `go-unit`, `go-lint`, and its golden files in `testdata/` |
| A new or removed directory of Go code in `cmd/` or `pkg/` | `docs`, which wants a row in `docs/contribute/packages.md` for each directory of Go code and none for another |
| `templates/lab` | `template-lab` |
| `templates/router-lab` | `template-router-lab` |
| `templates/cluster-metal` | `template-cluster-metal` |
| `.github/workflows` | `workflows` |
| `nix/checks/tiers.nix`, `.github/labels.yml`, a new check | `ci-tiers` |

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
