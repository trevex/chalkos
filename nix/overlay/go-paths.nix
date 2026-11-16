# The source paths of the Go programs built from the whole tree, chalkctl, chalklab and the e2e
# tests, and of the go-unit and go-lint checks, besides go.mod and go.sum.
{ lib }:
[
  ../../cmd
  ../../pkg
  ../../test/e2e
  ../../test/fixtures
  # The proto files, whose RPC comments chalkd's tests hold to the permissions tables.
  (lib.fileset.fileFilter (f: f.hasExt "proto") ../../api/chalkos)
  # The lab template's YAML, which a chalklab test reads.
  (lib.fileset.fileFilter (f: f.hasExt "yaml") ../../templates/lab)
  # The router outputs' examples, which chalkos-render's golden test renders.
  ../../modules/node/examples/router
  # The layers' rendered frr.conf files, which pkg/frr's tests reload from one to another.
  (lib.fileset.fileFilter (f: f.name == "frr.conf") ../../modules/node/router/tests)
  (lib.fileset.fileFilter (f: f.name == "frr.conf") ../../modules/node/kubernetes/bgp/tests)
]
