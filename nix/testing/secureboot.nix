# Test-only Secure Boot keys plus an OVMF variable store with them enrolled.
# Never use these keys outside chalklab tests: the private keys are world-readable in the store.
{ pkgs }:
pkgs.runCommand "chalkos-test-secureboot"
  {
    nativeBuildInputs = [
      pkgs.openssl
      pkgs.python3Packages.virt-firmware
    ];
  }
  ''
    mkdir -p $out
    for k in PK KEK db; do
      openssl req -new -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
        -subj "/CN=chalkos test $k/" -keyout $out/$k.key -out $out/$k.crt
    done

    owner=5f0e5ea2-6c1b-4e8e-9d33-1c1f2b0d7a11
    virt-fw-vars \
      --input ${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd \
      --output $out/OVMF_VARS.enrolled.fd \
      --set-pk $owner $out/PK.crt \
      --add-kek $owner $out/KEK.crt \
      --add-db $owner $out/db.crt \
      --secure-boot
    test -s $out/OVMF_VARS.enrolled.fd
  ''
