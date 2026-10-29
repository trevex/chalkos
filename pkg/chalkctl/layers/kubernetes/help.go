package kubernetes

// The long descriptions and examples of the Kubernetes layer's commands, as chalkctl's help.go keeps
// the others.

const bootstrapLong = `Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The node refuses when it is
bootstrapped already or holds etcd data, so a second cluster is never initialised; the other
control planes join the first. The command needs an admin client file or the secrets file.`

const bootstrapExample = `  chalkctl bootstrap cp1`

const kubeconfigLong = `Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl. The file holds the certificate's private key and is written with mode
0600. --server points clients at another URL than the cluster endpoint (a forwarded port, for
example), while they still verify the API server's certificate for the endpoint. The
kubeconfig's cluster, user and context are named after the inventory's metadata.name, or after
--context. The command needs the secrets file.`

const kubeconfigExample = `  chalkctl kubeconfig --secrets secrets.age --out lab.kubeconfig

  # With the context home, to merge into a kubeconfig that holds other clusters.
  chalkctl kubeconfig --secrets secrets.age --context home --out home.kubeconfig

  # Through a port forwarded to the API server.
  chalkctl kubeconfig --secrets secrets.age --server https://127.0.0.1:6443 --out -`

const etcdLong = `Manages the members of the cluster's etcd, which runs on the control planes. etcd members
needs a reader client file, and etcd remove-member and etcd leave need an admin client file; the
secrets file works for all three.`

const etcdExample = `  chalkctl etcd members`

const etcdMembersLong = `Lists etcd's members the way one control plane's own member sees them: name, ID, peer URLs,
whether a member votes or is a learner, and its health. The first control plane that answers is
asked, or --via. The command needs a reader client file or the secrets file.`

const etcdMembersExample = `  chalkctl etcd members --via cp2`

const etcdRemoveMemberLong = `Removes another node's etcd member, named by its node or its member ID: for example the member
of a node that is gone. The removal is refused when the voters left would have fewer healthy
members than their quorum, unless --force is given. A control plane other than the removed one
does the removal: --via, or the first one that answers. The command needs an admin client file
or the secrets file.`

const etcdRemoveMemberExample = `  # Remove the member of cp3, whose machine failed.
  chalkctl etcd remove-member cp3

  # Remove a member by its ID.
  chalkctl etcd remove-member 8e9e05c52164694d --via cp1`

const etcdLeaveLong = `Takes a control plane out of etcd: the node releases its virtual IPs, removes its own member
with the same quorum guard as remove-member, stops its static pods (etcd and the Kubernetes
control-plane components), deletes its etcd data and unpins its addresses. It joins the cluster
again only once it is reinstalled. A node whose own member does not answer (for example after it
lost its pinned address) leaves only with --force, through the other members. The command needs
an admin client file or the secrets file.`

const etcdLeaveExample = `  chalkctl etcd leave cp3`

const nodeRevokeLong = `Cuts a node off the cluster by name, at once and without a rotation. A control plane lists the
node, with the time of its first revocation, in the ConfigMap kube-system/chalkos-revoked-nodes and
deletes its Node object. From then on the control planes renew none of its certificates, the API
server denies every write by its kubelet, and chalkctl rotate skips it. Its certificates stay valid
until they expire, so its kubelet can still read what every kubelet may. See
https://trevex.github.io/chalkos/how-it-works/kubernetes/certificates/#revoking-a-node-cuts-off-its-writes-and-renewals.

Revoking a control plane does not cut it off, because its disk holds the node CA's and the
Kubernetes CA's keys. The command refuses a node that is still an etcd member, and the control
plane --via names: take it out with chalkctl etcd leave, or chalkctl etcd remove-member once it is
gone, then rotate os-ca and kubernetes-ca. A cluster without Kubernetes keeps no revoked nodes, and
there chalkctl rotate os-ca cuts a node off.

The command first asks every other control plane whether it reads the revoked nodes, and refuses
while one does not answer or does not say it read them. --force revokes the node despite either
refusal. A control plane revokes the node: --via, or the first other one that answers. The command
needs an admin client file or the secrets file.`

const nodeRevokeExample = `  # Cut off w1, which was removed from the cluster.
  chalkctl node revoke w1`

const nodeUnrevokeLong = `Removes a node from the revoked nodes, so a node of that name may join the cluster again. The old
node's certificates would work again too, so the control plane refuses while the OS CA or the
Kubernetes CA predates the revocation, and names the rotations missing. To reuse the name, run
chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish, first; a node
installed afresh under the name then joins as usual. See
https://trevex.github.io/chalkos/how-it-works/kubernetes/certificates/#revoking-a-node-cuts-off-its-writes-and-renewals.

--force unrevokes the node anyway, and its old certificates work again until they expire. The old
node does not come back by itself: its kubelet registers its Node once as it starts, so reboot it
with chalkctl reboot, or restart its kubelet.

A control plane unrevokes the node: --via, or the first one that answers. The command needs an
admin client file or the secrets file.`

const nodeUnrevokeExample = `  # Reuse the name w1 once the rotations of os-ca and kubernetes-ca have finished.
  chalkctl node unrevoke w1

  # Let the old w1 back in, certificates and all.
  chalkctl node unrevoke w1 --force
  chalkctl reboot w1`
