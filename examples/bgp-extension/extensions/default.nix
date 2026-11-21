# The guide's BGP extension, bird with its Speaker and Peer kinds and the config rendered from
# them, which the image node-bird-metal carries in place of the built-in speaker. Its credentials
# unit and its output's check are bash scripts, so the image carries a shell.
{
  imports = [ ./bgp.nix ];
  chalkos.shell.allow = true;
}
