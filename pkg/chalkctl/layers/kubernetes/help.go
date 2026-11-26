package kubernetes

// The long descriptions and examples of the Kubernetes layer's commands, as chalkctl's help.go keeps
// the others.

const bootstrapLong = `Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The other control planes join
the first.

With an inventory, chalkctl first asks the inventory's other control planes for their status and
refuses while one is an etcd member, is joining or does not answer. --ignore-members skips this
check, as
when recovering a cluster whose other control planes are lost. The node itself refuses when it
is bootstrapped already or holds etcd data, and when an API server answers at the cluster
endpoint, with a certificate of the cluster's CA or of another CA, so a second cluster is not
initialised behind the endpoint.`

const bootstrapExample = `  chalkctl bootstrap cp1`

const kubeconfigLong = `Writes a kubeconfig with a client certificate for the Kubernetes API server, issued from the
secrets file for --name in the group chalkos:cluster-admins, which is bound to cluster-admin,
and valid for --ttl, a day by default. The file holds the certificate's private key and is
written with mode 0600. Write a new one from the secrets file when it expires; pass a longer
--ttl for a kubeconfig that must outlast a day, knowing that a leaked one grants cluster-admin
until it expires or the Kubernetes CA rotates. --server points clients at another URL than the
cluster endpoint (a forwarded port, for example), while they still verify the API server's
certificate for the endpoint. The kubeconfig's cluster, user and context are named after the
inventory's metadata.name, or after --context. An existing file is replaced only with --overwrite.`

const kubeconfigExample = `  chalkctl kubeconfig --secrets secrets.age --out lab.kubeconfig

  # With the context home, to merge into a kubeconfig that holds other clusters.
  chalkctl kubeconfig --secrets secrets.age --context home --out home.kubeconfig

  # Through a port forwarded to the API server.
  chalkctl kubeconfig --secrets secrets.age --server https://127.0.0.1:6443 --out -`

const etcdLong = `Manages the members of the cluster's etcd, which runs on the control planes.`

const etcdExample = `  chalkctl etcd members`

const etcdMembersLong = `Lists etcd's members the way one control plane's own member sees them: name, ID, peer URLs,
whether a member votes or is a learner, and its health. The first control plane that answers is
asked, or --via.`

const etcdMembersExample = `  chalkctl etcd members --via cp2`

const etcdRemoveMemberLong = `Removes another node's etcd member, named by its node or its member ID: for example the member
of a node that is gone. The removal is refused when the voters left would have fewer healthy
members than their quorum, unless --ignore-quorum is given, and etcd then accepts no writes until
enough members are healthy again. A control plane other than the removed one does the removal:
--via, or the first one that answers.`

const etcdRemoveMemberExample = `  # Remove the member of cp3, whose machine failed.
  chalkctl etcd remove-member cp3

  # Remove a member by its ID.
  chalkctl etcd remove-member 8e9e05c52164694d --via cp1`

const etcdLeaveLong = `Takes a control plane out of etcd: the node releases its virtual IPs, removes its own member
with the same quorum guard as remove-member, stops its static pods (etcd and the Kubernetes
control-plane components), deletes its etcd data and unpins its addresses. It joins the cluster
again only once it is reinstalled. A node whose own member does not answer (for example after it
lost its pinned address) leaves only with --through-others, which removes its member through the
other members.`

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
while one does not answer or does not say it read them. --allow-control-plane revokes a control
plane, an etcd member or the node --via names all the same, and --skip-read-check revokes the node
without asking the other control planes. A control plane revokes the node: --via, or the first
other one that answers.`

const nodeRevokeExample = `  # Cut off w1, which was removed from the cluster.
  chalkctl node revoke w1`

const nodeUnrevokeLong = `Removes a node from the revoked nodes, so a node of that name may join the cluster again. The old
node's certificates would work again too, so the control plane refuses while the OS CA or the
Kubernetes CA predates the revocation, and names the rotations missing. To reuse the name, run
chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish, first; a node
installed afresh under the name then joins as usual. See
https://trevex.github.io/chalkos/how-it-works/kubernetes/certificates/#revoking-a-node-cuts-off-its-writes-and-renewals.

--allow-old-ca unrevokes the node anyway, and its old certificates work again until they expire. The
old node does not come back by itself: its kubelet registers its Node once as it starts, so reboot
it with chalkctl node reboot --no-drain, as its Node is gone and holds nothing to drain, or restart
its kubelet.

A control plane unrevokes the node: --via, or the first one that answers.`

const nodeUnrevokeExample = `  # Reuse the name w1 once the rotations of os-ca and kubernetes-ca have finished.
  chalkctl node unrevoke w1

  # Let the old w1 back in, certificates and all.
  chalkctl node unrevoke w1 --allow-old-ca
  chalkctl node reboot w1 --no-drain`

const registryLong = `Manages the images of the cluster registry, which chalkos-registry serves on the nodes that a
ClusterRegistry document's endpoints name. Nothing copies images between the endpoints, so each
command acts on every one of them, one after the other, and reports each.`

const registryExample = `  chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0
  chalkctl registry list`

const registryPushLong = `Pushes the image of SOURCE to every endpoint of the cluster's ClusterRegistry, under REFERENCE
below the endpoint: docker.io/library/nginx:1.27 goes to <endpoint>/docker.io/library/nginx:1.27,
where containerd looks for it. REFERENCE's host must be one the ClusterRegistry mirrors.
REFERENCE may be written as the kinds write images, repository:tag@sha256:<digest>: the image goes
under the tag, and chalkctl refuses a source of another digest before it reaches an endpoint. A
reference by digest alone pushes the manifest without a tag.

