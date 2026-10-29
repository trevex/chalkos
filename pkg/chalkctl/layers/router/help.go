package router

// The long descriptions and examples of the router layer's commands, as chalkctl's help.go keeps
// the others.

const nodeRoutesLong = `Shows the routes of a node of a router image: the routes in its kernel, of the main table unless
--table names another or all, and the paths its BGP instance selected, the best of a prefix
marked *. With a prefix, it shows the routes to that range and to ranges within it; with
--protocol, the kernel's routes of that protocol alone, and BGP's paths only for bgp. A node of
another image answers that the router layer is not in its image. The command needs a reader
client file or the secrets file.`

const nodeRoutesExample = `  # Every route of r1's main table and BGP's paths.
  chalkctl node routes r1

  # Every IPv6 route, in the kernel and in BGP.
  chalkctl node routes r1 ::/0

  # The routes zebra installed from BGP in every table, and BGP's paths.
  chalkctl node routes r1 --protocol bgp --table all`
