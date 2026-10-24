# Facts: node state that change rules read as facts.<name>, each from a file or a command. The
# image lists them in /etc/chalkos/facts.json, from which chalkd loads them.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  # A fact: node state that rules read as facts.<name>.
  factModule = {
    options = {
      file = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "/state/kubernetes/share.json";
        description = "File whose JSON content is the fact: null while the file is missing. A file that cannot be read, or holds no JSON, refuses the delivery.";
      };
      fields = lib.mkOption {
        type = lib.types.listOf lib.types.nonEmptyStr;
        default = [ ];
        example = [ "kind" ];
        description = "Keys of the file's JSON object the fact keeps, so rules and their messages see nothing else of the file; empty keeps the whole content, unless `hashOthers` is set.";
      };
      hashOthers = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Keep the other keys of the file's JSON object too, each as the hex SHA-256 of its value, so rules see whether a value changed but not what it holds, as of a file of private keys.";
      };
      command = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = lib.literalExpression ''"''${pkgs.my-tool}/bin/my-tool facts"'';
        description = ''
          Program of the image's store, with its arguments, that prints the fact as JSON. It reads
          `{"config": …, "oldConfig": …}` and its inputs as JSON on standard input, runs through
          `systemd-run` with the system read-only and no network, and must finish within 30
          seconds with at most 1 MiB of output.
        '';
      };
      inputs = lib.mkOption {
        type = lib.types.listOf (lib.types.enum [ "fallbackSecret" ]);
        default = [ ];
        description = "Delivery inputs the command receives besides the config: `fallbackSecret`, the fallback secret delivered with it.";
      };
    };
  };
  factsCfg = config.chalkos.facts;
  # A fact's name is read as facts.<name> in CEL, so it is an identifier.
  badFacts = lib.filter (name: builtins.match "[a-z][A-Za-z0-9]*" name == null) (
    lib.attrNames factsCfg
  );
  # A fact is a file or a command; only a command takes inputs, and only a file keeps fields.
  unsourcedFacts = lib.filter (
    name: (factsCfg.${name}.file == null) == (factsCfg.${name}.command == null)
  ) (lib.attrNames factsCfg);
  fileInputs = lib.filter (name: factsCfg.${name}.file != null && factsCfg.${name}.inputs != [ ]) (
    lib.attrNames factsCfg
  );
  commandFields = lib.filter (
    name:
    factsCfg.${name}.command != null && (factsCfg.${name}.fields != [ ] || factsCfg.${name}.hashOthers)
  ) (lib.attrNames factsCfg);
  facts =
    if badFacts != [ ] then
      throw "chalkos.facts.${lib.head badFacts}: a fact's name must match [a-z][A-Za-z0-9]*, since rules read it as facts.<name>"
    else if unsourcedFacts != [ ] then
      throw "chalkos.facts.${lib.head unsourcedFacts}: a fact has either a file or a command"
    else if fileInputs != [ ] then
      throw "chalkos.facts.${lib.head fileInputs}: only a command fact takes inputs"
    else if commandFields != [ ] then
      throw "chalkos.facts.${lib.head commandFields}: only a file fact keeps fields or hashes them"
    else
      lib.mapAttrs (
        _: f:
        if f.file != null then
          {
            inherit (f) file;
          }
          // lib.optionalAttrs (f.fields != [ ]) { inherit (f) fields; }
          // lib.optionalAttrs f.hashOthers { inherit (f) hashOthers; }
        else
          { inherit (f) command; } // lib.optionalAttrs (f.inputs != [ ]) { inherit (f) inputs; }
      ) factsCfg;
  factsFile = pkgs.writeText "chalkos-facts.json" (builtins.toJSON facts);
in
{
  options.chalkos.facts = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule factModule);
    default = { };
    description = ''
      Facts about the node that change rules read as `facts.<name>`, each from a file or a
      command. chalkd loads a fact when a rule first reads it and keeps it for the delivery: a
      file is read once, and a command runs once for each config it reads; chalkctl
      runs no rule that reads one.
    '';
  };

  config = {
    system.build.chalkosFacts = factsFile;
    system.build.chalkosFactDefinitions = facts;
    environment.etc."chalkos/facts.json".source = factsFile;
  };
}
