# protoc-gen-connect-go of connect-go v2, which buf generate runs for the Connect code of
# pkg/api. nixpkgs's plugin is of connect-go v1 and generates code for the v1 library. The
# generated code must suit the connectrpc.com/connect/v2 of go.mod, so the version here moves with
# it: set version, then hash and vendorHash from the errors of the next build.
{
  buildGoModule,
  fetchFromGitHub,
  go,
  lib,
}:
(buildGoModule.override { inherit go; }) (finalAttrs: {
  pname = "protoc-gen-connect-go";
  version = "2.0.2";
  src = fetchFromGitHub {
    owner = "connectrpc";
    repo = "connect-go";
    tag = "v${finalAttrs.version}";
    hash = "sha256-iZfWjbMOE2bWo6IbhwzfSYJCtm5yit/wMLKCY05gs4Q=";
  };
  vendorHash = "sha256-0XmH9V7Bbuzj//87Ev6KRy6ijsgh7K6JRhhM5WY7K38=";
  subPackages = [ "cmd/protoc-gen-connect-go" ];
  # The tests build and run the connect-go module's own generated code.
  doCheck = false;
  meta = {
    description = "Connect code generator for Go, of connect-go v2";
    homepage = "https://github.com/connectrpc/connect-go";
    license = lib.licenses.asl20;
    mainProgram = "protoc-gen-connect-go";
  };
})
