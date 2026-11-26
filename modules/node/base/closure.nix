# What the store leaves out. The UKI on the ESP carries the kernel and the initrd, so the system
# does not link them as well: nothing on a node boots from the store's copies. Nodes have no
# logins, so the system path holds none of NixOS's default packages, only what chalkos's and the
# image's modules put there; each unit names the tools it runs in its own path.
{
  config,
  lib,
  options,
  pkgs,
  ...
}:
let
  # The store paths of the shells a node must not carry, by their names: bash-5.3p15,
  # bash-interactive-5.3p15 and the static busybox-static-x86_64-unknown-linux-musl-1.37.0, not
  # bash-completion. nixpkgs splices each regex into a bash test unquoted, so none holds
  # parentheses or a bar.
  shells =
    lib.concatMap
      (name: [
        "^${builtins.storeDir}/[0-9a-z]+-${name}-[0-9]"
        "^${builtins.storeDir}/[0-9a-z]+-${name}-static-"
      ])
      [
        "bash"
        "bash-interactive"
        "dash"
        "busybox"
        "toybox"
        "mksh"
        "oksh"
        "loksh"
        "ksh"
        "yash"
        "zsh"
        "fish"
        "tcsh"
        "nushell"
      ];
  # The file names of shells' programs, which a package of another name may carry.
  shellPrograms = [
    "sh"
    "ash"
    "bash"
    "dash"
    "ksh"
    "mksh"
    "oksh"
    "yash"
    "zsh"
    "fish"
    "csh"
    "tcsh"
    "nu"
    "busybox"
    "toybox"
  ];
  shellsChecked = !config.chalkos.debug.tools && !config.chalkos.shell.allow;
  # What the initrd's store paths reference: make-initrd-ng copies a part of it into the
  # initrd, whose archive keeps no references.
  initrdSources = pkgs.writeText "chalkos-initrd-sources" (
    lib.concatMapStringsSep "\n" (p: "${p.source}") (
      lib.filter (p: p.enable) config.boot.initrd.systemd.storePaths
    )
  );
