# bombon's CycloneDX ($cdx) of an image as an SPDX 2.3 document. The image ($name, $tag)
# contains each component whose store path is in the closure ($attrs, Nix's exportReferencesGraph)
# and that has a version. A nixpkgs licence without an SPDX identifier becomes a LicenseRef, which
# the document declares.
def ref: "LicenseRef-" + gsub("[^A-Za-z0-9.-]"; "-");
def licence:
  if . == null or length == 0 then "NOASSERTION"
  else
    map(.expression // .license.id // (.license.name | ref))
    | if length == 1 then .[0] else map("(" + . + ")") | join(" AND ") end
  end;
def spdxid: "SPDXRef-" + gsub("[^A-Za-z0-9.-]"; "-");

($attrs[0].closure | map(.path | ltrimstr("/nix/store/")) | INDEX(.)) as $closure
| [
    $cdx[0].components[]
    | select($closure[."bom-ref"] and (.version // "") != "")
    | {
        SPDXID: (."bom-ref" | spdxid),
        name,
        versionInfo: .version,
        downloadLocation: ([.externalReferences[]? | select(.type == "vcs") | .url][0] // "NOASSERTION"),
        filesAnalyzed: false,
        licenseConcluded: "NOASSERTION",
        licenseDeclared: (.licenses | licence),
        copyrightText: "NOASSERTION",
        externalRefs: [
          {referenceCategory: "PACKAGE-MANAGER", referenceType: "purl", referenceLocator: .purl}
        ]
      }
      + ([.externalReferences[]? | select(.type == "website") | .url][0] | if . then {homepage: .} else {} end)
      + (if .description then {summary: .description} else {} end)
  ]
| unique_by(.SPDXID) as $packages
| {
    spdxVersion: "SPDX-2.3",
    dataLicense: "CC0-1.0",
    SPDXID: "SPDXRef-DOCUMENT",
    name: "\($name) \($tag)",
    documentNamespace: $namespace,
    creationInfo: {
      created: $created,
      creators: ["Organization: chalkos", ($cdx[0].metadata.tools.components[] | "Tool: \(.name)-\(.version)")]
    },
    documentDescribes: ["SPDXRef-image"],
    packages: ([
      {
        SPDXID: "SPDXRef-image",
        name: $name,
        versionInfo: $tag,
        downloadLocation: "https://github.com/trevex/chalkos",
        filesAnalyzed: false,
        licenseConcluded: "NOASSERTION",
        licenseDeclared: "NOASSERTION",
        copyrightText: "NOASSERTION",
        primaryPackagePurpose: "OPERATING-SYSTEM"
      }
    ] + $packages),
    hasExtractedLicensingInfos: (
      [$packages[].licenseDeclared | scan("LicenseRef-[A-Za-z0-9.-]+")] | unique
      | map({licenseId: ., name: ltrimstr("LicenseRef-"), extractedText: "The licence nixpkgs names \(ltrimstr("LicenseRef-")), which has no SPDX identifier."})
    ),
    relationships: (
      [{spdxElementId: "SPDXRef-DOCUMENT", relationshipType: "DESCRIBES", relatedSpdxElement: "SPDXRef-image"}]
      + [$packages[] | {spdxElementId: "SPDXRef-image", relationshipType: "CONTAINS", relatedSpdxElement: .SPDXID}]
    )
  }