SOURCE is an OCI image layout directory, an OCI archive, a docker archive as docker save writes
it, or an image in a registry that the workstation reaches, read with its docker config. chalkctl
reads a local source through before the first endpoint, so a missing blob fails at once; it
unpacks an OCI archive below $TMPDIR. A docker archive holds one platform's image, whose digest
differs from the one its registry serves, so pods and documents that pin the upstream digest do
not find it: push the upstream image or an OCI layout copied from it instead. An index goes with
every image it names, after them, attestations included, so an index of many platforms takes
several times the space of one image. chalkctl may read an image from a registry on a loopback or
private address over plain HTTP; pin REFERENCE to the image's digest, and chalkctl refuses
anything else it reads.

chalkctl prints the image's digest, checks that each endpoint serves it, reports each endpoint,
and fails unless every one has the image. --connect-to reaches an endpoint through another
address, such as a forwarded port, while it still verifies the endpoint's certificate.`

const registryPushExample = `  chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0 --secrets secrets.age

  # The control-plane image a Cluster names, from a registry the workstation reaches.
  chalkctl registry push registry.k8s.io/kube-apiserver:v1.37.2 \
    registry.k8s.io/kube-apiserver:v1.37.2@sha256:3e563afb5cb5b72a10bac7012698dd09f5a42bde7d22de8d7d3c12e654df86b4

  # Through a forwarded port to the endpoint fd00::11.
  chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0 --connect-to fd00::11=127.0.0.1:15000`

const registryDeleteLong = `Deletes REFERENCE, a tag or a digest, below every endpoint of the cluster's ClusterRegistry, as
registry push names it there. An endpoint that does not hold it counts as done, and the command
fails unless every endpoint deleted it or never held it.

A tag goes alone: chalkctl prints the digest it named and the tags that still name it. When none
do, the manifest stays, and with it its layers, until chalkctl registry delete
<repository>@<digest>. A digest takes the manifest. Before it deletes anything, chalkctl refuses a
digest that tags name unless --with-tags is given, and then prints each tag it deleted with it;
it always refuses a digest that a tagged index names, as the index would break. The layers stay
on disk until chalkctl registry gc.`

// registryDeleteConfirms says when registry delete asks, for its Meta.
const registryDeleteConfirms = "asks on the terminal before it deletes a digest together with the tags that name it, as --with-tags allows"

const registryDeleteExample = `  # Take the tag away; the manifest stays.
  chalkctl registry delete docker.io/library/busybox:1.36.1

  # Delete the manifest and every tag that names it.
  chalkctl registry delete docker.io/library/busybox@sha256:7b3ccabffc97de872a30dfd234fd972a66d247c8cfc69b0550f276481852627c --with-tags`

const registryGCLong = `Collects garbage on every endpoint of the cluster's ClusterRegistry, one after the other: each
removes the layers and configs that no manifest references, keeps every manifest, tagged or not,
and reports the space it freed. Nothing collects garbage on its own, so run the command after
registry delete of a digest; deleting a tag frees nothing, since its manifest stays.

While an endpoint collects, it serves pulls and refuses pushes and deletes, and a push whose
layers went up before the collection started may fail on them, so run the command when no push
is in progress. An endpoint that collects already says so, and the command fails for it. A
collection runs to its end on the endpoint once begun: Ctrl-C stops chalkctl, not the
collection.`

const registryGCExample = `  chalkctl registry delete docker.io/library/busybox@sha256:7b3ccabffc97de872a30dfd234fd972a66d247c8cfc69b0550f276481852627c --with-tags
  chalkctl registry gc`

const registryListLong = `Lists the repositories, tags and digests of every endpoint of the cluster's ClusterRegistry, with
the endpoints that serve each. A tag's digest is the one most endpoints serve; the command marks
an endpoint that lacks the tag, serves it with another digest, or lacks images of a tagged index,
which would fail a pull of that platform. Nothing copies images between the endpoints, so the
command exits 1 when any is marked or an endpoint cannot be listed; a push of the image to every
endpoint mends it. Images pushed by digest alone, without a tag, are not listed.

The registry serves reads to anyone, but chalkctl verifies the endpoints by the OS CA of a client
file or the secrets file, so the command takes one of them.

-o json prints the listing as an object: endpoints holds the endpoints asked, failed those that
could not be listed, and images each tag with its repository, tag and whole digest, the endpoints
serving it, those missing it, those that differ with their digest, and those broken with how many
images of the index they lack. It exits as the table does.`

const registryListExample = `  chalkctl registry list`
