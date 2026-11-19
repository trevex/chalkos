# Test-only Secure Boot keys, from test/fixtures/secrets/secureboot, plus an OVMF variable store
# with them enrolled. Never use these keys outside chalklab tests: they are public.
{ pkgs }:
pkgs.runCommand "chalkos-test-secureboot"
  {
    nativeBuildInputs = [ pkgs.python3Packages.virt-firmware ];
    keys = ../../test/fixtures/secrets/secureboot;
  }
  ''
    mkdir -p $out
    cp $keys/{PK,KEK,db}.{key,crt} $out/

    owner=5f0e5ea2-6c1b-4e8e-9d33-1c1f2b0d7a11
    # The owner of the entries chalkctl secureboot enroll writes, as secureboot new keeps it.
    echo $owner > $out/owner.txt
    virt-fw-vars \
      --input ${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd \
      --output $out/OVMF_VARS.enrolled.fd \
      --set-pk $owner $out/PK.crt \
      --add-kek $owner $out/KEK.crt \
      --add-db $owner $out/db.crt \
      --secure-boot
    test -s $out/OVMF_VARS.enrolled.fd
  ''
