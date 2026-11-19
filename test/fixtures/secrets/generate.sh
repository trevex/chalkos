#!/usr/bin/env bash
# Writes the test-only secrets of this directory anew: secrets.json, in the format of
# `chalkctl gen secrets --plaintext`, and the Secure Boot keys PK, KEK and db with their
# certificates. Run it from the development shell, which has openssl and jq, to rotate them; the
# test images and VMs then change with them. Every certificate is valid from 2026-01-01 to
# 2126-01-01, so no VM clock sees it outside its validity.
set -euo pipefail
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

validity=(-not_before 20260101000000Z -not_after 21260101000000Z)
key() { openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$work/$1"; }
# ca <name> <common name> <path length>
ca() {
  key "$1.key"
  openssl req -x509 -new -key "$work/$1.key" -sha256 "${validity[@]}" -subj "/CN=$2" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:$3" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" -out "$work/$1.crt"
}

# The OS CA issues the node CA, which issues node certificates for TLS servers and clients only.
ca ca "chalkos OS CA" 1
key node-ca.key
openssl req -new -key "$work/node-ca.key" -subj "/CN=chalkos node CA" -out "$work/node-ca.csr"
printf '%s\n' "basicConstraints=critical,CA:TRUE,pathlen:0" "keyUsage=critical,keyCertSign,cRLSign" \
  "extendedKeyUsage=serverAuth,clientAuth" "subjectKeyIdentifier=hash" "authorityKeyIdentifier=keyid" \
  >"$work/node-ca.ext"
openssl x509 -req -in "$work/node-ca.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -set_serial 2 \
  -sha256 "${validity[@]}" -extfile "$work/node-ca.ext" -out "$work/node-ca.crt"

ca kubernetes "chalkos Kubernetes CA" 0
ca front-proxy "chalkos front-proxy CA" 0
ca etcd "chalkos etcd CA" 0
key sa.key

jq -n \
  --rawfile caCert "$work/ca.crt" --rawfile caKey "$work/ca.key" \
  --rawfile nodeCACert "$work/node-ca.crt" --rawfile nodeCAKey "$work/node-ca.key" \
  --arg recovery "$(openssl rand -base64 32)" \
  --rawfile k8sCert "$work/kubernetes.crt" --rawfile k8sKey "$work/kubernetes.key" \
  --rawfile frontCert "$work/front-proxy.crt" --rawfile frontKey "$work/front-proxy.key" \
  --rawfile etcdCert "$work/etcd.crt" --rawfile etcdKey "$work/etcd.key" \
  --rawfile sa "$work/sa.key" \
  --arg encryption "$(openssl rand -base64 32)" \
  '{version: 3, osCA: {certificate: $caCert, key: $caKey}, nodeCA: {certificate: $nodeCACert, key: $nodeCAKey}, recoverySecret: $recovery,
    kubernetes: {ca: {certificate: $k8sCert, key: $k8sKey}, frontProxyCA: {certificate: $frontCert, key: $frontKey},
      etcdCA: {certificate: $etcdCert, key: $etcdKey}, serviceAccountKey: $sa, encryptionKey: $encryption, encryptionKeyName: "chalkos"}}' \
  >secrets.json

# The Secure Boot keys, RSA 2048 as firmware takes them.
mkdir -p secureboot
for k in PK KEK db; do
  openssl req -new -x509 -newkey rsa:2048 -nodes -sha256 "${validity[@]}" \
    -subj "/CN=chalkos test $k/" -keyout "secureboot/$k.key" -out "secureboot/$k.crt"
done
chmod 644 secureboot/*.key
