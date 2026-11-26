# Checks of what an image build produces, without booting it.
{
  pkgs,
  self,
  checkLib,
  testing,
  fixtures,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (checkLib)
    chalkPkgs
    router
    routerRendered
    routerRuleset
    worker
    ;
  testLib = import ../../test/nix/lib.nix {
    inherit lib;
    flakeLib = self.lib;
    layers = self.nixosModules;
  };
  examples = fixtures.examples-metal.nixos.config;
  # The router features an image may leave out.
  inherit (router._module.args.routerLib) features;
  # The router image without each of the features alone and without all six, by the names of
  # the features left out joined by dashes.
  routerWithout = lib.listToAttrs (
    map (
      without: lib.nameValuePair (lib.concatStringsSep "-" without) (testLib.routerWithout without)
    ) (map lib.singleton features ++ [ features ])
  );
in
{
  # The worker's and the router's kernel module trees, and one with the gpu group: a module outside
  # an image's groups is not in its tree, and neither a name the kernel does not know, a directory
  # without modules, a module the image loads but its tree lacks, nor one whose dependency the tree
  # lacks gets past the build. Out-of-tree modules bring the in-tree modules they depend on.
  kernel-modules =
    let
      treeOf = nixos: nixos.config.system.build.chalkosKernelModules;
      tree = treeOf worker;
      gpu = treeOf (worker.extendModules { modules = [ { chalkos.kernel.moduleGroups = [ "gpu" ]; } ]; });
      # v4l2loopback depends on videodev, which no base group holds.
      v4l2loopback = worker.extendModules {
        modules = [
          (
            { config, ... }:
            {
              boot.extraModulePackages = [ config.boot.kernelPackages.v4l2loopback ];
              boot.kernelModules = [ "v4l2loopback" ];
            }
          )
        ];
      };
      v4l2loopbackCheck = v4l2loopback.config.system.build.chalkosKernelModulesCheck;
      tool = lib.getExe pkgs.chalkos.kernel-modules;
      full = lib.getOutput "modules" worker.config.boot.kernelPackages.kernel;
      version = worker.config.boot.kernelPackages.kernel.modDirVersion;
      # A package of one out-of-tree module whose modinfo names the dependencies given.
      stub =
        name: modinfo:
        pkgs.runCommandCC "chalkos-${name}" { } ''
          echo >empty.c
          $CC -c empty.c -o empty.o
          printf 'name=${name}\0${modinfo}\0vermagic=${version} SMP preempt mod_unload \0' >modinfo
          mkdir -p $out/lib/modules/${version}/extra
          $OBJCOPY --add-section .modinfo=modinfo --set-section-flags .modinfo=alloc,readonly \
            empty.o $out/lib/modules/${version}/extra/${name}.ko
        '';
      # uvcvideo, a soft dependency, is in no base group either.
      stubWithDependencies = stub "chalkos_stub" "depends=videodev\\0softdep=pre: uvcvideo";
      stubWithMissingDependency = stub "chalkos_stub_missing" "depends=chalkos_no_such_module";
    in
    pkgs.runCommand "chalkos-kernel-modules"
      {
        nativeBuildInputs = [ pkgs.kmod ];
        # The image with v4l2loopback passes its own check.
        checked = [ v4l2loopbackCheck ];
      }
      ''
        ${checkLib.prelude}
        has() {
          modprobe --config no-config -d "$1" -S ${version} --show-depends "$2" >/dev/null 2>&1
        }
        for module in virtio_net i2c_i801 i2c_dev lpc_ich vmw_balloon ptp_vmw ceph cifs rpcsec_gss_krb5; do
          has ${tree} $module || fail "the worker's tree lacks $module"
        done
        if has ${tree} amdgpu; then fail "the worker's tree holds amdgpu"; fi
        has ${gpu} amdgpu || fail "the gpu group's tree lacks amdgpu"
        # The Kubernetes layer's group holds what pod networks load, such as Open vSwitch and SCTP;
        # a router image carries the netfilter and queueing modules every image does, without it.
        for module in openvswitch sctp; do
          has ${tree} $module || fail "the worker's tree lacks $module"
          if has ${treeOf router} $module; then fail "the router's tree holds $module"; fi
        done
        for module in nf_tables nft_nat sch_cake act_mirred ifb; do
          has ${treeOf router} $module || fail "the router's tree lacks $module"
        done

        echo drivers/nvme >directories
        echo chalkos_no_such_module >names
        refuses "the filter of an unknown module" 'unknown kernel module chalkos_no_such_module' ${tool} filter ${full} unknown directories names
        echo drivers/chalkos_no_such_directory >directories
        : >names
        refuses "the filter of a directory without modules" 'no kernel module below drivers/chalkos_no_such_directory' ${tool} filter ${full} empty directories names

        echo amdgpu >loaded
        refuses "the check of a module outside the tree" 'does not hold: amdgpu' ${tool} check ${tree} loaded

        has ${treeOf v4l2loopback} videodev || fail "the tree of an image with v4l2loopback lacks videodev"
        # An image's tree holds its out-of-tree modules too, as its own files: pods that mount it
        # read it without the store.
        has ${treeOf v4l2loopback} v4l2loopback || fail "the tree of an image with v4l2loopback lacks it"
        has ${treeOf router} jool || fail "the router's tree lacks jool"
        for t in ${treeOf v4l2loopback} ${treeOf router} ${tree}; do
          links=$(find "$t" -type l)
          [ -z "$links" ] || fail "the module tree $t holds links: $links"
        done
        # . is every module of the kernel.
        echo . >directories
        : >names
        ${tool} filter ${full} "$PWD/everything" directories names
        [ "$(wc -l <everything/lib/modules/${version}/modules.dep)" = "$(wc -l <${full}/lib/modules/${version}/modules.dep)" ] ||
          fail "the tree of every module holds $(wc -l <everything/lib/modules/${version}/modules.dep) of the kernel's $(wc -l <${full}/lib/modules/${version}/modules.dep)"
        rm -r everything
        echo drivers/nvme >directories
        ${tool} filter ${full} "$PWD/stubbed" directories names ${stubWithDependencies}
        has stubbed chalkos_stub || fail "the out-of-tree module is missing from the tree"
        has stubbed videodev || fail "the dependency of an out-of-tree module is missing"
        has stubbed uvcvideo || fail "the soft dependency of an out-of-tree module is missing"
        refuses "the filter of an out-of-tree module whose dependency the kernel lacks" 'chalkos_stub_missing depends on chalkos_no_such_module' \
          ${tool} filter ${full} missing directories names ${stubWithMissingDependency}
        : >loaded
        refuses "the check of a module whose dependency the tree lacks" 'chalkos_stub depends on videodev' ${tool} check ${stubWithDependencies} loaded
        grep -q 'to chalkos.kernel.extraModules' refused.log || fail "the error does not say how to fix it: $(cat refused.log)"
        touch $out
      '';

  # The worker's UKI carries the module policy first: an uncompressed archive whose
  # etc/chalkos/module-policy holds the policy with the store's root hash, padded with NULs to
  # modulepolicy.SlotSize, unsigned until chalkbuild sign signs it.
  module-policy =
    let
      c = worker.config;
      uki = "${c.system.build.uki}/${c.system.boot.loader.ukiFile}";
    in
    pkgs.runCommand "chalkos-module-policy"
      {
        nativeBuildInputs = [
          pkgs.binutils
          pkgs.cpio
          pkgs.jq
        ];
        inherit (c.system.build) chalkosModulePolicy;
        passAsFile = [ "chalkosModulePolicy" ];
      }
      ''
        fail() { echo "error: $*" >&2; exit 1; }
        objcopy --dump-section .initrd=initrd.bin ${uki} uki.copy
        last=$(cpio -t --quiet < initrd.bin | tail -n 1)
        [ "$last" = etc/chalkos/module-policy ] || fail "the UKI's first archive ends with $last"
        cpio -i --quiet --to-stdout etc/chalkos/module-policy < initrd.bin > slot
        [ "$(stat -c %s slot)" = 16384 ] || fail "the policy's file is $(stat -c %s slot) bytes"
        root=$(jq -r '[.[] | .roothash // empty] | first' ${c.system.build.image}/repart-output.json)
        sed "s/@ROOT_HASH@/$root/" $chalkosModulePolicyPath > want
        tr -d '\0' < slot > got
        cmp got want || fail "the UKI's policy is not the image's"
        touch $out
      '';

  # The test images on each platform and the installer within their ceilings
  # (testing/image-sizes.nix), and without Python in their closures. An image over one fails the
  # check with the largest paths of its system's closure, read from closureInfo, as the build has
  # no Nix daemon to ask. The images' own fit check is tried on the test image too, and its measure
  # of each image agrees with package verity, which an upgrade reads the sizes with.
  image-size =
    let
      inherit (pkgs.chalkos) veritysize;
      ceilings = import ../testing/image-sizes.nix;
      measure =
        name: config: ceiling:
        let
          image = config.system.build.image;
        in
        lib.escapeShellArgs [
          name
          "${image}/${config.image.fileName}"
          "${image}/repart-output.json"
          "${config.system.build.uki}/${config.system.boot.loader.ukiFile}"
          (pkgs.closureInfo { rootPaths = [ config.system.build.toplevel ]; })
          ceiling.storeData
          ceiling.hashTree
          ceiling.uki
        ];
      images = lib.mapAttrsToList (name: measure name testing.${name}.nixos.config) ceilings.images;
      test = testing.test-metal.nixos.config;
      testImage = "${test.system.build.image}/${test.image.fileName}";
      testPartitions = "${test.system.build.image}/repart-output.json";
      testUKI = "${test.system.build.uki}/${test.system.boot.loader.ukiFile}";
    in
    pkgs.runCommand "chalkos-image-size"
      {
        nativeBuildInputs = [
          pkgs.chalkos.image-size
          veritysize
        ];
      }
      ''
        mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }
        failed=0
        check() {
          local name=$1 raw=$2 partitions=$3 uki=$4 closure=$5 sizes verity data hash over=()
          local -A ceiling=([storeData]=$6 [hashTree]=$7 [uki]=$8)
          # Python takes over 100 MiB; a package that names it, as FRR's build did, brings it in:
          # python3-3.x, python3-minimal and the like, or a package of Python's, python3.x-*.
          if grep -E -- '-python3[.-]' "$closure/store-paths" >python; then
            echo "error: $name's closure holds Python: $(cat python)" >&2
            failed=1
          fi
          sizes=$(chalkos-image-size measure "$raw" "$partitions")
          verity=$(veritysize "$raw" "$partitions")
          if [[ $verity != "$sizes" ]]; then
            echo "error: $name: chalkos-image-size measures $sizes bytes of data and hash tree, package verity $verity" >&2
            failed=1
          fi
          read -r data hash <<<"$sizes"
          local -A size=([storeData]=$data [hashTree]=$hash [uki]=$(stat -L -c %s "$uki"))
          for part in storeData hashTree uki; do
            # A ceiling may be a fraction of a MiB, which Nix writes as 2.200000.
            echo "$name: $part $(mib "''${size[$part]}") of at most $(awk -v c="''${ceiling[$part]}" 'BEGIN { printf "%g", c }') MiB"
            if awk -v size="''${size[$part]}" -v ceiling="''${ceiling[$part]}" \
              'BEGIN { exit !(size > ceiling * 1048576) }'; then
              over+=("$part")
            fi
          done
          if [[ ''${#over[@]} != 0 ]]; then
            echo "error: $name is over its ceiling in ''${over[*]} (nix/testing/image-sizes.nix); the largest paths of its closure:" >&2
            # closureInfo's registration: a path, its hash, its size, its deriver, its number of
            # references and the references.
            awk 'step == 0 { path = $0; step = 1; next }
              step == 1 { step = 2; next }
              step == 2 { size = $0; step = 3; next }
              step == 3 { step = 4; next }
              step == 4 { refs = $0; print size, path; step = refs > 0 ? 5 : 0; next }
              step == 5 { if (--refs == 0) step = 0 }' "$closure/registration" |
              sort -rn | awk 'NR <= 20' | while read -r bytes path; do echo "  $(mib "$bytes") $path"; done >&2
            failed=1
          fi
        }
        ${lib.concatMapStrings (args: "check ${args}\n") images}
        check ${measure "installer" testing.installer.nixos.config ceilings.installer}

        # An image over a ceiling fails the check and lists the largest paths of its closure.
        if (
          failed=0
          check ${
            measure "test" test {
              storeData = 1;
              hashTree = 1;
              uki = 1;
            }
          }
          exit "$failed"
        ) >/dev/null 2>errors; then
          echo "error: the test image passed ceilings of 1 MiB" >&2
          failed=1
        fi
        grep -q "(nix/testing/image-sizes.nix); the largest paths of its closure" errors && grep -q "MiB /nix/store/" errors || {
          cat errors >&2
          failed=1
        }

        # The fit check every image build runs refuses a store, a hash tree or UKIs that leave no
        # room.
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 64M 64M 256M 2>errors; then
          echo "error: the fit check passed a store larger than its slot" >&2
          failed=1
        fi
        grep -q "the store's data takes" errors || { cat errors >&2; failed=1; }
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 1M 256M 2>errors; then
          echo "error: the fit check passed a hash tree larger than its partition" >&2
          failed=1
        fi
        grep -q "the store's hash tree takes" errors || { cat errors >&2; failed=1; }
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 64M 100M 2>errors; then
          echo "error: the fit check passed UKIs larger than the ESP" >&2
          failed=1
        fi
        grep -q "3 UKIs of" errors || { cat errors >&2; failed=1; }
        chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 64M 256M

        if [[ $failed != 0 ]]; then exit 1; fi
        touch $out
      '';

  # An image's bootspec, which nixos-init reads in the initrd to find /etc, names no initrd and, as
  # its kernel, the UKI on the ESP: the store holds neither the kernel nor the initrd.
  bootspec =
    let
      bootJSON = "${worker.config.system.build.toplevel}/boot.json";
    in
    pkgs.runCommand "chalkos-bootspec" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e '."org.nixos.bootspec.v1" | (has("initrd") | not) and (.kernel | startswith("/efi/"))' \
        ${bootJSON} >/dev/null || {
        echo "error: the bootspec names an initrd or a kernel off the ESP:" >&2
        cat ${bootJSON} >&2
        exit 1
      }
      touch $out
    '';

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
        echo "Run it without a shell, leave it out of the image, or allow it in nix/checks/image.nix with the reason it never runs one on a node." >&2
        exit 1
      fi
      touch $out
    '';

  # No writable file system of the worker or the router lets a file run as a program, take
  # set-user-ID effect or open as a device, as fstab gives their options: the root tmpfs, /run,
  # /dev/shm, the outputs' tmpfs and the ESP. /dev, which holds the devices, is noexec. The
  # worker's exec paths, containerd's and the kubelet's roots, reach chalkos-storage's generator,
  # which mounts them, and containerd and the kubelet wait for them; the router has none. Every
  # unit that chalkos's node modules define or change runs programs from the store alone, through
  # chalkosLib.sandbox.noExec, or is one of mayExec, with the reason it may not.
  noexec-mounts =
    let
      images = {
        worker = worker.config;
        router = router.config;
      };
      # The units of chalkos's node modules that may run programs from outside the store, by the
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
      modules = "${self}/modules/node/";
      # The enabled units chalkos's node modules define or change in the image.
      ownUnits =
        image:
        lib.filter (name: image.config.systemd.services.${name}.enable or false) (
          lib.unique (
            lib.concatMap (
              d: lib.optionals (lib.hasPrefix modules d.file) (lib.attrNames d.value)
            ) image.options.systemd.services.definitionsWithLocations
          )
        );
      units = lib.mapAttrs (_: ownUnits) {
        inherit worker router;
      };
      unitFailures =
        name: own:
        map (
          unit:
          "the ${name}'s ${unit} may run programs from outside the store: give it chalkosLib.sandbox.noExec, or list it in mayExec in nix/checks/image.nix with the reason"
        ) (lib.filter (unit: !(mayExec ? ${unit}) && !noExec images.${name}.systemd.services.${unit}) own);
      allOwn = lib.unique (lib.concatLists (lib.attrValues units));
      stale = lib.filter (
        unit:
        !lib.elem unit allOwn
        || lib.any (c: c.systemd.services ? ${unit} && noExec c.systemd.services.${unit}) (
          lib.attrValues images
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
          "mayExec in nix/checks/image.nix lists ${unit}, which no image's node modules define without noExec"
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

  # An image builds only when its rules compile and read the facts it declares, and its effects
  # act on units it defines. The test image's Greeting and the examples' Rack, Hardware and Peer
  # bring extension kinds with their own rules, facts, effects and outputs; a rule that does not
  # compile, one that reads a fact no module declares, an effect's rule that does not compile, an
  # effect on a unit the image lacks and a diagnostic whose regex does not compile fail the check
  # of their image. A router image checks its rules and holds the modules of its firewall and
  # Jool, its firewall check refuses what the kernel would, its FRR check what vtysh would, its
  # Jool check what jool would and its Unbound check what unbound-checkconf would. A Kubernetes
  # image's FRR check refuses what vtysh would. A router image
  # without BGP, NAT64, the resolver, DHCP, the DHCP relay or WireGuard, and one without all six,
  # checks its rules. image-tests renders the images' outputs.
  rules =
    let
      build = modules: (testLib.worker modules).system.build;
      examplesBuild = examples.system.build;
      routerBuild = router.config.system.build;
      # The router image loads nf_conntrack, the modules of its firewall and Jool, which its module
      # tree must hold with what they depend on.
      routerModules = routerBuild.chalkosKernelModulesCheck;
      demo = rule: {
        chalkos.kinds.Demo = {
          group = "demo.example.org";
          version = "v1alpha1";
          rules = [
            {
              inherit rule;
              message = "a demo";
            }
          ];
        };
      };
      broken = build [ (demo "documents ==") ];
      undeclared = build [ (demo "facts.missing == null") ];
      brokenEffect = build [
        {
          systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
          chalkos.effects.bird = {
            when = "config ==";
            restart = [ "bird.service" ];
          };
        }
      ];
      undefinedUnit = build [
        {
          chalkos.effects.bird = {
            when = "true";
            restart = [ "bird.service" ];
          };
        }
      ];
      # A diagnostic whose pattern Go's regexp does not compile, which no evaluation catches.
      brokenDiagnostic = build [
        (
          { pkgs, ... }:
          {
            chalkos.diagnostics.broken = {
              description = "A diagnostic whose pattern does not compile.";
              command = [
                "${pkgs.iproute2}/bin/ip"
                "route"
                "show"
                "table"
                "{table}"
              ];
              parameters.table = {
                type = "pattern";
                regex = "^[a-z$";
                description = "A table.";
              };
            };
          }
        )
      ];
    in
    pkgs.runCommand "chalkos-rules"
      {
        nativeBuildInputs = [
          pkgs.chalkos.chalkos-rules
          pkgs.jq
        ];
        checked = [
          testing.test-metal.nixos.config.system.build.chalkosRulesCheck
          examplesBuild.chalkosRulesCheck
          routerBuild.chalkosRulesCheck
          routerModules
        ]
        ++ lib.mapAttrsToList (_: c: c.system.build.chalkosRulesCheck) routerWithout;
      }
      ''
        ${checkLib.prelude}
        chalkos-rules check ${examplesBuild.chalkosRulesImage} | grep -q rackSurvey
        # refused <image> <message>: the check of the image's files fails, and says why.
        refused() { refuses "chalkos-rules check $1" "$2" chalkos-rules check "$1"; }
        refused ${broken.chalkosRulesImage} 'Demo: rule "documents ==": ERROR: <input>:1:13: Syntax error'
        refused ${undeclared.chalkosRulesImage} 'Demo: rule "facts.missing == null" reads facts.missing, which the image does not declare'
        refused ${brokenEffect.chalkosRulesImage} 'effect bird: rule "config ==": ERROR: <input>:1:10: Syntax error'
        refused ${undefinedUnit.chalkosRulesImage} 'effect bird: the image defines no unit bird.service'
        refused ${brokenDiagnostic.chalkosRulesImage} 'diagnostic broken: parameter table: its regex: error parsing regexp: missing closing ]'
        # The check of chrony's output passes the servers its template renders, and refuses a
        # line chronyd does not parse or a sourcedir file does not take.
        read -ra check < <(jq -r .chrony.check ${testing.test-metal.nixos.config.system.build.chalkosOutputs})
        mkdir chrony
        printf 'server a.example iburst nts\nserver 10.0.0.1 iburst port 12300\n' >chrony/servers.sources
        OUT=$PWD/chrony "''${check[@]}" </dev/null
        for bad in 'server a.example iburst bogus' 'pool a.example iburst' 'server a.example iburst nts maxpoll'; do
          printf '%s\n' "$bad" >chrony/servers.sources
          refuses "chrony's check of $bad" "" env OUT=$PWD/chrony "''${check[@]}" </dev/null
        done
        # The check of the router's time server passes the lines its renderer writes, and refuses
        # a subnet chronyd -p takes and any line of another form.
        read -ra check < <(jq -r '."chrony-server".check' ${routerBuild.chalkosOutputs})
        mkdir chrony-server
        printf '# header\nallow 192.0.2.0/24\nallow 2001:db8:1::/64\nratelimit interval 1 burst 16\nlocal stratum 15 orphan\n' >chrony-server/server.conf
        OUT=$PWD/chrony-server "''${check[@]}" </dev/null
        for bad in 'allow 10' 'allow all' 'local stratum 10' 'local stratum 16 orphan' 'port 0'; do
          printf '%s\n' "$bad" >chrony-server/server.conf
          refuses "chrony-server's check of $bad" "" env OUT=$PWD/chrony-server "''${check[@]}" </dev/null
        done
        # The check of the router's firewall passes the LAN example's ruleset and refuses one the
        # kernel would not load, with nft in a user and network namespace of its own.
        read -ra check < <(jq -r '."router-nftables".check' ${routerBuild.chalkosOutputs})
        mkdir nftables
        install -m 644 ${routerRuleset "lan"} nftables/ruleset.nft
        OUT=$PWD/nftables "''${check[@]}" </dev/null
        printf 'table inet chalkos {\n  chain input {\n    iif "nosuchif0" accept\n  }\n}\n' >nftables/ruleset.nft
        refuses "the router's nftables check" 'Error: Interface does not exist' env OUT=$PWD/nftables "''${check[@]}" </dev/null
        # The check of FRR's config passes the edge example's and the IPv4 and IPv6 tracking of
        # the renderer's tests, and refuses a command no daemon takes, with vtysh alone.
        read -ra check < <(jq -r .frr.check ${routerBuild.chalkosOutputs})
        mkdir frr
        install -m 644 ${../../cmd/chalkos-render/testdata/router/tracked.frr} frr/frr.conf
        OUT=$PWD/frr "''${check[@]}" </dev/null
        install -m 644 ${routerRendered "edge"}/frr/frr.conf frr/frr.conf
        OUT=$PWD/frr "''${check[@]}" </dev/null
        sed -i 's/^ neighbor peer-fabric bfd$/ neighbor peer-fabric bfd sometimes/' frr/frr.conf
        refuses "the router's FRR check" 'Unknown command' env OUT=$PWD/frr "''${check[@]}" </dev/null
        grep -qF 'neighbor peer-fabric bfd sometimes' refused.log || fail "the router's FRR check refused another command: $(cat refused.log)"
        # The check of the Kubernetes layer's FRR config passes the homelab's nodes' and that of
        # unnumbered and address sessions side by side, and refuses a command no daemon takes,
        # with vtysh alone.
        read -ra check < <(jq -r .bgp.check ${examplesBuild.chalkosOutputs})
        mkdir bgp
        install -m 644 ${../../modules/node/kubernetes/bgp/tests/mixed/bgp/frr.conf} bgp/frr.conf
        OUT=$PWD/bgp "''${check[@]}" </dev/null
        install -m 644 ${../../modules/node/kubernetes/bgp/tests/address-interface/bgp/frr.conf} bgp/frr.conf
        OUT=$PWD/bgp "''${check[@]}" </dev/null
        sed -i 's/^ neighbor peer-router-enp1s0 bfd$/ neighbor peer-router-enp1s0 bfd sometimes/' bgp/frr.conf
        refuses "the Kubernetes FRR check" 'Unknown command' env OUT=$PWD/bgp "''${check[@]}" </dev/null
        grep -qF 'neighbor peer-router-enp1s0 bfd sometimes' refused.log || fail "the Kubernetes FRR check refused another command: $(cat refused.log)"
        # The check of Jool's instances passes the edge example's and refuses a port past 65535,
        # with jool alone. It gets the edge example's instance file without its .veth.
        read -ra check < <(jq -r .jool.check ${routerBuild.chalkosOutputs})
        mkdir jool
        install -m 644 ${routerRendered "edge"}/jool/edge.json jool/edge.json
        OUT=$PWD/jool "''${check[@]}" </dev/null
        sed -i 's/"61001-65535"/"61001-70000"/' jool/edge.json
        refuses "the router's Jool check" "'70000' is out of bounds" env OUT=$PWD/jool "''${check[@]}" </dev/null
        # The check of Unbound's config passes the LAN example's, with the trust anchor and zone
        # the image renders it with in place of the test result's, whose store paths have their
        # hashes written as e's, and refuses a record Unbound does not take.
        read -ra check < <(jq -r .unbound.check ${routerBuild.chalkosOutputs})
        read -r _ _ anchor zone < <(jq -r .unbound.render ${routerBuild.chalkosOutputs})
        mkdir unbound
        sed -e "s|^\( *trust-anchor-file: \).*|\1\"$anchor\"|" -e "s|^\( *zonefile: \).*|\1\"$zone\"|" \
          ${routerRendered "lan"}/unbound/unbound.conf >unbound/unbound.conf
        grep -qF "trust-anchor-file: \"$anchor\"" unbound/unbound.conf \
          || fail "the LAN example's unbound.conf names no trust anchor to put $anchor in place of"
        OUT=$PWD/unbound "''${check[@]}" </dev/null
        sed -i 's/IN A 192.0.2.1/IN A 192.0.2.300/' unbound/unbound.conf
        refuses "the router's Unbound check" 'Bad local-data RR' env OUT=$PWD/unbound "''${check[@]}" </dev/null
        touch $out
      '';

  # The tests of chalkos.tests of the test image, the router image with every feature, without
  # each of the features an image may leave out and without all six, and the image of the
  # examples' extensions, as nix flake check runs an image's tests: the router's examples, the
  # networkd, chrony and Greeting examples, and those of the Hardware and bird-conf outputs
  # render, pass the outputs' checks and the validators accept them.
  image-tests = pkgs.linkFarm "chalkos-image-tests" (
    lib.mapAttrs (_: c: c.system.build.chalkosTests) (
      {
        test = testing.test-metal.nixos.config;
        router = router.config;
        inherit examples;
      }
      // lib.mapAttrs' (without: c: lib.nameValuePair "router-without-${without}" c) routerWithout
    )
  );

  # An image directory describes itself and carries a schema for each kind it lists.
  image-contract =
    pkgs.runCommand "chalkos-image-contract"
      {
        nativeBuildInputs = [
          pkgs.jq
          pkgs.util-linux
          pkgs.mtools
        ];
      }
      ''
        dir=${chalkPkgs.test-image}
        jq -e '
          .schemaVersion == 0
          and .id == "chalkos"
          and (has("version") | not)
          and .revision == null
          and .tag == null
          and .name == "test-metal"
          and .platform == "metal"
          and (.rootHash | test("^[0-9a-f]{64}$"))
          and ([.kinds[].kind] | index("Network") != null)
          and (has("kubernetes") | not)
        ' $dir/metadata.json
        # Slot A's labels name its store by the root hash, in the raw image as in
        # repart-output.json, and the image boots its UKI as the first generation.
        id=$(jq -r '.rootHash[0:16]' $dir/metadata.json)
        want='["store-verity_'$id'","store_'$id'"]'
        sfdisk --json $dir/chalkos.raw | jq -e --argjson want "$want" \
          '[.partitiontable.partitions[].name | select(startswith("store"))] == $want' >/dev/null || {
          echo "error: slot A of chalkos.raw is not labelled $want" >&2
          exit 1
        }
        jq -e --argjson want "$want" '[.[].label | select(startswith("store"))] == $want' $dir/repart-output.json >/dev/null || {
          echo "error: repart-output.json does not label slot A $want" >&2
          exit 1
        }
        esp=$(jq -r '.[] | select(.type == "esp") | .offset' $dir/repart-output.json)
        MTOOLS_SKIP_CHECK=1 mdir -b -i "$dir/chalkos.raw@@$esp" ::/EFI/Linux | grep -qi '/chalkos_1\.efi *$' || {
          echo "error: the image's ESP holds no /EFI/Linux/chalkos_1.efi" >&2
          exit 1
        }
        for k in $(jq -r '.kinds[] | "\(.apiVersion | sub("/"; "_"))_\(.kind).json"' $dir/metadata.json); do
          test -f $dir/schemas/$k || { echo "error: metadata.json lists $k, which schemas/ lacks" >&2; exit 1; }
        done
        if [ -n "$(ls -A $dir/facts)" ]; then
          echo "error: the test image runs no Kubernetes, but its directory has facts: $(ls $dir/facts)" >&2
          exit 1
        fi
        k=${chalkPkgs.test-kubernetes-image}
        test "$(ls $k/facts)" = kubernetes.json
        jq -e '.cni == "flannel" and (keys == ["cni", "version"])' $k/facts/kubernetes.json
        jq -e 'has("kubernetes") | not' $k/metadata.json
        jq -e '.name == "k8s-kvm"' $k/metadata.json
        touch $out
      '';

  # The test image's OCI layout: one manifest of the chalkos artifact type, whose header names the
  # image directory's root hash and platform, whose layers are the image's parts and contract in
  # order, whose annotations are those metadata.json names, and whose contract holds the
  # directory's metadata.json.
  image-oci = pkgs.runCommand "chalkos-image-oci" { nativeBuildInputs = [ pkgs.jq ]; } ''
    l=${chalkPkgs.test-image-oci}
    dir=${chalkPkgs.test-image}
    blob() { echo "$l/blobs/sha256/''${1#sha256:}"; }
    jq -e '.manifests | length == 1' $l/index.json
    m=$(blob "$(jq -r '.manifests[0].digest' $l/index.json)")
    jq -e '
      .artifactType == "application/vnd.chalkos.image.v1"
      and .config.mediaType == "application/vnd.chalkos.image.header.v1+json"
      and [.layers[].mediaType] == [
        "application/vnd.chalkos.image.store.v1",
        "application/vnd.chalkos.image.hash-tree.v1",
        "application/vnd.chalkos.image.uki.v1",
        "application/vnd.chalkos.image.boot-loader.v1",
        "application/vnd.chalkos.image.contract.v1.tar"
      ]' $m
    jq -e --slurpfile md $dir/metadata.json '
      .annotations == $md[0].annotations
      and .annotations == {"org.opencontainers.image.source": "https://github.com/trevex/chalkos"}
    ' $m
    jq -e --slurpfile md $dir/metadata.json '
      .imageId == "chalkos" and .architecture == "x86-64"
      and .rootHash == $md[0].rootHash and .platform == $md[0].platform
    ' "$(blob "$(jq -r '.config.digest' $m)")"
    tar -xOf "$(blob "$(jq -r '.layers[4].digest' $m)")" metadata.json | cmp - $dir/metadata.json
    touch $out
  '';
}
