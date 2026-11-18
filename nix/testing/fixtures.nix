# The images the checks and the documentation evaluate without booting them, from one call of
# mkImages so each is evaluated once: docs, with the Kubernetes layer; plain, without a layer;
# edge, a router image; and examples, with the Kubernetes layer and the BGP, Rack and Hardware
# extensions.
{ self }:
let
  inherit (self.nixosModules) kubernetes router;
in
self.lib.mkImages {
  images = [
    {
      name = "docs-metal";
      platform = "metal";
      modules = [ kubernetes ];
    }
    {
      name = "plain-metal";
      platform = "metal";
    }
    {
      name = "edge-metal";
      platform = "metal";
      modules = [ router ];
    }
    {
      name = "examples-metal";
      platform = "metal";
      modules = [
        kubernetes
        ../../examples/bgp-extension/extensions
        ../../examples/extensions
      ];
    }
  ];
}
