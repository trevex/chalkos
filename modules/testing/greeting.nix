# Test-only: a config kind and a unit that reads it with chalkcfg, for the install test. A Greeting
# holds a message and a token from a Secret; chalktest-greeting logs the message and the start of
# the token's SHA-256, and chalkd restarts it when the Greeting or its Secret changes. Its rules,
# which the image's rules check compiles, refuse a message too long for one log line, and a new
# message while the fact greetingLock, a file no test writes, holds one; while it does, a change of
# the token's Secret does not restart the unit either.
{
  config,
  lib,
  pkgs,
  chalkosLib,
  ...
}:
let
  greeting = config.chalkos.kinds.Greeting;
  cel = import ../node/runtime/cel.nix { inherit lib; };
in
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
      message =
        lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "What the unit logs.";
        }
        // {
          "x-chalkos-rules" = [
            {
              rule = "facts.greetingLock == null || self == oldSelf";
              messageExpression = ''"the message is locked to " + facts.greetingLock.message + " while /run/chalktest-greeting-lock.json exists"'';
            }
          ];
        };
      token = lib.mkOption {
        type = chalkosLib.types.secretRef;
        description = "A secret the unit reads as a credential.";
      };
    };
  };
  chalkos.facts.greetingLock.file = "/run/chalktest-greeting-lock.json";
  chalkos.effects.chalktest-greeting = {
    when = "${cel.specsChanged "greeting.example.org/v1alpha1" "Greeting"} || ${cel.specs "config" "chalkos.dev/v1alpha1" "Secret"} != ${cel.specs "oldConfig" "chalkos.dev/v1alpha1" "Secret"}";
    restart = [ "chalktest-greeting.service" ];
  };
  systemd.services.chalktest-greeting = {
    description = "Log the node's Greeting for chalklab tests";
    chalkos.kinds = [ "Greeting" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      # Active after it ran, so chalkd's try-restart runs it again.
      RemainAfterExit = true;
      ExecStart = lib.getExe (
        pkgs.writeShellApplication {
          name = "chalktest-greeting";
          runtimeInputs = [ pkgs.coreutils ];
          # A node has at most one Greeting. Without one, chalkcfg prints nothing and succeeds.
          text = ''
            message=$(${greeting.get { path = "spec.message"; }})
            [[ -n $message ]] || exit 0
            secret=$(${greeting.get { path = "spec.token.secretRef.name"; }})
            key=$(${greeting.get { path = "spec.token.secretRef.key"; }})
            token=$(sha256sum < "$CREDENTIALS_DIRECTORY/secret_$secret.$key" | cut -c1-16)
            echo "greeting $message, token $token"
          '';
        }
      );
    };
  };
}
