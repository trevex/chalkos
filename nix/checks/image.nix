# Checks of what an image build produces, without booting it.
{
  pkgs,
  self,
  checkLib,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (checkLib) chalkPkgs testing worker;
in
{
  # The worker's kernel module tree, and one with the gpu group: a module outside an image's groups
  # is not in its tree, and neither a name the kernel does not know, a directory without modules,
  # a module the image loads but its tree lacks, nor one whose dependency the tree lacks gets past
  # the build. Out-of-tree modules bring the in-tree modules they depend on.
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
      v4l2loopbackCheck =
        lib.findFirst (d: d.name == "kernel-modules-check")
          (throw "the image with v4l2loopback runs no kernel-modules-check")
          v4l2loopback.config.system.checks;
      tool = lib.getExe (pkgs.callPackage ../kernel-modules.nix { });
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
        fail() {
          echo "error: $*" >&2
          exit 1
        }
        has() {
          modprobe --config no-config -d "$1" -S ${version} --show-depends "$2" >/dev/null 2>&1
        }
        for module in virtio_net i2c_i801 i2c_dev lpc_ich vmw_balloon ptp_vmw ceph cifs rpcsec_gss_krb5; do
          has ${tree} $module || fail "the worker's tree lacks $module"
        done
        if has ${tree} amdgpu; then fail "the worker's tree holds amdgpu"; fi
        has ${gpu} amdgpu || fail "the gpu group's tree lacks amdgpu"

        echo drivers/nvme >directories
        echo chalkos_no_such_module >names
        if ${tool} filter ${full} unknown directories names 2>errors; then fail "an unknown module was taken"; fi
        grep -q 'unknown kernel module chalkos_no_such_module' errors || fail "the error names no module: $(cat errors)"
        echo drivers/chalkos_no_such_directory >directories
        : >names
        if ${tool} filter ${full} empty directories names 2>errors; then fail "a directory without modules was taken"; fi
        grep -q 'no kernel module below drivers/chalkos_no_such_directory' errors || fail "the error names no directory: $(cat errors)"

        echo amdgpu >loaded
        if ${tool} check ${tree} loaded 2>errors; then fail "a module outside the tree passed the check"; fi
        grep -q 'does not hold: amdgpu' errors || fail "the error names no module: $(cat errors)"

        has ${treeOf v4l2loopback} videodev || fail "the tree of an image with v4l2loopback lacks videodev"
        echo drivers/nvme >directories
        ${tool} filter ${full} "$PWD/stubbed" directories names ${stubWithDependencies}
        has stubbed videodev || fail "the dependency of an out-of-tree module is missing"
        has stubbed uvcvideo || fail "the soft dependency of an out-of-tree module is missing"
        if ${tool} filter ${full} missing directories names ${stubWithMissingDependency} 2>errors; then
          fail "an out-of-tree module whose dependency the kernel lacks was taken"
        fi
        grep -q 'chalkos_stub_missing depends on chalkos_no_such_module' errors || fail "the error names no modules: $(cat errors)"
        : >loaded
        if ${tool} check ${stubWithDependencies} loaded 2>errors; then fail "a module whose dependency the tree lacks passed the check"; fi
        grep -q 'chalkos_stub depends on videodev' errors || fail "the error names no modules: $(cat errors)"
        grep -q 'to chalkos.kernel.extraModules' errors || fail "the error does not say how to fix it: $(cat errors)"
        touch $out
      '';

  # The test images on each platform and the installer within their ceilings
  # (testing/image-sizes.nix). An image over one fails the check with the largest paths of its
  # system's closure, read from closureInfo, as the build has no Nix daemon to ask. The images' own
  # fit check is tried on the test image too, and its measure of each image agrees with package
  # verity, which an upgrade reads the sizes with.
  image-size =
    let
      veritysize = pkgs.callPackage ../go-module.nix { } {
        pname = "veritysize";
        paths = [
          ../../test/veritysize
          ../../pkg/verity
        ];
        subPackages = [ "test/veritysize" ];
        # The go-unit check runs package verity's tests.
        doCheck = false;
      };
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
      images = lib.concatLists (
        lib.mapAttrsToList (
          variant: platforms:
          lib.mapAttrsToList (
            platform: measure "${variant} on ${platform}" testing.nixos."${variant}-${platform}".config
          ) platforms
        ) ceilings.variants
      );
      test = testing.nixos.test-metal.config;
      testImage = "${test.system.build.image}/${test.image.fileName}";
      testPartitions = "${test.system.build.image}/repart-output.json";
      testUKI = "${test.system.build.uki}/${test.system.boot.loader.ukiFile}";
    in
    pkgs.runCommand "chalkos-image-size"
      {
        nativeBuildInputs = [
          (pkgs.callPackage ../image-size.nix { })
          veritysize
        ];
      }
      ''
        mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }
        failed=0
        check() {
          local name=$1 raw=$2 partitions=$3 uki=$4 closure=$5 sizes verity data hash over=()
          local -A ceiling=([storeData]=$6 [hashTree]=$7 [uki]=$8)
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
        check ${measure "installer" testing.nixos.installer.config ceilings.installer}

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

  # An image builds only when its rules compile and read the facts it declares, and its effects
  # act on units it defines. The test image's Greeting and the homelab example's Rack, Hardware and
  # Peer bring extension kinds with their own rules, facts, effects and outputs; a rule that does
  # not compile, one that reads a fact no module declares, an effect's rule that does not compile,
  # an effect on a unit the image lacks and an output whose check refuses an example fail the check
  # of their image. The homelab's Hardware output renders hardware-examples/server.yaml, and its
  # bird-conf output renders bgp-examples/two-peers.yaml, which bird -p parses.
  rules =
    let
      build =
        modules:
        (self.lib.mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config.system.build;
      homelab = build [
        ../../examples/homelab/extensions/rack.nix
        ../../examples/homelab/extensions/hardware.nix
        ../../examples/homelab/extensions/bgp.nix
      ];
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
      # An output whose check refuses the file one of its examples renders.
      refusedExample = build [
        (
          { pkgs, lib, ... }:
          {
            chalkos.kinds.Demo = {
              group = "demo.example.org";
              version = "v1alpha1";
              spec.name = lib.mkOption { type = lib.types.str; };
            };
            chalkos.outputs.demo = {
              inputs = [ "Demo" ];
              render = lib.chalkos.render.jq (pkgs.writeText "name.jq" ".documents.Demo[].spec.name");
              check = ''grep -qx first "$OUT/name"'';
              examples = pkgs.writeTextDir "second.yaml" ''
                apiVersion: demo.example.org/v1alpha1
                kind: Demo
                metadata: {name: demo}
                spec: {name: second}
              '';
            };
          }
        )
      ];
    in
    pkgs.runCommand "chalkos-rules"
      {
        nativeBuildInputs = [
          (pkgs.callPackage ../chalkos-rules.nix { })
          pkgs.jq
        ];
        checked = [
          testing.nixos.test-metal.config.system.build.chalkosRulesCheck
          homelab.chalkosRulesCheck
        ];
      }
      ''
        chalkos-rules check ${homelab.chalkosRulesImage} | grep -q rackSurvey
        # refused <image> <message>: the check of the image's files fails, and says why.
        refused() {
          if chalkos-rules check "$1" 2>err; then
            echo "error: chalkos-rules accepted $1" >&2
            exit 1
          fi
          if ! grep -qF "$2" err; then
            echo "error: chalkos-rules refused $1 without $2:" >&2
            cat err >&2
            exit 1
          fi
        }
        refused ${broken.chalkosRulesImage} 'Demo: rule "documents ==": ERROR: <input>:1:13: Syntax error'
        refused ${undeclared.chalkosRulesImage} 'Demo: rule "facts.missing == null" reads facts.missing, which the image does not declare'
        refused ${brokenEffect.chalkosRulesImage} 'effect bird: rule "config ==": ERROR: <input>:1:10: Syntax error'
        refused ${undefinedUnit.chalkosRulesImage} 'effect bird: the image defines no unit bird.service'
        refused ${refusedExample.chalkosRulesImage} 'output demo: example second.yaml: its check refused the rendered files (exit 1)'
        # The check of chrony's output passes the servers its template renders, and refuses a
        # line chronyd does not parse or a sourcedir file does not take.
        check=$(jq -r .chrony.check ${testing.nixos.test-metal.config.system.build.chalkosOutputs})
        mkdir chrony
        printf 'server a.example iburst nts\nserver 10.0.0.1 iburst port 12300\n' >chrony/servers.sources
        OUT=$PWD/chrony $check </dev/null
        for bad in 'server a.example iburst bogus' 'pool a.example iburst' 'server a.example iburst nts maxpoll'; do
          printf '%s\n' "$bad" >chrony/servers.sources
          if OUT=$PWD/chrony $check </dev/null 2>err; then
            echo "error: chrony's check accepted $bad" >&2
            exit 1
          fi
        done
        touch $out
      '';

  # An image directory describes itself and carries schemas that accept a real Network document
  # and reject a misspelled key.
  image-contract =
    pkgs.runCommand "chalkos-image-contract"
      {
        nativeBuildInputs = [
          pkgs.jq
          pkgs.check-jsonschema
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
          and .variant == "test"
          and .platform == "metal"
          and (.rootHash | test("^[0-9a-f]{64}$"))
          and ([.kinds[].kind] | index("Network") != null)
          and .kubernetes == null
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
        if [ -e $dir/image.json ]; then
          echo "error: the test image runs no Kubernetes, but its directory has image.json" >&2
          exit 1
        fi
        k=${chalkPkgs.test-kubernetes-image}
        jq -e '.cni == "flannel" and (. as $i | $i.images.kubeProxy | endswith(":v" + $i.version))' $k/image.json
        jq -e --slurpfile img $k/image.json \
          '.kubernetes == {version: $img[0].version, cni: $img[0].cni}' $k/metadata.json
        jq -e '.variant == "k8s"' $k/metadata.json

        schema=$dir/schemas/networkd.chalkos.dev_v1alpha1_Network.json
        cat > good.json <<'EOF'
        {
          "apiVersion": "networkd.chalkos.dev/v1alpha1",
          "kind": "Network",
          "metadata": { "name": "10-uplink" },
          "spec": {
            "match": { "name": "enp1s0" },
            "network": { "address": ["10.0.0.11/24"], "gateway": "10.0.0.1", "ipv6AcceptRA": false },
            "route": [ { "destination": "10.1.0.0/16", "gateway": "10.0.0.254" } ]
          }
        }
        EOF
        check-jsonschema --schemafile $schema good.json
        jq '.spec.network.gatway = "10.0.0.1"' good.json > typo.json
        if check-jsonschema --schemafile $schema typo.json; then
          echo "error: the Network schema accepted the misspelled key gatway" >&2
          exit 1
        fi
        touch $out
      '';

  # The test image's OCI layout: one manifest of the chalkos artifact type, whose header names the
  # image directory's root hash and platform, whose layers are the image's parts and contract in
  # order, and whose contract holds the directory's metadata.json.
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
      .imageId == "chalkos" and .architecture == "x86-64"
      and .rootHash == $md[0].rootHash and .platform == $md[0].platform
    ' "$(blob "$(jq -r '.config.digest' $m)")"
    tar -xOf "$(blob "$(jq -r '.layers[4].digest' $m)")" metadata.json | cmp - $dir/metadata.json
    touch $out
  '';
}
