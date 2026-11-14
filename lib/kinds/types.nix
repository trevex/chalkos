# The option types of kind fields that the schema converter and the get paths recognise by name,
# and the pattern of a document's name.
{ lib }:
rec {
  # The name of the types ref returns, which the schema converter recognises.
  refName = "chalkosRef";

  # A reference to a document of another kind by its name: { kind; reserved ? [ ]; }, or the kind
  # alone. The value is the name, as metadata.name writes it. The kinds module fills in the target
  # kind's apiVersion, and refuses the reserved names, which stand for something that is no
  # document, such as the VRF default, as names of documents of that kind.
  ref =
    target:
    let
      t = if lib.isString target then { kind = target; } else target;
      unknown = lib.attrNames (
        removeAttrs t [
          "kind"
          "reserved"
        ]
      );
      reserved = t.reserved or [ ];
      badNames = lib.filter (n: !lib.isString n || builtins.match documentName n == null) reserved;
    in
    if !(t ? kind) || !lib.isString t.kind then
      throw ''chalkosLib.types.ref: give the kind of the documents the field names, as ref "VRF" or ref { kind = "VRF"; }''
    else if unknown != [ ] then
      throw "chalkosLib.types.ref ${t.kind}: ${lib.head unknown} is none of kind and reserved"
    else if badNames != [ ] then
      throw "chalkosLib.types.ref ${t.kind}: the reserved name ${builtins.toJSON (lib.head badNames)} is no document name, which matches ^${documentName}$"
    else
      lib.types.strMatching documentName
      // {
        name = refName;
        description = "name of a ${t.kind} document";
        chalkosRef = {
          inherit (t) kind;
          reserved = lib.unique reserved;
        };
      };

  # A reference to a key of a Secret document. chalkd gives a granted unit the value it names as a
  # credential.
  secretRef =
    lib.types.submodule {
      options.secretRef = {
        name = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "Name of the Secret document.";
        };
        key = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "Key in the Secret's data.";
        };
      };
    }
    // {
      name = "chalkosSecretRef";
      description = "reference to a key of a Secret";
    };

  # A Kubernetes object, which the control planes apply as it is. Built-in kinds alone use it:
  # chalkosLib.types does not offer it, as extensions get no raw schemas.
  kubernetesObject = lib.types.attrsOf lib.types.anything // {
    name = "chalkosKubernetesObject";
    description = "Kubernetes object";
  };

  # An IPv4 or IPv6 address, which the rules read with ip(), and an address range, which they read
  # with cidr(). The schema converter gives them the formats ip and cidr, which chalkctl and chalkd
  # check with the parsers of ip() and cidr() and refuse what they read but the outputs may not,
  # such as an address's zone, so a value the schema accepts parses in every rule and output. The
  # patterns check only what Nix itself checks, such as defaults and examples.
  address = lib.types.strMatching "[0-9A-Fa-f:.]+" // {
    name = "chalkosAddress";
    description = "IPv4 or IPv6 address";
  };
  prefix = lib.types.strMatching "[0-9A-Fa-f:.]+/[0-9]{1,3}" // {
    name = "chalkosPrefix";
    description = "address range";
  };

  # A container image reference with its registry and a tag, a digest or both, as
  # repository:tag@sha256:<digest>; and one whose tag is a version v<major>.<minor>.<patch> with an
  # optional suffix. The schema converter gives them the formats image-reference and
  # versioned-image-reference, which chalkctl and chalkd check; the patterns check only what Nix
  # itself checks, such as defaults and examples.
  imageReference = lib.types.strMatching "[^ ]+[:@][^ ]+" // {
    name = "chalkosImageReference";
    description = "image reference";
  };
  versionedImageReference = lib.types.strMatching "[^ ]+:v[0-9]+\\.[0-9]+\\.[0-9]+[^ ]*" // {
    name = "chalkosVersionedImageReference";
    description = "image reference with a version tag";
  };

  # A port from 1, or a range of ports written "<first>-<last>", which the router kinds take. The
  # schema converter gives the range the format port-range, which chalkctl and chalkd check: both
  # ports from 1 to 65535 and the first no higher than the last.
  portRange =
    lib.types.either (lib.types.ints.between 1 65535) (
      lib.types.strMatching "[1-9][0-9]{0,4}-[1-9][0-9]{0,4}"
    )
    // {
      name = "chalkosPortRange";
      description = "port or range of ports";
    };

  # A BGP neighbour's AS: external or internal, as the session finds it, or an AS number.
  peerAS =
    lib.types.either (lib.types.enum [
      "external"
      "internal"
    ]) (lib.types.ints.between 1 4294967295)
    // {
      name = "chalkosPeerAS";
      description = "external, internal or an AS number";
    };

  # A document's metadata.name, as chalkcfg reads it.
  documentName = "[a-z0-9]([-a-z0-9.]*[a-z0-9])?";
}
