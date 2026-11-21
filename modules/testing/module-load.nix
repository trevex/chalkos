# Test-only: the host side of e2e-kubernetes's module policy step. A pod cannot load modules or
# touch IPE, so it leaves files below /var/lib/chalktest-modules and these units act on the host:
# chalktest-module-load tries to load the module the pod left there and reports on the first
# serial port whether the kernel refused it and IPE's audit record says so; chalktest-ipe-toggle
# switches IPE to permissive and back, which chalkd must report as tampered.
{ pkgs, ... }:
let
  dir = "/var/lib/chalktest-modules";
  report = ''
    fact() { echo "CHALKTEST $*" | dd of=/dev/ttyS0 oflag=noctty status=none 2>/dev/null || true; }
  '';
in
{
  systemd.tmpfiles.rules = [ "d ${dir} 0755 root root -" ];

  systemd.paths.chalktest-module-load = {
    wantedBy = [ "paths.target" ];
    pathConfig.PathExists = "${dir}/load";
  };
  systemd.services.chalktest-module-load = {
    description = "Load the module a pod left, as host root";
    path = [
      pkgs.kmod
      pkgs.systemd
      pkgs.gnugrep
      pkgs.coreutils
    ];
    serviceConfig.Type = "oneshot";
    script = ''
      ${report}
      rm -f ${dir}/load
      if out=$(insmod ${dir}/outside.ko 2>&1); then
        fact module_outside=loaded
        rmmod chalkos_e2e_outside || true
        exit 0
      fi
      echo "insmod: $out"
      # The kernel logs the denial as an audit record, which reaches the journal after insmod
      # returns: follow the journal until it shows, for at most a minute.
      if timeout 60 journalctl -b -f -n all -q -o cat _TRANSPORT=audit \
          | grep -m1 -F 'path="${dir}/outside.ko"' | grep -q 'rule="DEFAULT op=KMODULE action=DENY"'; then
        ipe=1
      else
        ipe=0
      fi
      case $out in
        *"Permission denied"*) fact module_outside=refused module_outside_ipe=$ipe ;;
        *) fact "module_outside=failed-otherwise module_outside_ipe=$ipe" ;;
      esac
    '';
  };

  systemd.paths.chalktest-ipe-toggle = {
    wantedBy = [ "paths.target" ];
    pathConfig.PathExists = "${dir}/toggle";
  };
  systemd.services.chalktest-ipe-toggle = {
    description = "Switch IPE to permissive and back, as host root";
    path = [ pkgs.coreutils ];
    serviceConfig.Type = "oneshot";
    script = ''
      ${report}
      rm -f ${dir}/toggle
      echo 0 > /sys/kernel/security/ipe/enforce
      echo 1 > /sys/kernel/security/ipe/enforce
      fact ipe_toggled=1
    '';
  };
}
