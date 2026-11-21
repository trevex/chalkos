# NixOS's nftables service without its shell scripts. With flushRuleset off, as appliance.nix sets
# it, the service keeps the deletions of the tables it loaded in /run/nftables/deletions.nft, so a
# reload deletes the tables of the ruleset before and the stop deletes its own; it writes and
# removes that file with three one-line bash scripts. The unit runs touch, cp and rm themselves
# in their places, in the same order, and keeps nixpkgs's ruleset file, which nft runs as its
# interpreter, and its check.
{
  chalkosLib,
  config,
  lib,
  options,
  pkgs,
  ...
}:
let
  cfg = config.networking.nftables;
  # nixpkgs's definition of the unit, whose ExecReload is ensure-deletions, the ruleset and
  # save-deletions. A definition of this module holds no list there.
  upstream =
    lib.findFirst (d: lib.isList (d.nftables.serviceConfig.ExecReload or null))
      (throw "modules/node/base/nftables.nix: nixpkgs's nftables unit has no ExecReload list")
      options.systemd.services.definitions;
  rules = lib.elemAt upstream.nftables.serviceConfig.ExecReload 1;
  # nixpkgs's deletions of the tables the ruleset loads, the same file as the ruleset includes;
  # an assertion fails the build when nixpkgs's differs.
  deletions = pkgs.writeScript "nftables-deletions" ''
    ${
      if cfg.flushRuleset then
        "flush ruleset"
      else
        lib.concatStringsSep "\n" (
          lib.mapAttrsToList (_: table: ''
            table ${table.family} ${table.name}
            delete table ${table.family} ${table.name}
          '') (lib.filterAttrs (_: table: table.enable) cfg.tables)
        )
    }
    ${cfg.extraDeletions}
  '';
  saved = "/run/nftables/deletions.nft";
  coreutils = "${pkgs.coreutils}/bin";
  ensure = "${coreutils}/touch ${saved}";
  save = "${coreutils}/cp ${deletions} ${saved}";
in
{
  config = lib.mkIf cfg.enable {
    assertions = [
      {
        # Paths compared as text alone: a regular expression may not name a store path.
        assertion = lib.hasInfix (builtins.unsafeDiscardStringContext ''include "${deletions}"'') (
          builtins.unsafeDiscardStringContext rules.text
        );
        message = "modules/node/base/nftables.nix: nixpkgs's nftables ruleset includes deletions other than the module's; update the module to nixpkgs's nftables.nix";
      }
    ];
    systemd.services.nftables.serviceConfig = chalkosLib.internal.noExec // {
      ExecStart = lib.mkForce [
        ensure
        rules
      ];
      ExecStartPost = lib.mkForce save;
      ExecReload = lib.mkForce [
        ensure
        rules
        save
      ];
      ExecStop = lib.mkForce [
        "${pkgs.nftables}/bin/nft -f ${saved}"
        "${coreutils}/rm ${saved}"
      ];
    };
  };
}
