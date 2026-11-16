---
title: "Common changes"
description: "Checklists for adding an API method, a chalkctl command, a built-in kind, a router feature or a layer"
---

# Common changes

This page lists the steps of the changes contributors make most often. Each checklist names every
file to touch and the checks that prove the change. Run the commands in the
[development shell](development.md#enter-the-development-shell).
[Code conventions](conventions.md) gives the rules each change follows, and
[Architecture for developers](architecture.md#where-to-change-what) maps other changes to their
files.

## Add a node API method

The node API is `NodeService` in `api/chalkos/node/v1/node.proto`. A layer's API is its own
service, such as `api/chalkos/router/v1/router.proto`; the steps differ where noted.

1. Declare the RPC and its request and response messages in the proto file. Its comment says
   what it does, and ends with the modes and least role it is available in, such as
   `Available in normal mode to operators.` A layer's RPC adds the layer:
   `Available in normal mode to admins, on images with the Router layer.` When the console may
   call it, add `The console may call it.`
2. Generate the Go code and the API reference page:

    ```sh
    buf generate
    ```

    This writes `pkg/api/<api>/v1/` and `docs/reference/api.md`.

3. Implement the handler. A node API method is a method of `*Server` in `pkg/chalkd`. A layer's
   method belongs to its chalkd plugin in `pkg/chalkd/layers/<layer>`.
4. Give the procedure its permission: the modes, the least role and the console's access. The
   node API's are in `permissions` in `pkg/chalkd/server.go`, and a layer's come from its
   plugin's `Permissions`. chalkd refuses a procedure without an entry.
5. Run the permission test of the API: `TestNodeAPIPermissions` in `pkg/chalkd/server_test.go`,
   or the layer's own, such as `TestRouterAPIPermissions`. Both call `layertest.CheckAPI`, which
   fails unless each RPC's comment and its permission agree.
6. Call the method from chalkctl. `client.Conn` embeds the generated `NodeServiceClient`, so a
   command calls `conn.<Method>`. A layer's chalkctl plugin creates its service's client on the
   connection, as `pkg/chalkctl/layers/router/routes.go` does.
7. Add the command that uses it, as the next checklist describes, and the tests of the handler.
8. Build the checks `api-generated`, `docs-generated`, `go-unit` and `go-lint`.

## Add a chalkctl command

1. Write the command in `pkg/chalkctl`, built with `a.command`, or for a layer's command in
   `pkg/chalkctl/layers/<layer>`. Give it `Use`, `Short`, `Long` and `Example`. A command that reaches nodes registers its flags through `layer.NodeFlags`:
   `Register` for the secrets file alone, `RegisterClient` when a client file may run it.
2. Put its long description and examples in `help.go` of the same package, beside the other
   commands' help. `TestCommandsDescribed` fails for a visible command without a short or long
   description or an example.
3. Add it to the tree: in `newCommand` in `pkg/chalkctl/chalkctl.go`, or in the layer plugin's
   `Commands`, by the group it joins.
4. A command that takes `--config` goes into the list of `client new`'s help in
   `pkg/chalkctl/help.go`. `TestClientNewNamesClientCommands` compares the two.
5. Test it in-process with `newTestApp`, and with a chalkd that the test app's `startNode` serves
   when the command reaches a node, as the tests beside it do.
6. Regenerate the command reference and the navigation:

    ```sh
    nix run .#docgen
    ```

7. Name the command on the hand-written pages it affects, such as the
   [command cheat sheet](../reference/cheat-sheet.md).
8. Build the checks `go-unit`, `go-lint`, `docs-generated` and `docs`.

A chalklab or chalkbuild command follows the same steps in `pkg/chalklab` or `pkg/chalkbuild`.

## Add a built-in kind

A built-in kind is one every image accepts, such as `TimeSync`. A layer's kinds live in the
layer's modules instead.

1. Declare the kind in a file of `modules/node/kinds/builtin/`, as
   `builtin groups.<group> // { description = ...; spec = { ... }; }`, the way `time.nix` declares
   `TimeSync`. A new file goes into the imports of `modules/node/kinds/builtin/default.nix`, and a
   new API group into `groups` in `lib/kinds/api.nix`.
2. Use the kind in the module of its area: an output that renders from it, an effect, or a unit
   that reads it. `modules/node/time.nix` renders chrony's sources from `TimeSync`.
3. When Go code reads the kind, translate it in `pkg/config`. Add its schema to
   `test/fixtures/kinds/builtin` and the kind to `testBuiltinSchemasMatchFixtures` in
   `test/nix/kinds.nix`, which holds the image's schema and the fixture equal.
4. Add evaluation tests to `test/nix/kinds.nix` for its defaults, rules and refusals.
5. Add image tests. Example files in `modules/node/examples/<area>/` become tests through
   `chalkos.tests = chalkosLib.internal.exampleTests "<area>" ./examples/<area>;`. A test of
   `chalkos.tests.<name>` with `outputs` compares the rendered files with golden directories.
6. Regenerate the kinds' reference and the built-ins page with `nix run .#docgen`.
7. Build the checks `eval-kinds`, `eval`, `image-tests`, `rules` and `docs-generated`, and
   `go-unit` when Go code reads the kind.

## Add a router feature

A router feature is a part of the router layer that an image can leave out, such as DHCP or
WireGuard. Its directory is in dash case, such as `dhcp-relay/`, and its name in camel case, such
as `dhcpRelay`.

1. Create `modules/node/router/<feature>/default.nix` and `kinds.nix`. `kinds.nix` is a
   function of `{ chalkosLib, lib, routerLib }` that returns the feature's kinds, and
   `default.nix` loads it with `routerLib.kinds ./kinds.nix`.
2. Declare the feature's option in `default.nix`, and put everything else under it:

    ```nix
    options.chalkos.router = routerLib.feature "<feature>" {
      carries = "<what the images carry>";
      kinds = lib.attrNames kinds;
    };
    config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
      chalkos.kinds = kinds;
    };
    ```

3. Add the name to `features` in `modules/node/router/lib.nix`. `routerLib.feature` refuses a
   name that is missing there. A new output also goes into `outputs` there, so the examples'
   tests compare its files.
4. Import `./<feature>` in `modules/node/router/default.nix`.
5. Pin the IDs of the feature's users and groups, as `dhcp/default.nix` pins `kea`'s. An unpinned
   ID moves the IDs after it whenever an image adds or drops a feature.
6. Add the feature to `testRouterFeaturesOptional` in `test/nix/eval.nix`: its kinds, outputs,
   effects, services, users and the examples that use it. The test checks that leaving the
   feature out removes exactly these.
7. Add an example, `modules/node/examples/router/<name>.yaml`, which becomes the image test
   `router-<name>`. Create `modules/node/router/tests/<name>`, build the `image-tests` check, read
   each diff and run the command its log prints without `rm -r DIR &&`.
   [Image tests](testing.md#image-tests) explains the golden directories.
8. When the feature runs a unit, add a check in a network namespace as
   `nix/checks/units/router-<feature>.nix`. Each file of that directory is the check of its name.
9. Put a renderer written in Go in `cmd/chalkos-render`, and the derivations it shares with
   chalkd in `pkg/router/derive`.
10. Add a page to `docs/router/`, with its card on `docs/router/index.md` and its entry in the
    navigation of `zensical.toml`, and regenerate the reference with `nix run .#docgen`.
11. Build the checks `eval`, `eval-kinds`, `rules`, `image-tests`, `docs-generated`, `docs`, the
    feature's namespace check and `e2e-router` when the feature needs a booted router.

## Add a layer

A layer is a set of NixOS modules and a Go plugin in each program that serves it.
[Layer plugins](internals/layer-plugins.md) describes the hooks of each plugin.

1. Name the layer in `pkg/layers/layers.go`, beside `Kubernetes` and `Router`.
2. Write its modules in `modules/node/<layer>/`, imported by `modules/node/default.nix`. Its
   option `chalkos.<layer>.enable` is false by default. Under it, the module sets
   `chalkos.layers.<layer> = "";`, which writes the marker `/etc/chalkos/layers/<layer>`.
3. Export the layer in `lib/layers.nix`, which the flake gives as `nixosModules.<layer>`. Its
   entry sets `chalkos.<layer>.enable = true`.
4. Declare its kinds in its modules, with a new API group in `lib/kinds/api.nix`. Give each
   feature its own `chalkos.<layer>.<feature>.enable`, as
   [Code conventions](conventions.md#features-can-be-left-out) says.
5. Write its API in `api/chalkos/<layer>/v1/<layer>.proto`, and follow
   [Add a node API method](#add-a-node-api-method) for each method.
6. Write chalkd's plugin in `pkg/chalkd/layers/<layer>`: a `layer.Plugin` of
   `pkg/chalkd/layer` with its API's handler, permissions and loops, and the hooks it needs. List
   it in `plugins` in `cmd/chalkd/plugins.go`.
7. Write chalkctl's plugin in `pkg/chalkctl/layers/<layer>`, with its commands' help in its own
   `help.go`. List it in `plugins` in `pkg/chalkctl/layers.go`.
8. Write the layer's section of a node's status in `pkg/statusview/layers/<layer>`, which
   chalkctl's plugin prints.
9. Write the console's tab in `pkg/console/layers/<layer>`, and list it in `layerTabs` in
   `cmd/chalkos-console/layers.go`.
10. Add a row for each new directory of Go code to [Packages and programs](packages.md).
11. Regenerate the code and pages with `buf generate` and `nix run .#docgen`.
12. Build the checks `api-generated`, `docs-generated`, `docs`, `go-unit`, `go-lint`, `eval`,
    `eval-kinds` and `image-tests`. Extend an existing end-to-end test before you add one, as
    [Keep the VM tests few and small](testing.md#keep-the-vm-tests-few-and-small) asks.
