# Helpers the built-in kinds share.
{
  # The fields that make a kind of the group a built-in one.
  builtin = group: {
    inherit group;
    version = "v1alpha1";
    builtin = true;
  };
  # A DNS name: letters, digits, dots and dashes, starting and ending with a letter or digit.
  dnsName = "[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?";
}
