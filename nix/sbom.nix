# The software bills of materials (SBOMs) of what a release publishes.
#
# image: an image's SBOM, SPDX 2.3 JSON, lists each package of what the image ships, its system's
# runtime closure and its kernel, which the UKI carries, with its version, its licence, its source
# and its purl, from nixpkgs's metadata. bombon reads the components and the metadata of the
# derivations it finds by walking the system's derivation; this file converts its CycloneDX to
# SPDX. bombon finds a derivation only through derivation-valued attributes, so a package that a
# unit or a script names in a string, such as a service's ExecStart, is found by name instead: a
# string's context names the derivation, and the package of that name in the image's nixpkgs, or
# in its chalkos scope, is taken when its derivation is the same. Those packages only add metadata:
# the SBOM keeps the components of the closure, whose store paths with neither version nor package,
# such as generated configuration files, it leaves out.
#
# program: a Go program's SBOM, CycloneDX JSON, lists the modules linked into its binary, from the
# build information Go embeds, with their licences, which cyclonedx-gomod reads from the modules'
# source in the shared module download, offline.
{ pkgs, bombon }:
let
  inherit (pkgs) lib;

  # A store path's name, without its hash and its .drv.
  storeName = path: lib.removeSuffix ".drv" (lib.substring 33 (-1) (baseNameOf path));

  # The derivations a string's context names that are packages of the package sets given, by name.
  packagesOf =
    sets: string:
    lib.concatMap (
      drvPath:
      let
        inherit (builtins.parseDrvName (storeName drvPath)) name;
        candidates = lib.concatMap (
          set:
          let
            found = builtins.tryEval (
              let
                p = set.${name} or null;
              in
              if lib.isDerivation p && p.drvPath == drvPath then [ p ] else [ ]
            );
          in
          if found.success then found.value else [ ]
        ) sets;
      in
      lib.take 1 candidates
    ) (lib.filter (lib.hasSuffix ".drv") (lib.attrNames (builtins.getContext string)));

  # Every string in a value of an option, such as a service's script or its serviceConfig.
  stringsOf =
    value:
    if lib.isString value then
      [ value ]
    else if lib.isList value then
      lib.concatMap stringsOf value
    else if lib.isAttrs value && !lib.isDerivation value then
      lib.concatMap stringsOf (lib.attrValues value)
    else
      [ ];

  # The CycloneDX of bombon as SPDX 2.3: the image, which the document describes, contains each
  # component of the closure that has a version.
  toSPDX = ./sbom-spdx.jq;
