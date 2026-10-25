# Test-only: a config kind, an output rendered from it and a unit that reads both, for the install
# test. A Greeting holds a message and a token from a Secret; the output greeting, rendered from a
# template, holds the message and the name of the token's credential, and chalktest-greeting,
# granted the kind for the credential, logs the message and the start of the token's SHA-256.
# chalkd restarts the unit when the output changes, the token's Secret among it. The Greeting's
# rules, which the image's rules check compiles, refuse a message too long for one log line, and
# a new message while the fact greetingLock, a file no test writes, holds one; the output's check
# refuses a message of more than one line, fails outside an unprivileged sandbox and checks an
# nftables table in a user and network namespace of its own, and the build renders its examples.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kinds.Greeting = {
    group = "greeting.example.org";
    version = "v1alpha1";
    rules = [
      {
        rule = "documents.all(n, size(documents[n].spec.message) <= 120)";
        message = "a Greeting's message fits one log line: at most 120 characters";
      }
    ];
    spec = {
      message = lib.chalkos.mkOption {
        type = lib.types.nonEmptyStr;
        description = "What the unit logs.";
        rules = [
          {
            rule = "facts.greetingLock == null || self == oldSelf";
            messageExpression = ''"the message is locked to " + facts.greetingLock.message + " while /run/chalktest-greeting-lock.json exists"'';
          }
        ];
      };
      token = lib.mkOption {
        type = lib.chalkos.types.secretRef;
        description = "A secret the unit reads as a credential.";
      };
    };
  };
  chalkos.facts.greetingLock.file = "/run/chalktest-greeting-lock.json";
  # A node has at most one Greeting: the file holds its message and its token's credential, a
  # line each, and nothing without one.
  chalkos.outputs.greeting = {
    inputs = [ "Greeting" ];
    render = lib.chalkos.render.template ./greeting.tmpl;
    check = ''
      lines=$(wc -l < "$OUT/greeting")
      [ "$lines" -eq 0 ] || [ "$lines" -eq 2 ]
      # The sandbox: no root, no capabilities and no STATE.
      [ "$(id -u)" -ne 0 ]
      grep -q '^CapEff:[[:space:]]*0*$' /proc/self/status
      [ ! -e /state/chalkd ]
      # A check may talk netlink in a user and network namespace of its own, as the router's
      # nft --check does.
      echo 'table inet greeting { }' | ${pkgs.util-linux}/bin/unshare --map-root-user --net ${pkgs.nftables}/bin/nft --check --file -
    '';
    examples = ./greeting-examples;
  };
  chalkos.effects.chalktest-greeting = lib.chalkos.restartOn { outputs = [ "greeting" ]; } [
    "chalktest-greeting.service"
  ];
  systemd.services.chalktest-greeting = {
    description = "Log the node's Greeting for chalklab tests";
    wantedBy = [ "multi-user.target" ];
    # The token's credential.
    chalkos.kinds = [ "Greeting" ];
    serviceConfig = {
      Type = "oneshot";
      # Active after it ran, so chalkd's try-restart runs it again.
      RemainAfterExit = true;
      ExecStart = lib.getExe (
        pkgs.writeShellApplication {
          name = "chalktest-greeting";
          runtimeInputs = [ pkgs.coreutils ];
          text = ''
            file=${config.chalkos.outputs.greeting.dir}/greeting
            [[ -s $file ]] || exit 0
            { read -r message; read -r credential; } < "$file"
            token=$(sha256sum < "$CREDENTIALS_DIRECTORY/$credential" | cut -c1-16)
            echo "greeting $message, token $token"
          '';
        }
      );
    };
  };
}
