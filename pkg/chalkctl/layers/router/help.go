package router

// The long descriptions and examples of the router layer's commands, as chalkctl's help.go keeps
// the others.

const nodeRoutesLong = `Shows the routes of a node of a router image: the routes in its kernel, of the main table unless
--vrf names a VRF, whose table it shows, or --table names another table or all, and the paths
the BGP instance of that VRF selected, the best of a prefix marked *. A path the instance
imports from another VRF names that VRF as its peer. With a prefix, it shows the routes to that
range and to ranges within it; with --protocol, the kernel's routes of that protocol alone, and
BGP's paths only for bgp. --vrf and --table exclude each other, and --vrf fails on a node whose
image does not know VRFs. A router whose image carries no BGP shows the kernel's routes alone. It
shows at most 10,000 kernel routes and 10,000 paths and says so on standard error when it leaves
some out, as for a full Internet table without a prefix. A node of another image answers that
the router layer is not in its image.`

const nodeRoutesExample = `  # Every route of r1's main table and BGP's paths.
  chalkctl node routes r1

  # Every IPv6 route, in the kernel and in BGP.
  chalkctl node routes r1 ::/0

  # The routes of the VRF tenant: its table and its BGP instance's paths.
  chalkctl node routes r1 --vrf tenant

  # The routes zebra installed from BGP in every table, and BGP's paths.
  chalkctl node routes r1 --protocol bgp --table all`
