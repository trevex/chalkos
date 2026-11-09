# How chalkos's modules refuse what they are given: each check makes a list of problems, every one
# a message that names the option, and refuse throws them all at once, so one evaluation reports
# every problem. The phrases the messages share are here too.
{ lib }:
{
  # value, or a throw of every problem, one a line.
  refuse =
    problems: value: if problems == [ ] then value else throw (lib.concatStringsSep "\n" problems);

  # The form of a name that names files and directories on the node and appears in commands, such
  # as an output's or a validator's: lower-case letters, digits and dashes, starting and ending with
  # a letter or digit.
  plainName = "[a-z0-9]([-a-z0-9]*[a-z0-9])?";

  # An attribute set given where another value was expected, by its attributes' names.
  attrsetText =
    set:
    if set == { } then
      "an empty attribute set"
    else
      "an attribute set of ${lib.concatStringsSep ", " (lib.attrNames set)}";

  # The image a message is about: the variant by its name, or the image when it has none.
  imageText = variant: if variant == null then "the image" else "the variant ${variant}";
}
