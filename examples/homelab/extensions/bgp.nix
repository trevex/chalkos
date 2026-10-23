# BGP sessions with bird, the running example of the image guides. The Peer kind holds a node's
# neighbours, the output bird-conf renders bird's config from them, and bird reloads when that
# config changes. A Peer's password comes from a Secret: bird gets it as a credential, and the
# rendered config names the credential, never the value. A variant that carries this module
# accepts Peer documents. The comments # --8<-- mark the sections the guides include.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  bird = config.services.bird.package;
  # bird reads no password from a file, and its strings take no escapes: this writes each of the
  # unit's credentials as a constant of bird's config, named as the credential, which bird.conf's
  # password lines name. A name outside letters, digits, '.', '_' and '-', or a value with a quote
  # or a line break, fails the unit. The constants go to a new file that replaces the old one, so
  # bird never reads a file half written or emptied by a failed run.
  credentials = pkgs.writeShellApplication {
    name = "bird-credentials";
    text = ''
      shopt -s nullglob
      umask 077
      tmp=$(mktemp /run/bird/credentials.conf.XXXXXX)
      trap 'rm -f "$tmp"' EXIT
      for file in "''${CREDENTIALS_DIRECTORY-}"/secret_*; do
        name=''${file##*/}
        if [[ ! ''${name#secret_} =~ ^[A-Za-z0-9._-]+$ ]]; then
          echo "$name: bird takes a credential name of letters, digits, '.', '_' and '-' alone" >&2
          exit 1
        fi
        value=$(<"$file")
        if [[ $value == *[$'"\n']* ]]; then
          echo "$name: bird takes no password with a quote or a line break" >&2
          exit 1
        fi
        printf "define '%s' = \"%s\";\n" "$name" "$value"
      done >"$tmp"
      mv "$tmp" /run/bird/credentials.conf
    '';
  };
in
{
  # --8<-- [start:kind]
  chalkos.kinds.Peer = {
    group = "bgp.example.org";
    version = "v1alpha1";
    multiple = true;
    spec = {
      # The address lands in bird.conf, so the pattern accepts an IPv4 or IPv6 address and
      # nothing that could end bird's statement.
      address = lib.mkOption {
        type = lib.types.strMatching "([0-9]{1,3}\\.){3}[0-9]{1,3}|[0-9A-Fa-f]{0,4}(:[0-9A-Fa-f]{0,4}){2,7}";
        example = "10.0.0.1";
        description = "IPv4 or IPv6 address of the neighbour.";
      };
      # --8<-- [start:rule]
      # A neighbour's AS number is part of who it is: a new one is a new Peer.
      asn = lib.chalkos.mkOption {
        type = lib.types.ints.between 1 4294967295;
        example = 64513;
        description = "AS number of the neighbour.";
        rules = [
          {
            rule = "self == oldSelf";
            messageExpression = ''"the AS number stays " + string(oldSelf) + "; add a Peer of another name for " + string(self)'';
          }
        ];
      };
      # --8<-- [end:rule]
      password = lib.mkOption {
        type = lib.types.nullOr lib.chalkos.types.secretRef;
        default = null;
        example = {
          secretRef = {
            name = "bgp";
            key = "password";
          };
        };
        description = "Key of a Secret that holds the session's TCP MD5 password; null for none.";
      };
    };
  };
  # --8<-- [end:kind]

  # --8<-- [start:grant]
  # chalkd loads the values the Peers' password fields name as bird's credentials.
  systemd.services.bird.chalkos.kinds = [ "Peer" ];
  # --8<-- [end:grant]

  # --8<-- [start:output]
  # bird.conf, with a protocol per Peer. bird -p parses a config without running it; the
  # constants the password lines name exist on the node alone, so the check defines each first.
  chalkos.outputs.bird-conf = {
    inputs = [ "Peer" ];
    render = lib.chalkos.render.template ./bird.conf.tmpl;
    check = ''
      conf=$(mktemp)
      grep -o "'secret_[^']*'" "$OUT/bird.conf" | sort -u | sed 's/.*/define & = "checked";/' >"$conf"
      echo "include \"$OUT/bird.conf\";" >>"$conf"
      ${bird}/bin/bird -p -c "$conf"
    '';
    examples = ./bgp-examples;
  };
  # --8<-- [end:output]

  # --8<-- [start:service]
  services.bird = {
    enable = true;
    # The node's constants, then the rendered config. Neither exists when the image is built, so
    # bird-conf's check parses the config in place of the module's.
    config = ''
      include "/run/bird/credentials.conf";
      include "${config.chalkos.outputs.bird-conf.dir}/bird.conf";
    '';
    checkConfig = false;
  };
  systemd.services.bird.serviceConfig = {
    ExecStartPre = lib.getExe credentials;
    # A reload loads the credentials again and writes the constants anew, so a rotated password
    # reaches bird without a restart.
    RefreshOnReload = "credentials";
    ExecReload = lib.mkForce [
      (lib.getExe credentials)
      "${bird}/bin/birdc configure"
    ];
  };
  # --8<-- [end:service]

  # --8<-- [start:effect]
  # bird reads its config again on birdc configure, which keeps the sessions a restart would drop.
  chalkos.effects.bird = lib.chalkos.reloadOn { outputs = [ "bird-conf" ]; } [ "bird.service" ];
  # --8<-- [end:effect]
}
