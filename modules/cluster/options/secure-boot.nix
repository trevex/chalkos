{ lib, ... }:
{
  options.chalkos.secureBoot = {
    enrollment = lib.mkOption {
      type = lib.types.enum [
        "append"
        "strict"
        "none"
      ];
      default = "append";
      description = ''
        How installation writes Secure Boot variables: `append` keeps existing db and dbx entries
        and adds the cluster certificates, `strict` keeps only the cluster certificate plus
        option ROM hashes, `none` never writes variables because the firmware is prepared
        beforehand.

        chalkos does not act on this option yet: enroll the firmware keys out of band. The option
        is reserved for key enrollment by chalkd.
      '';
    };
    require = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Refuse to install a node that could not boot the signed image under Secure Boot.

        chalkos does not act on this option yet: enroll the firmware keys out of band. The option
        is reserved for key enrollment by chalkd.
      '';
    };
    signerCertificate = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        PEM certificate of the db signing key (public). chalkctl upgrade refuses an image whose UKI
        this certificate's key did not sign.
      '';
    };
  };
}
