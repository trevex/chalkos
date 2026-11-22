# The images the e2e tests boot, and their secrets. The tests' clusters are the YAML of
# test/fixtures/clusters, whose inventories name these images' directories.
{ self, pkgs }:
let
  secrets = import ./secrets.nix { inherit pkgs; };
  # Every image has the test image's small partitions and the probe the tests read. test, which
  # most VM tests boot, carries chalklab's Greeting kind; k8s is the image of the Kubernetes
  # tests' control planes and workers, on metal without BGP for the HA test; router that of the
  # router test's routers.
  shared = {
    osCA = "${secrets}/secrets.pub.json";
    modules = [ ../../modules/testing/test-image.nix ];
  };
  modules = {
    test = [ ../../modules/testing/greeting.nix ];
    k8s = [
      self.nixosModules.kubernetes
      ../../modules/testing/module-load.nix
      { chalkos.kubernetes.longhorn.enable = true; }
    ];
    router = [
      self.nixosModules.router
      {
        # The test asks r2's resolver of fabric on edge0's address from within fabric, over a
        # socket bound to the VRF's device, which needs no privilege on a socket not bound
        # before. The query asks for the A records of ipv4only.arpa with the ID 0x1234; any
        # answer shows that the resolver's sockets are bound to the VRF's device.
        chalkos.diagnostics.chalktest-fabric-dns = {
          description = "The first bytes of r2's fabric resolver's answer, asked from within fabric.";
          command = [
            (toString (
              pkgs.writeShellScript "chalktest-fabric-dns" ''
                printf '\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x08ipv4only\x04arpa\x00\x00\x01\x00\x01' |
                  ${pkgs.socat}/bin/socat -t 3 -T 5 - 'UDP6:[2001:db8:ffff::2]:53,so-bindtodevice=fabric' |
                  ${pkgs.coreutils}/bin/od -An -tx1 -N4
              ''
            ))
          ];
          role = "reader";
          timeout = "10s";
        };
      }
    ];
  };
  # The HA test's control planes leave BGP out, so a cluster's lifecycle runs without FRR too.
  withoutBGP = {
    chalkos.kubernetes.bgp.enable = false;
  };
  # The image of modules.<base> on a platform, <base>-<platform>.
  image = base: platform: {
    name = "${base}-${platform}";
    inherit platform;
    modules = modules.${base};
  };
  images = self.lib.mkImages (
    shared
    // {
      images = [
        (image "test" "metal")
        (image "test" "kvm")
        {
          name = "k8s-metal";
          platform = "metal";
          modules = modules.k8s ++ [ withoutBGP ];
        }
        (image "k8s" "kvm")
        (image "router" "kvm")
      ];
    }
  );
  # An image's metal image again, of the same name, with the module given and a file in /etc that
  # names the image, so its store, and with it its root hash, differs from the image and from the
  # others.
  imageAs =
    base: name: extra:
    self.lib.mkImage (
      shared
      // {
        name = "${base}-metal";
        platform = "metal";
        modules =
          shared.modules
          ++ modules.${base}
          ++ [
            { environment.etc."chalktest-image".text = name; }
            extra
          ];
      }
    );
in
images
// {
  inherit secrets;
  # The images e2e-upgrade installs after the test image: the upgrade, which declares a Farewell
  # kind the test image does not, so its config is config the test image refuses, and one that
  # never becomes healthy, as a unit fails at every boot the way a broken service would, and falls
  # back after one try of 30 seconds. The upgrade image's output farewell refuses a Farewell whose
  # message is stay in its check alone, which neither its schema nor chalkctl validate do.
  upgradeImage = imageAs "test" "upgrade" (
    {
      chalkosLib,
      lib,
      pkgs,
      ...
    }:
    {
      chalkos.kinds.Farewell = {
        group = "greeting.example.org";
        version = "v1alpha1";
        spec.message = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "What the node says on leaving.";
        };
      };
      chalkos.outputs.farewell = {
        inputs = [ "Farewell" ];
        render = chalkosLib.render.documents [ "Farewell" ];
        # jq -e fails on a Farewell whose message is stay.
        check = chalkosLib.check.each "Farewell/*.json" [
          (lib.getExe pkgs.jq)
          "-e"
          ''.spec.message!="stay"''
          "$FILE"
        ];
      };
    }
  );
  unhealthyImage = imageAs "test" "unhealthy" {
    chalkos.upgrade = {
      bootTries = 1;
      healthTimeout = 30;
    };
    systemd.services.chalktest-broken = {
      description = "Fail, as a broken service does";
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${pkgs.coreutils}/bin/false";
      };
    };
  };
  # The Kubernetes image the HA test's rolling upgrade installs.
  haUpgradeImage = imageAs "k8s" "upgrade" withoutBGP;
  kubernetesImages = import ./kubernetes-images.nix {
    inherit pkgs;
    inherit (self.lib) kubernetesReleases;
  };
  # A kernel module for the Kubernetes test image's kernel, from outside the image.
  outsideModule = import ./outside-module.nix {
    inherit pkgs;
    inherit (images.k8s-kvm.nixos.config.boot.kernelPackages) kernel;
  };
}
