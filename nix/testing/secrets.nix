# Test-only cluster secrets in the format of `chalkctl gen secrets --plaintext`, from the files of
# test/fixtures/secrets, so every build of them, and of the test images that carry their OS CA,
# is the same. Never use them outside chalklab tests: the keys are public.
{ pkgs }:
pkgs.runCommand "chalkos-test-secrets"
  {
    nativeBuildInputs = [ pkgs.jq ];
    secrets = ../../test/fixtures/secrets/secrets.json;
  }
  ''
    mkdir -p $out
    cp $secrets $out/secrets.json
    jq '{version, osCA: {certificate: .osCA.certificate}, nodeCA: {certificate: .nodeCA.certificate},
      kubernetes: (.kubernetes | {ca: {certificate: .ca.certificate}, frontProxyCA: {certificate: .frontProxyCA.certificate}, etcdCA: {certificate: .etcdCA.certificate}})}' \
      $secrets > $out/secrets.pub.json
  ''
