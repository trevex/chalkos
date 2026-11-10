# The images the checks and the documentation evaluate without booting them, from one call of
# mkImages so each is evaluated once: docs, the default variant; plain, without Kubernetes; edge,
# a router image; and examples, the default variant with the extensions of the homelab and
# extensions examples.
{ self }:
self.lib.mkImages {
  platforms = [ "metal" ];
  variants = {
    docs = { };
    plain.kubernetes = false;
    edge = {
      kubernetes = false;
      router = true;
    };
    examples.modules = [
      ../../examples/homelab/extensions
      ../../examples/extensions
    ];
  };
}
