# NVIDIA GPUs for NVIDIA's GPU operator with the driver and the toolkit on the host: the driver's
# kernel modules, built against the image's kernel and loaded at boot; its GSP firmware;
# containerd's nvidia handler; a unit that writes the CDI specification of the node's GPUs at
# boot; and a driver root at /usr/local/nvidia, the operator's hostPaths.driverInstallDir.
#
# The driver root holds nvidia-smi and the driver's compute, utility and video libraries as files
# of its own, without run paths and with nvidia-smi's interpreter at its FHS path, so they run in
# the operator's containers, which mount the directory and lack the store: the containers bring
# the glibc. The libraries' graphics and display parts, which link X11 and Wayland, are left out.
# On the host, nvidia-ctk finds libnvidia-ml in the driver root itself. The driver is unfree; the
# image's nixpkgs must allow it.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  k = config.chalkos.kubernetes;
  cfg = k.nvidia;
  inherit (pkgs.stdenv.hostPlatform) isx86_64;
  modules = if cfg.kernelModuleType == "open" then cfg.package.open else cfg.package.mod;

  # nixpkgs's own test of an unfree package, so the image refuses what its nixpkgs refuses.
  allowed =
    p:
    pkgs.config.allowUnfree or false
    || builtins.getEnv "NIXPKGS_ALLOW_UNFREE" == "1"
    || (pkgs.config.allowUnfreePredicate or (_: false)) p;
  refused = lib.filter (p: p.meta.unfree or false && !allowed p) [
    cfg.package
    modules
  ];
  message = "chalkos.kubernetes.nvidia needs NVIDIA's unfree packages ${
    lib.concatMapStringsSep " and " lib.getName refused
  }; allow them in the image's modules, for example nixpkgs.config.allowUnfreePredicate = p: lib.elem (lib.getName p) [ ${
    lib.concatMapStringsSep " " (p: ''"${lib.getName p}"'') refused
  } ];";
  # The packages the image carries, which fail the evaluation with the message before nixpkgs's
  # own refusal, whose advice does not fit an image's modules.
  guard = chalkosLib.internal.refuse (lib.optional (refused != [ ]) message);
  driver = guard cfg.package;

  toolkit = pkgs.nvidia-container-toolkit;
  tools = lib.getOutput "tools" toolkit;
  ctk = "${toolkit}/bin/nvidia-ctk";
  driverRootPath = "/usr/local/nvidia";
  # The libraries of the driver capabilities utility, compute and video, by name without lib and
  # .so; a name the driver lacks fails the build.
  libraries = [
    "nvidia-ml"
    "nvidia-cfg"
    "nvidia-sandboxutils"
    "cuda"
    "cudadebugger"
    "nvidia-opencl"
    "nvidia-ptxjitcompiler"
    "nvidia-nvvm"
    "nvidia-nvvm70"
    "nvidia-gpucomp"
    "nvidia-tileiras"
    "nvidia-allocator"
    "nvidia-pkcs11-openssl3"
    "nvcuvid"
    "nvidia-encode"
    "nvidia-opticalflow"
  ];
  fhsInterpreter = if isx86_64 then "/lib64/ld-linux-x86-64.so.2" else "/lib/ld-linux-aarch64.so.1";
  # lib64 is where the operator looks for libnvidia-ml.so.1; lib leads to it.
  driverRoot =
    pkgs.runCommand "nvidia-driver-root-${driver.version}"
      {
        nativeBuildInputs = [
          pkgs.patchelf
          pkgs.nukeReferences
        ];
        allowedReferences = [ ];
      }
      ''
        mkdir -p $out/bin $out/lib64
        for name in ${toString libraries}; do
          set -- ${driver}/lib/lib$name.so*
          [ -e "$1" ] || { echo "error: NVIDIA's driver ${driver.version} has no lib$name" >&2; exit 1; }
          cp -P "$@" $out/lib64/
        done
        cp ${lib.getBin driver}/bin/nvidia-smi $out/bin/
        chmod -R u+w $out
        for f in $out/bin/* $out/lib64/*; do
          [ -L "$f" ] || patchelf --remove-rpath "$f"
        done
        patchelf --set-interpreter ${fhsInterpreter} $out/bin/nvidia-smi
        # The old run paths stay behind as strings, which name the driver and its libraries.
        nuke-refs $out/bin/* $out/lib64/*
        ln -s lib64 $out/lib
      '';
in
{
  options.chalkos.kubernetes =
    lib.recursiveUpdate
      (chalkosLib.internal.feature "Kubernetes" "nvidia" {
        carries = "NVIDIA's GPU driver, loaded at boot, with its firmware, containerd's runtime handler `nvidia`, a CDI specification of the node's GPUs written at boot and the driver's programs and libraries in `/usr/local/nvidia`, for NVIDIA's GPU operator with the driver and the toolkit on the host. The driver is unfree: the image's nixpkgs must allow it";
        default = false;
      })
      {
        nvidia = {
          kernelModuleType = lib.mkOption {
            type = lib.types.enum [
              "open"
              "proprietary"
            ];
            default = "open";
            description = ''
              Which of NVIDIA's kernel modules the image carries: `open`, NVIDIA's open modules,
              which GPUs from Turing on run, or `proprietary`, which GPUs before Turing need.
              Only with `chalkos.kubernetes.nvidia.enable`.
            '';
          };
          package = lib.mkOption {
            type = lib.types.package;
            default = config.boot.kernelPackages.nvidiaPackages.stable;
            defaultText = lib.literalExpression "config.boot.kernelPackages.nvidiaPackages.stable";
            description = ''
              NVIDIA's driver, of the image's kernel packages, whose kernel modules, firmware,
              programs and libraries the image carries. Only with
              `chalkos.kubernetes.nvidia.enable`.
            '';
          };
        };
      };

  config = lib.mkIf (k.enable && cfg.enable) {
    assertions = [
      {
        assertion = refused == [ ];
        inherit message;
      }
    ];

    boot.extraModulePackages = [ (guard modules) ];
    boot.kernelModules = [
      "nvidia"
      "nvidia_uvm"
      "nvidia_modeset"
      "nvidia_drm"
    ];
    # The gpu group carries nouveau too, which would take the GPU first.
    boot.blacklistedKernelModules = [ "nouveau" ];
    chalkos.kernel.moduleGroups = [ "gpu" ];
    # The GSP firmware of the GPUs from Turing on: one file for Turing, one for later GPUs.
    hardware.firmware = [ driver.firmware ];

    chalkos.usr.links."local/nvidia" = driverRoot;

    chalkos.kubernetes.containerd.runtimes.nvidia.options = {
      BinaryName = "${tools}/bin/nvidia-container-runtime";
      SystemdCgroup = true;
    };
    # The runtime adds the devices of the CDI specifications alone and runs runc from the store.
    environment.etc."nvidia-container-runtime/config.toml".source =
      (pkgs.formats.toml { }).generate "nvidia-container-runtime.toml"
        {
          nvidia-container-runtime = {
            mode = "cdi";
            runtimes = [ "${pkgs.runc}/bin/runc" ];
            modes.cdi = {
              default-kind = "nvidia.com/gpu";
              annotation-prefixes = [ "cdi.k8s.io/" ];
              spec-dirs = [
                "/etc/cdi"
                "/var/run/cdi"
              ];
            };
          };
          nvidia-container-runtime-hook.path = "${tools}/bin/nvidia-container-runtime-hook";
          nvidia-ctk.path = ctk;
        };

    # The directory of the specifications units write at boot, which the operator's device plugin
    # writes to as well.
    systemd.tmpfiles.settings.chalkos-cdi."/run/cdi".d.mode = "0755";

    # The control devices first; NVML, which runs as root, makes each GPU's /dev/nvidia<N>.
    systemd.services.chalkos-nvidia-cdi = {
      description = "Write the CDI specification of the node's NVIDIA GPUs";
      wantedBy = [ "multi-user.target" ];
      after = [
        "systemd-modules-load.service"
        "systemd-tmpfiles-setup.service"
      ];
      before = [
        "containerd.service"
        "kubelet.service"
      ];
      # No LD_LIBRARY_PATH: nixpkgs's nvidia-ctk tries dlopen before the driver root and aborts
      # when dlopen finds the library.
      serviceConfig = chalkosLib.internal.noExec // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStartPre = "${ctk} system create-device-nodes --control-devices";
        ExecStart = lib.concatStringsSep " " [
          "${ctk} cdi generate"
          "--output=/var/run/cdi/nvidia.yaml"
          "--driver-root=${driverRootPath}"
          "--ldconfig-path=${lib.getBin pkgs.glibc}/bin/ldconfig"
          "--nvidia-cdi-hook-path=${tools}/bin/nvidia-cdi-hook"
        ];
      };
    };
  };
}
