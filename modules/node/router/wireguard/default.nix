# WireGuard on router images whose chalkos.router.wireguard.enable is true: the output networkd
# renders a .netdev and a .network per WireGuard document beside the VRFs' units, and
# systemd-networkd creates the interfaces. The .netdev names the private and preshared keys as
# networkd's credentials, which chalkd loads from the Secrets the documents name, so no rendered
# file holds a key. The diagnostic wireguard shows the interfaces as wg show prints them.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.wireguard;
  kinds = routerLib.kinds ./kinds.nix;
in
{
  options.chalkos.router = routerLib.feature "wireguard" ''
    Whether router images accept the kinds WireGuard and WireGuardPeer, whose interfaces
    systemd-networkd runs, and run the diagnostic wireguard with wireguard-tools; only with the
    router layer. Set it false in a variant's modules to leave WireGuard out of its images.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # The router layer renders the interfaces' units into networkd's output, whose effect reloads
    # networkd.
    chalkos.outputs.networkd.inputs = lib.attrNames kinds;

    # chalkd loads the keys the WireGuard documents name as systemd-networkd's credentials, which
    # a .netdev reads as PrivateKey=@secret_<secret>.<key>.
    systemd.services.systemd-networkd = {
      chalkos.kinds = lib.attrNames kinds;
      serviceConfig = {
        # A reload loads the credentials again, so a rotated key reaches the interface without a
        # restart: networkd applies a WireGuard's keys and peers anew on each reload.
        RefreshOnReload = "credentials";
      };
    };

    # wg reads an interface's state over netlink, which takes CAP_NET_ADMIN. It prints the public
    # keys, and (hidden) for the private and preshared keys.
    chalkos.diagnostics.wireguard = {
      description = "The router's WireGuard interfaces and their peers, as wg show prints them: public keys, endpoints, allowed IPs, the latest handshakes and the bytes sent and received. It shows no private or preshared key.";
      command = [
        "${pkgs.wireguard-tools}/bin/wg"
        "show"
        "{interface}"
      ];
      # A WireGuard document's name, as the kinds take it.
      parameters.interface = {
        type = "pattern";
        regex = "^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$";
        optional = true;
        description = "Name of a WireGuard document, whose interface alone is shown; every interface when left out.";
      };
      role = "reader";
      timeout = "10s";
      capabilities = [ "CAP_NET_ADMIN" ];
    };
  };
}
