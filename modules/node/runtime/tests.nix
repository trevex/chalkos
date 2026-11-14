# Tests: config files a node could be given and what the image should make of them, the files its
# outputs render, the refusals it gives and the effects a change takes. `chalkos-rules test` runs
# them with the checks chalkd runs; nix flake check runs them through mkImages's checks, and the
# image's build only with chalkos.testsOnBuild.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal) refuse;
  cfg = config.chalkos.tests;
  # Facts by name, each any JSON value, as a fact's file or command gives it.
  factValues = lib.types.attrsOf (pkgs.formats.json { }).type;

  refusal = {
    options = {
      kind = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "Peer";
        description = "Kind of the refused document; empty for a refusal of no one document.";
      };
      name = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "router";
        description = "Name of the refused document; empty for a refusal of no one document.";
      };
      path = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "spec.interfaces[2]";
        description = "Path of the refused field in the document; empty for the whole document.";
      };
      message = lib.mkOption {
        type = lib.types.str;
        example = "enp3s0 has no Network document";
        description = "The refusal's message, exactly as chalkctl prints it.";
      };
    };
  };

  test = {
    options = {
      config = lib.mkOption {
        type = lib.types.path;
        example = lib.literalExpression "./tests/two-peers.yaml";
        description = ''
          YAML or JSON file of a node's documents, as a delivery gives them. The test checks them
          as a set, as chalkd checks a delivery, without the documents of the node's identity,
          such as its SystemDisk, which a test need not hold.
        '';
      };
      before = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        example = lib.literalExpression "./tests/one-peer.yaml";
        description = ''
          YAML or JSON file of the node's documents before `config`, which makes the test one of
          a change from these documents to those of `config`: change rules compare the two, and
          the effects due are those of the change. It must be accepted, or the test fails. Null
          makes the test one of a first delivery.
        '';
      };
      node = lib.mkOption {
        type = lib.types.str;
        default = "example";
        description = "Name of the node, which rules read as `node.name` and renderers and validators as `.node.name`.";
      };
      facts = lib.mkOption {
        type = factValues;
        default = { };
        example = {
          greetingLock.message = "hello";
        };
        description = ''
          Facts that rules and effects' `when` conditions read as `facts.<name>`, by name, each
          any JSON value. A fact the test leaves out reads as null. Each must be a fact of
          `chalkos.facts`.
        '';
      };
      oldFacts = lib.mkOption {
        type = factValues;
        default = { };
        description = ''
          Facts as they were before the change, in the form of `facts`, which the check of
          `before` and effects' `when` conditions read.
        '';
      };
      outputs = lib.mkOption {
        type = lib.types.attrsOf lib.types.path;
        default = { };
        example = lib.literalExpression "{ bird-conf = ./tests/two-peers/bird-conf; }";
        description = ''
          Golden files of outputs of `chalkos.outputs`, by output: a directory that holds
          exactly the files the output renders from `config`, with the hash of each store path
          written as `eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee`. A missing, extra or different file fails
          the test. An output left out is rendered and checked, but its files are not compared.
        '';
      };
      refusals = lib.mkOption {
        type = lib.types.listOf (lib.types.submodule refusal);
        default = [ ];
        description = ''
          The refusals the config gets, from the kinds' schemas, change rules, validators and
          outputs' checks, in the order chalkctl prints them, each once. They are always
          compared: the default, an empty list, expects the config to be accepted.
        '';
      };
      effects = lib.mkOption {
        type = lib.types.nullOr (lib.types.listOf lib.types.str);
        default = null;
        example = [ "bird" ];
        description = ''
          Names of the effects of `chalkos.effects` the change from `before` to `config` takes,
          sorted by name, as chalkd's plan lists them; an empty list expects none. Null leaves
          the effects unchecked. A test that sets it needs `before`.
        '';
      };
    };
  };

  # The files of the runner's output that no test's directory may be.
  reservedNames = [
    "summary.txt"
    "failures"
    "goldens.tsv"
  ];
  # What is wrong with a test: its name names its directory of the results, and what it names
  # must be the image's.
  testProblems =
    name: t:
    let
      where = "chalkos.tests.${name}";
      undeclared =
        field: given:
        map (f: "${where}.${field}.${f}: the image declares no fact ${f}") (
          lib.filter (f: !(config.chalkos.facts ? ${f})) (lib.attrNames given)
        );
    in
    lib.optional
      (
        lib.elem name [
          ""
          "."
          ".."
        ]
        || lib.hasInfix "/" name
        || lib.elem name reservedNames
        || builtins.match ".*[[:cntrl:]].*" name != null
      )
      "${where}: a test's name is a file name, without control characters, and none of ${lib.concatStringsSep ", " reservedNames}"
    ++ lib.optional (
      t.effects != null && t.before == null
    ) "${where}.effects: effects are those of a change, so the test needs before"
    ++ map (o: "${where}.outputs.${o}: the image has no output ${o}") (
      lib.filter (o: !(config.chalkos.outputs ? ${o})) (lib.attrNames t.outputs)
    )
    ++ map (e: "${where}.effects: the image has no effect ${e}") (
      lib.filter (e: !(config.chalkos.effects ? ${e})) (lib.defaultTo [ ] t.effects)
    )
    ++ undeclared "facts" t.facts
    ++ undeclared "oldFacts" t.oldFacts;

  # The tests as chalkos-rules test reads them.
  testsJSON = pkgs.writeText "chalkos-tests.json" (
    refuse (lib.concatLists (lib.mapAttrsToList testProblems cfg)) (
      builtins.toJSON {
        tests = lib.mapAttrs (_: t: {
          inherit (t)
            effects
            facts
            node
            oldFacts
            refusals
            ;
          config = "${t.config}";
          before = if t.before == null then null else "${t.before}";
          outputs = lib.mapAttrs (_: p: "${p}") t.outputs;
        }) cfg;
      }
    )
  );
  # What each test got, which never fails for a test that does.
  results =
    pkgs.runCommand "chalkos-test-results" { nativeBuildInputs = [ pkgs.chalkos.chalkos-rules ]; }
      ''
        chalkos-rules test ${config.system.build.chalkosRulesImage} ${testsJSON} $out
      '';
  # The tests' summary, built only when every test passes; otherwise the build fails with the
  # failures and a diff of each output's golden files and the files it rendered.
  tests = pkgs.runCommand "chalkos-tests" { nativeBuildInputs = [ pkgs.diffutils ]; } ''
    if [ -s ${results}/failures ]; then
      cat ${results}/failures >&2
      while IFS=$'\t' read -r test output golden got; do
        echo >&2
        echo "test $test, output $output: the golden files differ from what it rendered:" >&2
        diff -ruN "$golden" "${results}/$got" >&2 || true
        echo "to take what it rendered, with DIR the directory of chalkos.tests.$test.outputs.$output:" >&2
        echo "  rm -r DIR && cp -r --no-preserve=mode ${results}/$got DIR" >&2
      done < ${results}/goldens.tsv
      exit 1
    fi
    cp ${results}/summary.txt $out
  '';
