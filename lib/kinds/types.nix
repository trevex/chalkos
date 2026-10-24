# The option types of kind fields that the schema converter and the get paths recognise by name,
# and the pattern of a document's name.
{ lib }:
{
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
  # lib.chalkos.types does not offer it, as extensions get no raw schemas.
  kubernetesObject = lib.types.attrsOf lib.types.anything // {
    name = "chalkosKubernetesObject";
    description = "Kubernetes object";
  };

  # A document's metadata.name, as chalkcfg reads it.
  documentName = "[a-z0-9]([-a-z0-9.]*[a-z0-9])?";
}
