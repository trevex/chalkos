# The checks that hold the images' hardening: no shell in the images (no-shell), no program that
# may run one unless the allow-list says why it never does (no-shell-calls), no writable mount
# that runs programs and no unit that runs them from outside the store unless mayExec says why
# (noexec-mounts), and no unit with the full capability bounding set unless mayRunAsFullRoot says
# why (unit-sandbox). A contributor who adds a unit or a package that one of them refuses changes
# its allow-list here, with the reason.
{
  pkgs,
  self,
  checkLib,
  testing,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (checkLib) router worker;
  # The images whose units the unit checks read: the worker, the router, the installer and the
  # Kubernetes images with each feature that is off by default.
  unitImages = {
    inherit worker router;
    installer = testing.installer.nixos;
  }
  // lib.mapAttrs (_: image: image.nixos) checkLib.featureImages;
  # The enabled units that chalkos's modules, the node modules, the platforms and the installer,
  # define or change in each of unitImages. The test images' modules are left out.
  ownUnits = lib.mapAttrs (
    _: image:
    lib.filter (name: image.config.systemd.services.${name}.enable or false) (
      lib.unique (
        lib.concatMap (
          d:
          lib.optionals (
            lib.hasPrefix "${self}/modules/" d.file && !lib.hasPrefix "${self}/modules/testing/" d.file
          ) (lib.attrNames d.value)
        ) image.options.systemd.services.definitionsWithLocations
      )
    )
  ) unitImages;
  # The services of the image of unitImages called name.
  servicesOf = name: unitImages.${name}.config.systemd.services;
in
{
  # The worker, the router, the release images and the feature images have no /bin/sh and no shell
  # on the system path: nixos-init, which sets the system up in the initrd, is given no sh or env
  # binary to link, and the system has no bash activation script. The release images build with
  # neither the debug tools nor chalkos.shell.allow, so their builds hold that no shell is in
  # their systems or what their initrds' store paths reference. The feature images carry the
  # Kubernetes features that are off by default. The worker's NixOS firewall stays on: NixOS's
  # bashless profile, which chalkos does not import, would turn it off. The router runs a
  # firewall of its own. The Longhorn feature image's build fails with a host tool that is a
  # shell script, whose shell only the links below /usr reference, and with one named sh.
  no-shell =
    let
      release = self.releaseImages.${pkgs.stdenv.hostPlatform.system};
      images = {
        worker = worker.config;
        router = router.config;
      }
      // lib.mapAttrs (
        name: image:
        let
          c = image.nixos.config;
        in
        assert lib.assertMsg (
          !c.chalkos.debug.tools && !c.chalkos.shell.allow
        ) "no-shell: the release image ${name} allows a shell";
        c
      ) release
      // lib.mapAttrs (_: image: image.nixos.config) checkLib.featureImages;
      # The Longhorn feature image's builds with a host tool that must fail them: the test worker
      # allows a shell.
      refused =
        lib.mapAttrs
          (
            _: tool:
            pkgs.testers.testBuildFailure
              (checkLib.featureImages.longhorn.nixos.extendModules {
                modules = [ { chalkos.kubernetes.hostTools.foo = tool; } ];
              }).config.system.build.toplevel
          )
          {
            shellScript = "${pkgs.writeShellScript "foo" "true"}";
            programNamedSh = "${
              pkgs.writeTextFile {
                name = "chalkos-not-a-shell";
                text = "";
                executable = true;
                destination = "/bin/sh";
              }
            }/bin/sh";
          };
    in
    pkgs.runCommand "chalkos-no-shell" ({ nativeBuildInputs = [ pkgs.jq ]; } // refused) ''
      fail() { echo "error: $*" >&2; exit 1; }
      grep -q "the image's extra dependencies holds a shell" $shellScript/testBuildFailure.log \
        || fail "the Kubernetes image builds with a host tool that is a shell script"
      grep -q "holds programs named after a shell" $programNamedSh/testBuildFailure.log \
        || fail "the Kubernetes image builds with a host tool named sh"
      ${lib.concatStrings (
        lib.mapAttrsToList (name: c: ''
          top=${c.system.build.toplevel}
          jq -e '."org.nixos.nixos-init.v1" | (has("sh_binary") or has("env_binary")) | not' $top/boot.json >/dev/null \
            || fail "the ${name}'s bootspec gives nixos-init a /bin/sh or /usr/bin/env to link"
          [ ! -e $top/activate ] || fail "the ${name} has an activation script"
          for sh in sh bash; do
            [ ! -e $top/sw/bin/$sh ] || fail "the ${name}'s system path holds $sh"
          done
          for p in ${toString c.system.fsPackages}; do
            [ ! -e $p/bin/fsck.btrfs ] || fail "the ${name}'s btrfs-progs holds fsck.btrfs, a bash script"
          done
        '') images
      )}
      grep -q '^ *table inet nixos-fw {' ${checkLib.firewall} || fail "the worker's firewall has no nixos-fw table"
      touch $out
    '';

  # The release and feature images' ELF files that may run a shell: those that import system,
  # popen or wordexp, which glibc runs /bin/sh for, and those that hold /bin/sh as a string, as Go
  # and execl do. Each must be on the allow-list, by its package's name without the version and its
  # path below the package, such as frr/bin/vtysh, or a glob of those, with the reason it never
  # runs a shell on a node. A new caller, as FRR's watchfrr was, fails the check: run it without
  # the shell, drop it from the image or add it here with its reason. The scan covers each
  # image's system and what its initrd's store paths reference. libkbdfile pipes a compressed
  # keymap or font through a tool only when it cannot open the tool's library, so each image's
  # console keymap must load with the tools disabled, as systemd-vconsole-setup runs loadkeys,
  # and each initrd must carry the zlib that libkbdfile opens for a gzipped keymap or font.
  no-shell-calls =
    let
      release = self.releaseImages.${pkgs.stdenv.hostPlatform.system};
      images = lib.attrValues release;
      keymaps = lib.concatStrings (
        lib.mapAttrsToList (
          name: image:
          let
            c = image.nixos.config;
          in
          lib.optionalString c.console.enable ''
            KBDFILE_IGNORE_DECOMP_UTILS=1 LOADKEYS_KEYMAP_PATH='${c.environment.etc.kbd.source}/keymaps/**' \
              ${c.systemd.package.kbd}/bin/loadkeys -b -u ${lib.escapeShellArg c.console.keyMap} >/dev/null \
              || fail "the ${name}'s keymap ${c.console.keyMap} does not load with the decompression tools disabled"
            initrdZlib ${name} ${c.boot.initrd.systemd.package.kbd} ${c.system.build.initialRamdisk}/initrd
          ''
        ) release
      );
      closure = pkgs.closureInfo {
        rootPaths = lib.concatMap (
          image: with image.nixos.config.system.build; [
            toplevel
            chalkosInitrdSources
          ]
        ) (images ++ lib.attrValues checkLib.featureImages);
      };
      allowed = {
        "glibc/lib/libc.so.6" = "implements system, popen and wordexp";
        "systemd/*" =
          "the shell of user records without one, debug-shell.service, emergency mode, run0, systemd-run --shell and systemd-nspawn, none of which a node runs";
        "systemd-minimal/*" = allowed."systemd/*";
        "util-linux/*" =
          "the shell that login, su, runuser, script, nsenter, unshare and flock start when not given a command, which a node never leaves out";
        "util-linux-minimal/*" = allowed."util-linux/*";
        "shadow/*" = "login shells and vipw's editor; nodes have no logins";
        "linux-pam/lib/security/pam_shells.so" =
          "checks a login shell against /etc/shells; nodes have no logins";
        "coreutils/bin/coreutils" = "chroot without a command runs a shell; nothing runs chroot";
        "kmod/*" = "runs the install and remove commands of modprobe.d, which no image sets";
        "e2fsprogs/*" =
          "mke2fs -c and e2fsck -c run badblocks, debugfs runs a pager and libss runs a command after !; chalkos runs mke2fs and e2fsck without -c";
        "frr/bin/vtysh" = "runs VTYSH_PAGER as a pager, which chalkos's vtysh -c and -b leave unset";
        "frr/lib/libfrr.so*" = "show configuration compare runs diff, which nothing asks";
        "frr/libexec/frr/nhrpd" = "asks strongSwan's ipsec for its directory; no image runs nhrpd";
        "chrony/bin/chronyd" =
          "mails a clock change through the mailonchange directive, which chrony.conf never sets";
        "dnsmasq/bin/dnsmasq" = "runs dhcp-script, which the router's dnsmasq config never sets";
        "gnused/bin/sed" = "the e command and the e flag of s, which no unit's sed script uses";
        "kbd/bin/spawn_console" = "nothing runs it";
        "kbd/bin/spawn_login" = allowed."kbd/bin/spawn_console";
        "kbd/bin/resizecons" = allowed."kbd/bin/spawn_console";
        "kbd/lib/libkbdfile.so*" =
          "pipes a compressed keymap or font through a tool only when it cannot open the tool's library from its runpath, which below holds for the images' keymaps and initrds";
        "less/bin/less" = "shell escapes, LESSOPEN and the editor of an interactive session";
        "procps/bin/top" = "interactive";
        "procps/bin/watch" = "runs its command through a shell; interactive";
        "ncurses/bin/tput" = "tput init and reset run the terminal's init program; interactive";
        "ncurses/bin/tset" = allowed."ncurses/bin/tput";
        "npth/lib/libnpth.so*" = "wraps system for gnupg's threads";
        "gnupg/bin/gpg" = "shows photo IDs with a viewer; systemd-importd's verification shows none";
        "numactl/bin/numastat" = "reads other tools' output; qemu-ga links libnuma and runs no numastat";
        "rdma-core/bin/ibacm" = "a daemon no image runs";
        "glib/lib/libglib-2.0.so*" =
          "runs a script without #! through a shell; qemu-ga, which links GLib, runs commands only for guest-exec, which it does not allow";
        "libarchive/lib/libarchive.so*" = "writes #!/bin/sh into shar archives, which nothing writes";
        "audit/lib/libauparse.so*" = "names /usr/bin/sh among the shells it interprets in audit records";
        "tpm2-tss/lib/libtss2-tcti-cmd.so*" = "the cmd TCTI runs a command; chalkos uses the device TCTI";
        "iproute2/bin/tc" = "tc exec bpf runs a shell; nothing on a node runs it";
        "mtools/bin/mtools" =
          "expands a drive's file name from mtools.conf through a shell; nodes run no mtools";
        "erofs-utils/bin/mount.erofs" =
          "runs a shell for a FUSE mount alone; nodes mount erofs in the kernel";
        "nixos-init/bin/nixos-init" =
          "links /bin/sh when the bootspec names a shell, which the no-shell check holds it does not";
        "nixos-system-nixos/init" = allowed."nixos-init/bin/nixos-init";
        "cni-plugins-chalkos/bin/portmap" = "a string of Go's tables, which no code executes";
        "chalkd/bin/chalkd" = "the command of flannel's container, which runs in the container";
        "nvidia-driver-root/bin/nvidia-smi" =
          "NVIDIA's closed driver, which the GPU operator's containers run from the driver root; on the node, which has no /bin/sh, such a call fails instead of running a shell";
        "nvidia-driver-root/lib64/libcudadebugger.so*" = allowed."nvidia-driver-root/bin/nvidia-smi";
        "nvidia-driver-root/lib64/libnvidia-nvvm.so*" = allowed."nvidia-driver-root/bin/nvidia-smi";
        "open-iscsi-chalkos/bin/*" =
          "runs iscsid.conf's iscsid.startup when iscsid does not answer, which the image's iscsid.conf leaves unset";
      };
    in
    pkgs.runCommand "chalkos-no-shell-calls" { nativeBuildInputs = [ pkgs.pax-utils ]; } ''
      fail() { echo "error: $*" >&2; exit 1; }
      # Whether the initrd holds libz.so.1 in a directory of the runpath of its kbd's libkbdfile.
      initrdZlib() {
        ${lib.getExe' pkgs.libarchive "bsdtar"} -tf "$3" | sed 's|^\./||; s|^|/|' >initrd-files
        rpath=$(scanelf -q -F '%r' "$2/lib/libkbdfile.so.1" | cut -d' ' -f1)
        for dir in ''${rpath//:/ }; do
          if grep -qx "$dir/libz.so.1" initrd-files; then return; fi
        done
        fail "the $1's initrd lacks the zlib that libkbdfile opens"
      }
      ${keymaps}
      xargs scanelf -R -y -q -B -s -system,-popen,-wordexp -F '%s %F' <${closure}/store-paths \
        | sed 's/@[^ ,]*//g' >calls
      # /bin/sh as a word of its own, not /bin/shutdown.
      xargs grep -rlaE '/bin/sh([^A-Za-z0-9_.-]|$)' <${closure}/store-paths >strings || true
      xargs -r scanelf -q -B -F '%F' <strings | sed 's|^|/bin/sh |' >>calls
      sort -u calls | while read -r what file; do
        rest=''${file#"$NIX_STORE"/*-}
        package=''${rest%%/*}
        id=''${package%%-[0-9]*}/''${rest#*/}
        case $id in
          ${lib.concatStringsSep "|" (lib.attrNames allowed)}) ;;
          *) echo "error: $id ($file) may run a shell: it calls $what" >&2; touch failed ;;
        esac
      done
      if [ -e failed ]; then
        echo "Run it without a shell, leave it out of the image, or allow it in nix/checks/hardening.nix with the reason it never runs one on a node." >&2
        exit 1
      fi
      touch $out
    '';

  # No writable file system of the worker or the router lets a file run as a program, take
  # set-user-ID effect or open as a device, as fstab gives their options: the root tmpfs, /run,
  # /dev/shm, the outputs' tmpfs and the ESP. /dev, which holds the devices, is noexec. The
  # worker's exec paths, containerd's and the kubelet's roots, reach chalkos-storage's generator,
  # which mounts them, and containerd and the kubelet wait for them; the router has none. Every
  # unit that chalkos's modules define or change in the images of the unit checks runs programs
  # from the store alone, through chalkosLib.sandbox.noExec, or is one of mayExec, with the reason
  # it may not.
  noexec-mounts =
    let
      images = {
        worker = worker.config;
        router = router.config;
      };
      # The units of chalkos's modules that may run programs from outside the store, by the
      # reason: a unit whose mounts the host must see can have no mount namespace of its own, and
      # systemd's units keep the sandboxes systemd gives them.
      mayExec = {
        chalkd = "mounts volumes and the ESP, which the host must see";
        chalkos-nat64 = "ip netns add mounts each instance's namespace on /run/netns";
        containerd = "mounts the containers' file systems, which run from its exec path";
        iscsid = "Longhorn runs iscsiadm in its mount and network namespaces, which must be the host's";
        kubelet = "mounts the pods' volumes, whose programs run from its exec path";
        systemd-networkd = "systemd's; the router gives it credentials alone";
        systemd-sysctl = "systemd's; the router orders it alone";
        systemd-bless-boot = "systemd's; chalkos names its ESP alone";
      };
      noExec =
        s:
        s.serviceConfig.NoExecPaths or null == [ "/" ]
        && s.serviceConfig.ExecPaths or null == [ "/nix/store" ];
      units = ownUnits;
      unitFailures =
        name: own:
        map (
          unit:
          "the ${name}'s ${unit} may run programs from outside the store: give it chalkosLib.sandbox.noExec, or list it in mayExec in nix/checks/hardening.nix with the reason"
        ) (lib.filter (unit: !(mayExec ? ${unit}) && !noExec (servicesOf name).${unit}) own);
      allOwn = lib.unique (lib.concatLists (lib.attrValues units));
      stale = lib.filter (
        unit:
        !lib.elem unit allOwn
        || lib.any (name: lib.elem unit units.${name} && noExec (servicesOf name).${unit}) (
          lib.attrNames units
        )
      ) (lib.attrNames mayExec);
      fstabLine =
        name: c: mountPoint: flags:
        let
          fs = c.fileSystems.${mountPoint} or null;
          missing = lib.subtractLists (if fs == null then [ ] else fs.options) flags;
        in
        lib.optional (fs == null || missing != [ ])
          "the ${name}'s ${mountPoint} lacks ${
            lib.concatStringsSep "," (if fs == null then flags else missing)
          }";
      writable = [
        "nosuid"
        "nodev"
        "noexec"
      ];
      mountFailures =
        name: c:
        lib.concatMap (p: fstabLine name c p writable) (
          [
            "/"
            "/run"
            "/dev/shm"
            "/efi"
          ]
          ++ lib.optional (c.fileSystems ? "/run/chalkos/outputs") "/run/chalkos/outputs"
        )
        ++ fstabLine name c "/dev" [
          "nosuid"
          "noexec"
        ]
        ++ fstabLine name c "/efi" [
          "fmask=0177"
          "dmask=0077"
        ]
        ++ lib.optional (
          c.boot.kernel.sysctl."fs.protected_fifos" != 2
        ) "the ${name}'s fs.protected_fifos is not 2"
        ++ lib.optional (
          c.boot.kernel.sysctl."fs.protected_regular" != 2
        ) "the ${name}'s fs.protected_regular is not 2";
      w = images.worker;
      requiresMountsFor =
        unit: lib.toList (w.systemd.services.${unit}.unitConfig.RequiresMountsFor or [ ]);
      failures =
        lib.concatLists (lib.mapAttrsToList mountFailures images)
        ++ lib.optional (
          w.chalkos.storage.execPaths != {
            "/var/lib/containerd" = {
              suid = true;
              dev = false;
            };
            "/var/lib/kubelet" = {
              suid = false;
              dev = true;
            };
          }
        ) "the worker's exec paths are ${builtins.toJSON w.chalkos.storage.execPaths}"
        ++ lib.optional (
          !lib.elem "/var/lib/containerd" (requiresMountsFor "containerd")
        ) "containerd does not wait for /var/lib/containerd"
        ++ lib.optional (
          !lib.elem "/var/lib/kubelet" (requiresMountsFor "kubelet")
        ) "the kubelet does not wait for /var/lib/kubelet"
        ++ lib.concatLists (lib.mapAttrsToList unitFailures units)
        ++ lib.optional (lib.any (unit: !lib.elem unit units.worker) [
          "chalkos-console"
          "chalkos-registry"
          "chalkos-kubernetes"
        ]) "the worker lacks a unit the check expects of it"
        ++ lib.optional (lib.any (unit: !lib.elem unit units.router) [
          "chalkos-console"
          "chalkos-firewall"
          "frr-bgpd"
          "kea-dhcp4@"
          "unbound@"
          "dnsmasq-relay@"
          "chronyd"
        ]) "the router lacks a unit the check expects of it"
        ++ map (
          unit:
          "mayExec in nix/checks/hardening.nix lists ${unit}, which no image's node modules define without noExec"
        ) stale
        ++ lib.optional (
          images.router.chalkos.storage.execPaths != { }
        ) "the router has exec paths: ${toString (lib.attrNames images.router.chalkos.storage.execPaths)}";
    in
    pkgs.runCommand "chalkos-noexec-mounts" { } ''
      fail() { echo "error: $*" >&2; exit 1; }
      ${lib.concatMapStrings (f: "fail ${lib.escapeShellArg f}\n") failures}
      generator=${w.systemd.generators.chalkos-storage}
      for arg in --exec-path /var/lib/containerd=exec,suid,nodev /var/lib/kubelet=exec,nosuid,dev; do
        grep -qaF -- "$arg" $generator || fail "the worker's storage generator lacks $arg"
      done
      ! grep -qaF -- --exec-path ${images.router.systemd.generators.chalkos-storage} \
        || fail "the router's storage generator mounts exec paths"
      touch $out
    '';

  # Every unit that chalkos's modules define or change in the images of the unit checks keeps a
  # capability bounding set short of the full one, as chalkosLib.sandbox.hardened starts it, or is
  # one of mayRunAsFullRoot, with the reason it keeps every capability. A unit without
  # CapabilityBoundingSet, or with one of the capabilities it drops, ~CAP_..., keeps the full set.
  # The check's own probe units show that it refuses those, an empty list and a ~ after a space
  # included, and accepts them once listed.
  unit-sandbox =
    let
      # The units of chalkos's modules that keep every capability, by the reason.
      mayRunAsFullRoot = {
        chalkd = "the node agent: it mounts volumes, lays out and formats disks, enrols TPM2 key slots, writes EFI variables and the ESP and sets the host name";
        chalkos-config = "applies the config recorded on STATE at boot, before the units that read it, and runs every layer's part of that, which writes where the layer's services read";
        chalkos-health = "decides the boot's health: it asks chalkd, reads the ESP and EFI variables, records a failed boot on STATE and reboots; the capabilities this takes are not yet tested on a node";
        chalkos-links = "has udev read its rules again and replays the network devices' add events through /sys, which a sandbox's read-only /sys stops; the capabilities this takes are not yet tested on a node";
        chalkos-chrony-dhcp = "runs chronyc, which binds its socket in chronyd's directory, and ignores its failure, so a missing capability would go unnoticed";
        chalkos-kubernetes = "prepares the node's Kubernetes certificates, kubeconfigs and kubelet flags and fills the firewall's VXLAN table; the capabilities this takes are not yet tested on a node";
        chalkos-controlplane = "makes etcd's directory and loads or deletes the control plane's nftables table; the capabilities this takes are not yet tested on a node";
        chalkos-nvidia-cdi = "makes NVIDIA's device nodes, and NVML, which runs as root, those of each GPU";
        containerd = "runs the containers, privileged ones among them, with every capability its bounding set keeps; it drops CAP_SYS_MODULE and CAP_MAC_ADMIN";
        kubelet = "mounts the pods' volumes and manages their cgroups and devices";
        systemd-networkd = "systemd's; the router gives it credentials alone";
        systemd-sysctl = "systemd's; the router orders it alone";
        systemd-bless-boot = "systemd's; chalkos names its ESP alone";
      };
      # Whether the unit keeps every capability: an empty list writes no CapabilityBoundingSet=, and
      # systemd reads a ~ after leading whitespace too.
      fullRoot =
        s:
        let
          caps = s.serviceConfig.CapabilityBoundingSet or null;
        in
        caps == null || caps == [ ] || lib.any (c: lib.hasPrefix "~" (lib.trim c)) (lib.toList caps);
      # The failures of the units of an image, called name, of the services given, by the table.
      failures =
        table: services: name: units:
        map (
          unit:
          "the ${name}'s ${unit} keeps every capability: give it a CapabilityBoundingSet, as chalkosLib.sandbox.hardened does, or list it in mayRunAsFullRoot in nix/checks/hardening.nix with the reason"
        ) (lib.filter (unit: !(table ? ${unit}) && fullRoot services.${unit}) units);
      stale = lib.filter (
        unit:
        !lib.any (name: lib.elem unit ownUnits.${name} && fullRoot (servicesOf name).${unit}) (
          lib.attrNames ownUnits
        )
      ) (lib.attrNames mayRunAsFullRoot);
      probe = {
        unbound.serviceConfig = { };
        dropping.serviceConfig.CapabilityBoundingSet = "~CAP_SYS_MODULE";
        empty.serviceConfig = self.lib.sandbox.hardened;
        emptyList.serviceConfig.CapabilityBoundingSet = [ ];
        spacedDropping.serviceConfig.CapabilityBoundingSet = [ " ~CAP_SYS_MODULE" ];
        bound.serviceConfig.CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];
      };
      probeFailures = table: failures table probe "probe" (lib.attrNames probe);
      all =
        lib.concatLists (
          lib.mapAttrsToList (name: failures mayRunAsFullRoot (servicesOf name) name) ownUnits
        )
        ++ map (
          unit:
          "mayRunAsFullRoot in nix/checks/hardening.nix lists ${unit}, which no image's modules define with every capability"
        ) stale
        ++
          lib.optional
            (
              map (f: lib.head (lib.splitString ":" f)) (probeFailures { }) != [
                "the probe's dropping keeps every capability"
                "the probe's emptyList keeps every capability"
                "the probe's spacedDropping keeps every capability"
                "the probe's unbound keeps every capability"
              ]
            )
            "the check refuses other probe units than dropping, emptyList, spacedDropping and unbound, or not all four"
        ++ lib.optional (
          probeFailures {
            unbound = "a reason";
            dropping = "a reason";
            emptyList = "a reason";
            spacedDropping = "a reason";
          } != [ ]
        ) "the check refuses the probe's units that mayRunAsFullRoot lists";
    in
    pkgs.runCommand "chalkos-unit-sandbox" { } ''
      fail() { echo "error: $*" >&2; failed=1; }
      ${lib.concatMapStrings (f: "fail ${lib.escapeShellArg f}\n") all}
      [ -z "''${failed:-}" ] || exit 1
      touch $out
    '';
}
