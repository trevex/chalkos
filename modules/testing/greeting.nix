# Test-only: a config kind and a unit that reads it with chalkcfg, for the install test. A Greeting
# holds a message and a token from a Secret; chalktest-greeting logs the message and the start of
# the token's SHA-256, and chalkd restarts it when the Greeting or its Secret changes.
{
  config,
  lib,
  pkgs,
  chalkosLib,
  ...
}:
let
  greeting = config.chalkos.kinds.Greeting;
in
{
  chalkos.kinds.Greeting = {
    group = "greeting.example.org";
    version = "v1alpha1";
    spec = {
      message = lib.mkOption {
        type = lib.types.nonEmptyStr;
        description = "What the unit logs.";
      };
      token = lib.mkOption {
        type = chalkosLib.types.secretRef;
        description = "A secret the unit reads as a credential.";
      };
    };
  };
  chalkos.consumers.chalktest-greeting.kinds = [ "Greeting" ];
  systemd.services.chalktest-greeting = {
    description = "Log the node's Greeting for chalklab tests";
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
