# Test-only: a config kind and a unit that reads it, for the install test. A Greeting holds a
# message and a token from a Secret; chalktest-greeting logs the message and the start of the
# token's SHA-256, and chalkd restarts it when the Greeting or its Secret changes.
{
  config,
  lib,
  pkgs,
  chalkosLib,
  ...
}:
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
          runtimeInputs = [
            pkgs.coreutils
            pkgs.jq
          ];
          text = ''
            for doc in ${config.chalkos.kinds.Greeting.dir}/*.json; do
              [[ -e $doc ]] || continue
              message=$(jq -r .spec.message "$doc")
              ref=$(jq -r '.spec.token.secretRef | "\(.name).\(.key)"' "$doc")
              token=$(sha256sum < "$CREDENTIALS_DIRECTORY/secret_$ref" | cut -c1-16)
              echo "greeting $message, token $token"
            done
          '';
        }
      );
    };
  };
}
