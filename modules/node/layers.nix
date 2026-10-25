# The layers an image carries beside the base every image has, such as Kubernetes. A layer's
# module names it in chalkos.layers, and the image holds the layer's marker
# /etc/chalkos/layers/<name>, from which chalkd learns the layers whose methods of the node API it
# serves. An image carries one layer at most.
{ config, lib, ... }:
let
  inherit (config.chalkos) layers;
  names = lib.attrNames layers;
  image =
    if config.chalkos.variant.name == null then
      "the image"
    else
      "the variant ${config.chalkos.variant.name}";
in
{
  options.chalkos.layers = lib.mkOption {
    type = lib.types.attrsOf lib.types.str;
    default = { };
    internal = true;
    description = ''
      The image's layers by name, each with the text of its marker /etc/chalkos/layers/<name>:
      empty, or facts of the layer.
    '';
  };

  config = {
    environment.etc = lib.mapAttrs' (
      name: text: lib.nameValuePair "chalkos/layers/${name}" { inherit text; }
    ) layers;
    assertions = [
      {
        assertion = lib.length names <= 1;
        message = "${image} enables the ${lib.concatStringsSep " and " names} layers; an image carries one layer at most, so set kubernetes = false or router = false";
      }
    ];
  };
}