in
{
  image =
    {
      # The image's name, its tag and its NixOS system.
      name,
      tag,
      nixos,
      # The time of the SBOM, in RFC 3339, the commit's for a release, so a rebuild writes the
      # same file.
      created ? "1970-01-01T00:00:00Z",
    }:
    let
      inherit (nixos) config;
      toplevel = config.system.build.toplevel;
      inherit (config.boot.kernelPackages) kernel;
      sets = [
        nixos.pkgs
        nixos.pkgs.chalkos
      ];
      safe =
        f: x:
        let
          r = builtins.tryEval (f x);
        in
        if r.success then r.value else [ ];
      services = lib.attrValues config.systemd.services;
      # The packages besides toplevel whose metadata bombon reads.
      packages = lib.unique (
        lib.filter lib.isDerivation (
          config.environment.systemPackages
          ++ config.systemd.packages
          ++ config.services.udev.packages
          ++ lib.concatMap (s: s.path) services
          ++ [ kernel ]
          ++ lib.toList config.hardware.firmware
          ++ lib.concatMap (safe (
            s:
            packagesOf sets (
              lib.concatStrings (stringsOf [
                s.script
                s.preStart
                s.postStart
                s.preStop
                s.postStop
                s.reload
                s.serviceConfig
                s.environment
              ])
            )
          )) services
          ++ lib.concatMap (safe (
            f:
            packagesOf sets (
              lib.concatStrings (stringsOf [
                (f.text or "")
                (toString f.source)
              ])
            )
          )) (lib.attrValues config.environment.etc)
        )
      );
      cdx = bombon.buildBom toplevel {
        # One derivation that names them all, which bombon walks once.
        extraPaths = [
          (pkgs.runCommand "${name}-packages" { inherit packages; } "printf '%s\\n' $packages >$out")
        ];
      };
    in
    pkgs.runCommand "${name}.spdx.json"
      {
        __structuredAttrs = true;
        exportReferencesGraph.closure = [
          toplevel
          kernel
        ];
        nativeBuildInputs = [ pkgs.jq ];
        passthru = { inherit cdx; };
      }
      ''
        jq -n -S \
          --slurpfile cdx ${cdx} \
          --slurpfile attrs "$NIX_ATTRS_JSON_FILE" \
          --arg name ${lib.escapeShellArg name} \
          --arg tag ${lib.escapeShellArg tag} \
          --arg created ${lib.escapeShellArg created} \
          --arg namespace ${lib.escapeShellArg "https://github.com/trevex/chalkos/spdx/${name}/${tag}/${lib.substring 11 32 toplevel.outPath}"} \
          -f ${toSPDX} >"$out"
      '';

  # Shell that validates SBOMs, for the checks: validate-spdx <file> holds an image's SBOM to SPDX
  # 2.3, to the licence list, and to a package of every component with a version, at least
  # three quarters of them with a licence, and among them the kernel under GPL-2.0-only;
  # validate-cyclonedx <file> holds a program's SBOM to CycloneDX with a licence for every
  # module. pyspdxtools checks the document without its licences, as its licence list is older
  # than nixpkgs's; jq checks them against the list nixpkgs carries.
  validate = ''
    validate-spdx() {
      local file=$1 coverage
      jq -e \
        --slurpfile licences ${pkgs.spdx-license-list-data.json}/json/licenses.json \
        --slurpfile exceptions ${pkgs.spdx-license-list-data.json}/json/exceptions.json '
        ($licences[0].licenses | map(.licenseId)) + ($exceptions[0].exceptions | map(.licenseExceptionId))
          + [.hasExtractedLicensingInfos[].licenseId] as $ids
        | .spdxVersion == "SPDX-2.3"
          and (.packages | length > 1)
          and all(.packages[]; (.name // "") != "" and (.versionInfo // "") != "")
          and all(.packages[].licenseDeclared | select(. != "NOASSERTION")
            | scan("[A-Za-z0-9.+-]+") | select(. != "AND" and . != "OR" and . != "WITH");
            . as $id | any($ids[]; . == $id))
          and any(.packages[]; .name == "linux" and .licenseDeclared == "GPL-2.0-only")
      ' "$file" >/dev/null || { echo "error: $file is no SBOM of an image" >&2; return 1; }
      coverage=$(jq '[.packages[1:][] | .licenseDeclared != "NOASSERTION"] | (map(select(.)) | length) * 100 / length | floor' "$file")
      echo "$file: $(jq '.packages | length - 1' "$file") packages, $coverage% with a licence"
      if [ "$coverage" -lt 75 ]; then
        echo "error: $file names a licence for only $coverage% of its packages" >&2
        return 1
      fi
      jq '(.packages[] | .licenseDeclared) = "NOASSERTION" | del(.hasExtractedLicensingInfos)' "$file" >unlicensed.spdx.json
      pyspdxtools --infile unlicensed.spdx.json
    }
    validate-cyclonedx() {
      local file=$1
      jq -e '
        .bomFormat == "CycloneDX"
        and (.serialNumber // "" | test("^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))
        and (.specVersion // "") != ""
        and (.components | length > 0)
        and all(.components[], .metadata.component; (.name // "") != "" and (.version // "") != "" and (.licenses | length > 0))
      ' "$file" >/dev/null || { echo "error: $file is no SBOM of a program" >&2; return 1; }
      echo "$file: $(jq '.components | length' "$file") modules"
    }
  '';
  validateInputs = [
    pkgs.jq
    pkgs.python3Packages.spdx-tools
  ];

  program =
    package:
    package.overrideAttrs (old: {
      pname = "${old.pname}-sbom";
      nativeBuildInputs = old.nativeBuildInputs ++ [
        pkgs.cyclonedx-gomod
        pkgs.jq
      ];
      # The module download is the module proxy that buildGoModule's configurePhase sets, so
      # licence detection downloads the modules from it, offline. The binary's own module, which
      # it does not download, takes the package's licence. The serial number, which consumers such
      # as actions/attest require, is a UUID derived from the binary's hash, so the SBOM stays
      # reproducible.
      buildPhase = ''
        runHook preBuild
        cyclonedx-gomod bin -json -licenses -assert-licenses -noserial -notimestamp \
          -version v${old.version} \
          -output sbom.json ${package}/bin/${old.pname}
        h=$(sha256sum ${package}/bin/${old.pname} | cut -c1-32)
        serial="urn:uuid:''${h:0:8}-''${h:8:4}-5''${h:13:3}-$(printf %x $(( (0x''${h:16:1} & 3) | 8 )))''${h:17:3}-''${h:20:12}"
        jq --arg licence ${package.meta.license.spdxId} --arg serial "$serial" \
          '.serialNumber = $serial | .metadata.component.licenses = [{license: {id: $licence}}]' sbom.json >"$out"
        runHook postBuild
      '';
      installPhase = "true";
      outputs = [ "out" ];
      doCheck = false;
      dontFixup = true;
      passthru = removeAttrs old.passthru [ "sbom" ];
    });
}