in
{
  options.chalkos.debug.tools = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Add bash, coreutils, grep, sed, findutils, procps, iproute2 and util-linux to the system
      path, and crictl on images with Kubernetes; systemd, less, nftables and kmod are on it
      already. Without the debug tools the system path holds no shell, and the build fails when
      a shell gets into the image unless [`chalkos.shell.allow`](#chalkosshellallow) is set.
      With them the image carries bash and allows a shell, and still has no `/bin/sh`.
      Nodes have no logins; use the tools from a privileged pod on the node, for example
      `kubectl debug node/<node> -it --profile=sysadmin --image=busybox -- chroot /host /run/current-system/sw/bin/bash`.
    '';
  };

  options.chalkos.shell.allow = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Let a shell, such as bash, dash or busybox, into the image's system or its initrd. Without
      it the build fails when one gets in, and names the shell's store paths and what references
      them. Nodes run no shell: nothing on them needs one, and a shell an attacker finds is the
      first tool they use.

      Set it on an image whose own units, outputs' checks, validators or diagnostics are shell
      scripts, such as those `pkgs.writeShellScript` and `pkgs.writeShellApplication` write,
      and to keep a package whose scripts the image does not run. A script still names its
      shell by its store path: the image has no `/bin/sh` either way.
      [Keep an image shell-free](../build/shell-free.md) shows how to find what brings a shell
      in and how to do without it. The debug tools allow a shell too.
    '';
  };

  config = {
    environment.corePackages = lib.mkForce [ ];
    environment.defaultPackages = lib.mkForce [ ];
    environment.systemPackages = lib.mkIf config.chalkos.debug.tools [
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gnused
      pkgs.findutils
      pkgs.procps
      pkgs.iproute2
      pkgs.util-linux
      pkgs.bashInteractive
    ];
    programs.nano.enable = false;
    security.sudo.enable = false;
    fonts.fontconfig.enable = false;
    boot.bcache.enable = false;
    boot.kexec.enable = false;
    i18n.defaultLocale = "C.UTF-8";
    i18n.supportedLocales = [ "C.UTF-8/UTF-8" ];

    # xfs_scrub brings python, ICU and GLib; mkfs.xfs, xfs_growfs and xfs_repair are what nodes
    # run.
    nixpkgs.overlays = [
      (_: prev: {
        xfsprogs = prev.xfsprogs.overrideAttrs (old: {
          configureFlags = old.configureFlags ++ [
            "--enable-scrub=no"
            "--enable-libicu=no"
          ];
          buildInputs = lib.filter (
            p:
            !lib.elem (lib.getName p) [
              "icu4c"
              "python3"
            ]
          ) old.buildInputs;
          # xfsprogs's shell scripts, xfs_admin, xfs_info and fsck.xfs among them: nodes carry no
          # shell to run them. The fixup moves the tools to $bin, so they go after it.
          postFixup = (old.postFixup or "") + ''
            for f in $bin/bin/*; do
              if [ "$(head -c 2 "$f")" = "#!" ]; then rm "$f"; fi
            done
          '';
        });
        # mtools's shell scripts. Nodes run none of mtools, which NixOS adds beside dosfstools. A
        # rebuild rather than pkgs.chalkos.trimPackage: the mtools program holds its own store path.
        mtools = prev.mtools.overrideAttrs (old: {
          postInstall = (old.postInstall or "") + ''
            for f in $out/bin/*; do
              if [ "$(head -c 2 "$f")" = "#!" ]; then rm "$f"; fi
            done
          '';
        });
        # The firewall's command-line tool, a bash script for interactive use.
        nixos-firewall-tool = prev.runCommand "nixos-firewall-tool-empty" { } "mkdir $out";
      })
    ];

    # xfsprogs without xfs_scrub ships no xfs_scrub_all unit, but NixOS sets that unit's PATH,
    # which would generate a unit holding nothing else.
    systemd.suppressedSystemUnits = [ "xfs_scrub_all.service" ];

    system.build.chalkosInitrdSources = initrdSources;
    system.systemBuilderCommands = lib.mkMerge [
      # A shell in the system, its extra dependencies or the initrd fails the build, before
      # nixpkgs's check of the same regexes would, naming what references each shell's path or
      # each program named after a shell. $closureInfo is the system's closure without its extra
      # dependencies, which nixpkgs lists for its forbidden regexes, and empty in the copy of the
      # system it lists it from, which holds no extra dependencies either.
      (lib.mkIf shellsChecked (
        lib.mkBefore ''
          chalkosNoShell() {
            local found programs shell
            [ -n "$2" ] || return 0
            if found=$(grep -E ${lib.escapeShellArg (lib.concatStringsSep "|" shells)} "$2/store-paths"); then
              echo "error: $1 holds a shell, which a node never runs:" >&2
              for shell in $found; do
                echo "  $shell, referenced by" >&2
                awk -v shell="$shell" '
                  s == 0 { path = $0; s = 1; next }
                  s < 4 { s++; next }
                  s == 4 { n = $0; s = n > 0 ? 5 : 0; next }
                  $0 == shell && path ~ /-chalkos-initrd-sources$/ { print "    boot.initrd.systemd.storePaths, contents or initrdBin" }
                  $0 == shell && path != shell && path !~ /-chalkos-initrd-sources$/ { print "    " path }
                  { if (--n == 0) s = 0 }
                ' "$2/registration" >&2
              done
              echo "Do without the shell, as chalkos's guide Keep an image shell-free shows, or set chalkos.shell.allow = true to keep it." >&2
              exit 1
            fi
            tr '\n' '\0' <"$2/store-paths" >store-paths
            programs=$(find -files0-from store-paths \( ${
              lib.concatMapStringsSep " -o " (p: "-name ${p}") shellPrograms
            } \) \( -type l -o -type f -perm -0100 \))
            rm store-paths
            if [ -n "$programs" ]; then
              echo "error: $1 holds programs named after a shell, which a node never runs:" >&2
              printf '  %s\n' $programs >&2
              echo "Do without them, as chalkos's guide Keep an image shell-free shows, or set chalkos.shell.allow = true to keep them." >&2
              exit 1
            fi
          }
          chalkosNoShell "the image's system" "$closureInfo"
          if [ -n "$closureInfo" ]; then
            chalkosNoShell "the image's extra dependencies" ${
              pkgs.closureInfo { rootPaths = config.system.extraDependencies; }
            }
          fi
          chalkosNoShell "the image's initrd" ${pkgs.closureInfo { rootPaths = [ initrdSources ]; }}
        ''
      ))
      # NixOS links both into the system's store path.
      (lib.mkAfter ''
        rm $out/kernel $out/initrd
      '')
    ];
    # nixos-init reads the system's bootspec in the initrd, so it stays, without the initrd and
    # with the UKI's path on the ESP as the kernel, which a bootspec must name. Nothing boots from
    # that path: only nixos-init's find-etc reads boot.json, for /etc, and the path goes stale once
    # an upgrade names the UKI by another generation or boot counting renames it.
    boot.bootspec.writer = lib.mkForce ''
      ${options.boot.bootspec.writer.default}
      ${lib.getExe pkgs.buildPackages.jq} --sort-keys --arg kernel /efi${config.image.repart.verityStore.ukiPath} \
        '."org.nixos.bootspec.v1" |= (del(.initrd) | .kernel = $kernel)' $out/boot.json >boot.json
      mv boot.json $out/boot.json
    '';
    # The build fails should anything else bring them back. The paths only name what to look for,
    # so the system's build does not depend on them. Nor does it carry a shell, unless the debug
    # tools or chalkos.shell.allow let one in.
    system.forbiddenDependenciesRegexes =
      map (path: "^${lib.escapeRegex (builtins.unsafeDiscardStringContext "${path}")}$") [
        config.boot.kernelPackages.kernel
        config.system.build.initialRamdisk
      ]
      ++ lib.optionals shellsChecked shells;
  };
}