in
{
  options.chalkos.tests = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule test);
    default = { };
    description = ''
      Tests of the image, by a name of the test's own: a config a node could be given, and what
      the image makes of it, the files its outputs render, the refusals it gives and, for a
      change, the effects it takes. `chalkos-rules test` runs each config through the checks
      chalkd runs for a delivery: the kinds' schemas, change rules, validators, every output's
      renderer and check, and the effects' plan. `system.build.chalkosTestResults` holds what
      each test got, `<test>/outputs/<output>/`, `<test>/refusals.json` and
      `<test>/effects.json`, and builds whatever the tests got; `system.build.chalkosTests`
      fails when a test does, with a diff of each output's golden files. mkImages's `checks`
      holds each image's `chalkosTests`, so `nix flake check` runs them.
    '';
  };

  options.chalkos.testsOnBuild = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether the image directory's build runs the tests of `chalkos.tests` and fails when one
      does. The tests change neither the image's content nor its root hash; without this,
      `nix flake check` runs them through mkImages's `checks`.
    '';
  };

  config = {
    system.build = {
      chalkosTestsJSON = testsJSON;
      chalkosTestResults = results;
      chalkosTests = tests;
    };
    chalkos.imageDirectory.checks = lib.mkIf config.chalkos.testsOnBuild [ tests ];
  };
}
