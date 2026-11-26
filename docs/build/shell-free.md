---
title: "Keep an image shell-free"
description: "Find what brings a shell into an image, and run units, checks, effects and small programs without one"
---

# Keep an image shell-free

This page is for an image author whose image build fails because a shell got into the image, or
who adds units, outputs and programs to an image. At the end the image builds without a shell, and
its units and checks run programs directly.

## Why nodes have no shell

nixos-init sets the system up in the initrd in place of NixOS's bash activation, nothing activates
the system again, and nodes have no logins. No image has `/bin/sh` or `/usr/bin/env`. A shell on a
node would serve no unit, and an attacker who gets a foothold reaches for it first.

When a shell is in the closure of the image's system, its extra dependencies, such as the links
below `/usr`, or what the initrd's store paths reference, the build fails and names each shell with
the store paths that reference it. The build looks for the packages bash, bash-interactive, dash,
busybox, toybox, mksh, oksh, loksh, ksh, yash, zsh, fish, tcsh and nushell, their static builds,
and programs of any package named sh, ash, bash, dash, ksh, mksh, oksh, yash, zsh, fish, csh, tcsh,
nu, busybox or toybox:

```text
error: the image's system holds a shell, which a node never runs:
  /nix/store/<hash>-bash-5.3p20, referenced by
    /nix/store/<hash>-disk-report
Do without the shell, as chalkos's guide Keep an image shell-free shows, or set chalkos.shell.allow = true to keep it.
```

Two options let a shell in:

