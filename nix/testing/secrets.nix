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
    # ca <name> <common name> <path length>
    ca() {
      key "$1.key"
      openssl req -x509 -new -key "$1.key" -sha256 -days "$days" -subj "/CN=$2" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:$3" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" -out "$1.crt"
    }

    # The OS CA issues the node CA, which issues node certificates for TLS servers and clients
    # only.
    ca ca "chalkos OS CA" 1
    key node-ca.key
    openssl req -new -key node-ca.key -subj "/CN=chalkos node CA" -out node-ca.csr
    printf '%s\n' "basicConstraints=critical,CA:TRUE,pathlen:0" "keyUsage=critical,keyCertSign,cRLSign" \
      "extendedKeyUsage=serverAuth,clientAuth" "subjectKeyIdentifier=hash" "authorityKeyIdentifier=keyid" > node-ca.ext
    openssl x509 -req -in node-ca.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days "$days" \
      -extfile node-ca.ext -out node-ca.crt

    ca kubernetes "chalkos Kubernetes CA" 0
    ca front-proxy "chalkos front-proxy CA" 0
    ca etcd "chalkos etcd CA" 0
    key sa.key

    jq -n \
      --rawfile caCert ca.crt --rawfile caKey ca.key \
      --rawfile nodeCACert node-ca.crt --rawfile nodeCAKey node-ca.key \
      --arg recovery "$(openssl rand -base64 32)" \
      --rawfile k8sCert kubernetes.crt --rawfile k8sKey kubernetes.key \
      --rawfile frontCert front-proxy.crt --rawfile frontKey front-proxy.key \
      --rawfile etcdCert etcd.crt --rawfile etcdKey etcd.key \
      --rawfile sa sa.key \
      --arg encryption "$(openssl rand -base64 32)" \
      '{version: 3, osCA: {certificate: $caCert, key: $caKey}, nodeCA: {certificate: $nodeCACert, key: $nodeCAKey}, recoverySecret: $recovery,
        kubernetes: {ca: {certificate: $k8sCert, key: $k8sKey}, frontProxyCA: {certificate: $frontCert, key: $frontKey},
          etcdCA: {certificate: $etcdCert, key: $etcdKey}, serviceAccountKey: $sa, encryptionKey: $encryption}}' \
      > $out/secrets.json
    jq '{version, osCA: {certificate: .osCA.certificate}, nodeCA: {certificate: .nodeCA.certificate},
      kubernetes: (.kubernetes | {ca: {certificate: .ca.certificate}, frontProxyCA: {certificate: .frontProxyCA.certificate}, etcdCA: {certificate: .etcdCA.certificate}})}' \
      $out/secrets.json > $out/secrets.pub.json
  ''
