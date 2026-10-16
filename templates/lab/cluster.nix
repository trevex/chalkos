# The image the lab's nodes run. The nodes' config is the YAML of chalkos.yaml, cluster/ and
# nodes/, which chalkctl and chalklab read; this file only builds the image.
{
  # mkCluster needs a cluster's name and endpoint to build an image; the nodes take theirs from
  # cluster/cluster.yaml.
  chalkos.cluster = {
    name = "lab";
    endpoint = "https://192.168.123.11:6443";
  };

  # The public part of the cluster's secrets, which chalkctl gen secrets writes. The image carries
  # its OS CA, so in maintenance mode it accepts only the cluster's clients.
  chalkos.osCA = ./secrets.pub.json;

  # One image for both nodes; cp1's Node document makes it the control plane.
  chalkos.roles.node = { };
}
