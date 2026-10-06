# Test-only cluster secrets in the format of `chalkctl gen secrets --plaintext`. Built with
# openssl rather than chalkctl, so the test images, which carry the OS CA, do not depend on the
# Go sources. Never use them outside chalklab tests: the keys are world-readable in the store.
{ pkgs }:
pkgs.runCommand "chalkos-test-secrets"
  {
    nativeBuildInputs = [
      pkgs.openssl
      pkgs.jq
    ];
  }
  ''
    mkdir -p $out
    # The derivation is built once and reused, so its certificates must not expire under it.
    days=36500
    key() { openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$1"; }

    key ca.key
    openssl req -x509 -new -key ca.key -sha256 -days "$days" -subj "/CN=chalkos OS CA" \
      -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
      -addext "keyUsage=critical,keyCertSign,cRLSign" -out ca.crt

    key admin.key
    openssl req -new -key admin.key -subj "/O=admin/CN=admin" -out admin.csr
    printf 'keyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n' > admin.ext
    openssl x509 -req -in admin.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days "$days" \
      -extfile admin.ext -out admin.crt

    jq -n \
      --rawfile caCert ca.crt --rawfile caKey ca.key \
      --rawfile adminCert admin.crt --rawfile adminKey admin.key \
      --arg recovery "$(openssl rand -base64 32)" \
      '{version: 1, osCA: {certificate: $caCert, key: $caKey}, admin: {certificate: $adminCert, key: $adminKey}, recoverySecret: $recovery}' \
      > $out/secrets.json
    jq '{version, osCA: {certificate: .osCA.certificate}}' $out/secrets.json > $out/secrets.pub.json
  ''