- [`chalkos.debug.tools`](../reference/image-options.md#chalkosdebugtools) adds bash and other
  tools to the system path, for a shell through `kubectl debug`, and allows a shell in the image.
  [Add the debug tools](add-software.md#add-the-debug-tools) covers them.
- [`chalkos.shell.allow`](../reference/image-options.md#chalkosshellallow) keeps a shell in the
  image without adding anything. Set it when the image's own units or checks are shell scripts
  and you accept the shell:

  ```nix
  {
    chalkos.shell.allow = true;
  }
  ```

With either option the image still has no `/bin/sh`. A shell script names its shell by its store
path, as `pkgs.writeShellScript` writes it, or it does not run.

The build sees store paths only. A program that runs `/bin/sh` by name, through `system()` or
`popen()` or a hardcoded path, builds and then fails on the node when it tries. Run such a program
only in a way that never reaches its shell, or replace it.

## Find what brings a shell in

The build's error names the store paths that reference the shell directly, such as a unit's
script or a package. Most causes are one of these:

| What brings the shell | Do instead |
| --- | --- |
| A unit's `script`, `preStart`, `postStart`, `reload`, `preStop` or `postStop`, which NixOS turns into bash scripts | Run a program in `ExecStart` and its siblings, as [Write units without a shell](#write-units-without-a-shell) shows |
| A script from `pkgs.writeShellScript`, `pkgs.writeShellApplication`, `pkgs.writers.writeBash` or `pkgs.writers.writeDash` | A program of the store, or a compiled one, as [Write small programs without a shell](#write-small-programs-without-a-shell) shows |
| A wrapper from `makeWrapper` or `wrapProgram`, which writes bash | `makeBinaryWrapper` |
| A script that a package ships beside its programs | Leave the script out, as [Packages that ship shell scripts](#packages-that-ship-shell-scripts) shows |
| An interpreter such as Python, whose own closure holds bash | A compiled program |

When the direct referrer does not explain it, find the whole chain. Build the image once with
`chalkos.shell.allow = true`, and ask Nix why the system depends on the shell, with the store
path the error named:

```sh
nix build .#node-metal.nixos.config.system.build.toplevel
nix why-depends --all .#node-metal.nixos.config.system.build.toplevel /nix/store/<hash>-bash-5.3p20
```

`--all` shows every chain, not only the shortest. The image's SBOM lists the shell among its
packages too, which helps to compare two images. Build it as
[Inspect what an image contains](add-software.md#inspect-what-an-image-contains) shows, and filter
the list:

```sh
nix build .#node-metal.sbom -o node-metal.spdx.json
jq -r '.packages[1:][] | "\(.name) \(.versionInfo)"' node-metal.spdx.json | grep -E '^(bash|bash-interactive|dash|busybox|toybox|mksh|oksh|loksh|ksh|yash|zsh|fish|tcsh|nushell) '
```

Take `chalkos.shell.allow` out again once the shell is gone.

## Check rendered files without a shell

chalkd runs an output's check, its renderer and a validator without a shell: it splits a command
on spaces and runs the program directly. `chalkosLib.check.run` and `chalkosLib.check.each`
cover what a check script did, and replace `$OUT` and `$FILE` in the arguments themselves.

`chalkosLib.check.run` runs a program once, with `$OUT` replaced by the directory of the rendered
files. The router's firewall checks its ruleset this way, with `nft --check` in a user and network
namespace of its own. For an output `myapp-rules` that renders `ruleset.nft`:

```nix
chalkos.outputs.myapp-rules.check = chalkosLib.check.run [
  "${pkgs.util-linux}/bin/unshare"
  "--map-root-user"
  "--net"
  "${pkgs.nftables}/bin/nft"
  "--check"
  "--file"
  "$OUT/ruleset.nft"
];
```

`chalkosLib.check.each` runs a program once for each file a glob matches, with `$FILE` replaced
by the file's path. The router's DHCP relay checks each of its configs with dnsmasq, as this
output `myapp-conf` does:

```nix
chalkos.outputs.myapp-conf.check = chalkosLib.check.each "*.conf" [
  "${pkgs.dnsmasq}/bin/dnsmasq"
  "--test"
  "--conf-file=$FILE"
];
```

A renderer and a validator written in jq need no shell either: `chalkosLib.render.jq` and
`chalkosLib.validate.jq` run jq on the input themselves.

```nix
chalkos.outputs.hardware-json.render = chalkosLib.render.jq ./hardware.json.jq;
chalkos.validators.hardware-rack.program = chalkosLib.validate.jq ./hardware-rack.jq;
```

[Render config files](render-config.md) and [Validate config with a program](validate-config.md)
cover both. A check that must prepare something first, such as the BGP guide's, which defines
the password constants that exist on the node alone before it runs `bird -p`, needs a program of
its own, as [Write small programs without a shell](#write-small-programs-without-a-shell) shows.

## Write units without a shell

Give a unit a program and its arguments in `ExecStart`, and leave `script` and `preStart` unset.
systemd itself does most of what a unit's shell script did:

| In a shell script | In the unit |
| --- | --- |
| `export NAME=value` | `Environment = [ "NAME=value" ];` or `EnvironmentFile` |
| `$NAME` in an argument | `$NAME`, which systemd splits on white space, or `${NAME}`, which it keeps as one argument |
| `>file` | `StandardOutput = "truncate:/path/file";`, or `append:` |
| `mkdir -p /var/lib/myapp` | `StateDirectory = "myapp";`, and `RuntimeDirectory` for `/run` |
| `cd /var/lib/myapp` | `WorkingDirectory` |
| `first && second` | `ExecStartPre` for the first, `ExecStart` for the second |
| `command \|\| true` | A `-` before the command: `ExecStartPre = "-${pkgs.foo}/bin/foo";` |
| `[ -e /path ] \|\| exit 0` | `ConditionPathExists = "/path";` in `unitConfig` |
| `$(cat secret)` | `LoadCredential`, or a Secret passed as a credential |

systemd's specifiers stand for the unit's paths, such as `%S` for `/var/lib` and `%t` for `/run`.
This unit writes the disks that nvme-cli lists to a file, with no script:

```nix
systemd.services.disk-report = {
  wantedBy = [ "multi-user.target" ];
  serviceConfig = {
    Type = "oneshot";
    StateDirectory = "disk-report";
    Environment = [ "NVME_FORMAT=--output-format=json" ];
    # In place of: nvme list $NVME_FORMAT >/var/lib/disk-report/disks.json
    ExecStart = "${pkgs.nvme-cli}/bin/nvme list $NVME_FORMAT";
    StandardOutput = "truncate:%S/disk-report/disks.json";
  };
};
```

systemd expands `$` and `%` in `ExecStart`, so a literal one is written `$$` or `%%`. A pipe or a
loop does not fit a unit: write it as a program.

A NixOS module may give its unit a `script` of its own. Empty it and set `ExecStart` in its place:

```nix
systemd.services.myapp = {
  script = lib.mkForce "";
  serviceConfig.ExecStart = "${pkgs.myapp}/bin/myapp --config /etc/myapp.conf";
};
```

chalkos's own modules do the same: the nftables unit runs `touch`, `cp` and `rm` in place of
NixOS's three one-line scripts.

chalkos's units also run programs from the store alone. `chalkosLib.sandbox.noExec` sets
systemd's `NoExecPaths=/` and `ExecPaths=/nix/store`, which give the unit a mount namespace in
which no other file system allows execution, not even an exec path. A unit of an extension that
runs nothing from elsewhere can do the same:

```nix
systemd.services.disk-report.serviceConfig = chalkosLib.sandbox.noExec;
```

Leave it out of a unit whose mounts the host must see, such as one that runs `ip netns add`:
the mount namespace keeps its mounts from the host.

A service that needs no devices, such as a network daemon, starts from
[`chalkosLib.sandbox.hardened`](../reference/extension-api.md#chalkoslibsandboxhardened): `noExec`,
no capabilities, Unix sockets alone, a read-only system and the rest the reference lists. The
unit widens it to what the service needs:

```nix
systemd.services.myapp.serviceConfig = chalkosLib.sandbox.hardened // {
  ExecStart = "${pkgs.myapp}/bin/myapp --listen [::]:443";
  DynamicUser = true;
  StateDirectory = "myapp";
  CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
  AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
  RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
};
```

A unit that a NixOS module defines may set some of these already. NixOS refuses two different
values of one setting and joins two lists, which would add the module's capabilities to the
preset's. `lib.mapAttrs (_: lib.mkForce)` over the preset and the unit's own values replaces the
module's, as the bird unit of the [BGP extension](../guides/bgp-extension.md) does.

An effect's `run` and a diagnostic's `command` take a program and its arguments too. chalkd splits
`run` on spaces, with no shell quoting, and runs it directly, so it can neither redirect nor
expand a variable:

```nix
chalkos.effects.bird = {
  outputs = [ "bird-conf" ];
  run = "${pkgs.bird3}/bin/birdc -s /run/bird/bird.ctl configure";
};
```

[React to changes](react-to-changes.md#choose-what-the-effect-does) and
[Add a diagnostic](add-a-diagnostic.md) give the rules of each.

## Write small programs without a shell

A program that sets flags or the environment of another needs no script. `makeBinaryWrapper`
writes a small compiled wrapper that sets them and runs the program. This one runs jq with a
program of the image:

```nix
hardwareSummary = pkgs.runCommand "hardware-summary" {
  nativeBuildInputs = [ pkgs.makeBinaryWrapper ];
  meta.mainProgram = "hardware-summary";
} ''
  makeBinaryWrapper ${lib.getExe pkgs.jq} $out/bin/hardware-summary \
    --set TZ UTC \
    --add-flags "-c -f ${./hardware-summary.jq}"
'';
```

`makeBinaryWrapper` takes the arguments of `makeWrapper`, such as `--set`, `--prefix` and
`--add-flags`. A package built with `makeBinaryWrapper` in its `nativeBuildInputs` gets a
`wrapProgram` that writes binary wrappers. `makeWrapper` writes bash scripts.

A program with logic of its own is a compiled one. nixpkgs's compiled writers build one from a
single source file, and their closures hold no shell: `pkgs.writers.writeRustBin` and
`pkgs.writers.writeNimBin`. This unit runs a Rust program:

```nix
systemd.services.hello = {
  wantedBy = [ "multi-user.target" ];
  serviceConfig = {
    Type = "oneshot";
    ExecStart = lib.getExe (
      pkgs.writers.writeRustBin "hello" { } ''
        fn main() {
            println!("hello from a node without a shell");
        }
      ''
    );
  };
};
```

A larger program is a package, such as a Go module that `pkgs.buildGoModule` builds, as chalkos
builds its own tools.

These do not fit:

- `pkgs.writeShellScript`, `pkgs.writeShellApplication` and `pkgs.writers.writeBash` and
  `writeDash` write shell scripts, so their closures hold the shell.
- `pkgs.writers.writePython3` and the other interpreted writers bring their interpreter, and
  Python's closure holds bash.

execline is a language without a shell that runs a script's lines as a chain of programs. nixpkgs
has no writer for it, but a script that names execlineb keeps the closure free of a shell. It is
still an interpreted script:

```nix
pkgs.writeScript "hello" ''
  #!${pkgs.execline}/bin/execlineb -P
  ${pkgs.coreutils}/bin/echo hello
''
```

## Packages that ship shell scripts

Some packages ship a shell script beside their programs, such as a setup or warning script that
a node never runs. The script references the shell, so the package brings it in. Leave the script
out in one of two ways.

A copy of the package without the script costs no rebuild: the copy is made from the binary
cache's build. chalkos runs iptables for the CNI plugins this way, without `iptables-apply`:

```nix
iptables = pkgs.runCommand "iptables-${pkgs.iptables.version}" { } ''
  cp -a ${pkgs.iptables} $out
  chmod u+w $out/bin
  rm $out/bin/iptables-apply $out/bin/ip6tables-apply
'';
```

A copy works only when the remaining files do not reference the original package, or the original
comes back into the closure, script and shell with it. The package references itself when
`nix-store -q --references` lists its own path. Name the copy wherever the image runs the
package, such as a unit's `ExecStart` or `path`. An overlay that replaces the package everywhere
also rebuilds every package that depends on it; chalkos's btrfs-progs overlay avoids that by
building containerd against the original.

Otherwise rebuild the package without the script, in `postFixup`, which runs after the build has
installed and wrapped everything. smartmontools references itself, and its
`smartd_warning.sh` brings bash:

```nix
smartmontools = pkgs.smartmontools.overrideAttrs (old: {
  postFixup = (old.postFixup or "") + ''
    rm $out/etc/smartd_warning.sh
  '';
});
```

The rebuild has no cache hit, and neither has anything built against it. Use the override where
the image runs the program, as with the copy, rather than in an overlay, so the rest of the image
keeps the binary cache's builds.

## Debug a node without a shell

Without a shell, look at a node through chalkos's own tools:

- `chalkctl status` names failed units, and `chalkctl logs --unit=<unit>` shows a unit's journal,
  as [Status, logs and the console](../operate/status-and-logs.md) describes.
- `chalkctl node diag` runs the image's diagnostics, read-only commands of the image.
  [Add a diagnostic](add-a-diagnostic.md) declares one for each service whose state an operator
  needs.

On an image with the Kubernetes layer and the debug tools, a privileged pod reaches the node's
tools through the host's root:

```sh
kubectl debug node/node1 -it --profile=sysadmin --image=busybox -- \
  chroot /host /run/current-system/sw/bin/bash
```

The bash it runs is the debug tools', named by its path on the system path, since the node has no
`/bin/sh`. The debug tools change the image, so they reach the nodes with an upgrade, and the
image they are in carries a shell.

## What next

- [Add software, drivers and kernel modules](add-software.md) adds packages and services within
  these limits.
- [Render config files](render-config.md) and [React to changes](react-to-changes.md) give the
  rules of outputs and effects.
- [The image](../how-it-works/node/image.md) explains how a node runs its image.
