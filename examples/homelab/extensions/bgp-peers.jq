# The validator bgp-peers: it reads the node's Peer, Speaker and Network documents, by kind.
# bird binds each session of a Peer to one of the Peer's interfaces, and announces the addresses
# of the Speaker's interfaces. Neither works on an interface that networkd leaves down, so the
# validator refuses an interface that no Network document names. It compares the names as
# written: a glob such as fabric* in a Network may or may not match an interface, so it names
# none. It also refuses Peer documents without a Speaker, which gives bird its AS number and
# router ID.

# A value as a list: a section or key of a networkd document is one value or a list of them.
def each: if type == "array" then .[] else . end;

# The names the [Match] sections of the Network documents give, where one string may hold
# several, separated by spaces.
[ .documents.Network[] | .spec.match? // [] | each | .name? // [] | each
  | strings | splits(" +") | select(. != "") ] as $names

# A refusal of each item of a list of interfaces that no Network names, as the field path names
# the list.
| def unconfigured($document; $path; $why):
    to_entries[] | select(.value as $name | $names | index([$name]) | not)
    | { kind: $document.kind, name: $document.metadata.name, path: "\($path)[\(.key)]",
        message: "\(.value) has no Network document; \($why)" };

{ refusals: [
    ( .documents.Peer[] | . as $peer | .spec.interfaces
      | unconfigured($peer; "spec.interfaces"; "bird binds the session to an interface the node configures") ),
    ( .documents.Speaker[] | . as $speaker | .spec.export.interfaces
      | unconfigured($speaker; "spec.export.interfaces"; "bird announces the addresses of an interface the node configures") ),
    ( select(.documents.Peer != [] and .documents.Speaker == [])
      | { kind: "Peer", name: .documents.Peer[0].metadata.name, path: "metadata.name",
          message: "a node with Peer documents needs a Speaker document, which gives bird its AS number and router ID" } )
  ] }
