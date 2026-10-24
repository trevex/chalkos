package node

// Membership is where a control-plane node stands in etcd, as its markers on STATE record it.
type Membership int

const (
	// MembershipNone is a node that was never bootstrapped and never started joining.
	MembershipNone Membership = iota
	// MembershipJoining is a node that started joining the cluster and has not finished: it may
	// be an etcd member already.
	MembershipJoining
	// MembershipBootstrapped is an etcd member: a node that initialised etcd or finished joining.
	MembershipBootstrapped
	// MembershipLeft is a node that left etcd. It joins the cluster again only once reinstalled.
	MembershipLeft
)

func (m Membership) String() string {
	switch m {
	case MembershipNone:
		return "none"
	case MembershipJoining:
		return "joining"
	case MembershipBootstrapped:
		return "bootstrapped"
	case MembershipLeft:
		return "left"
	}
	return "unknown"
}

// Leftovers are what etcd membership leaves on the node besides its markers.
type Leftovers struct {
	// InitialCluster is set when the node recorded the initial cluster its etcd joins with.
	InitialCluster bool
	// Pinned is set when the node is pinned to the addresses etcd advertises.
	Pinned bool
	// EtcdData is set when etcd's data directory holds data.
	EtcdData bool
	// EtcdDataMissing is set when etcd's data directory is empty although etcd was initialised,
	// as after VAR was reset.
	EtcdDataMissing bool
}

// ReadMembership reads the node's membership from its markers, and what membership left behind.
//   - The left marker wins: a leave records it before it clears the other markers.
//   - The bootstrapped marker wins over the joining one: a join sets it before it removes the
//     joining marker.
func ReadMembership(p Paths) (Membership, Leftovers, error) {
	var l Leftovers
	left, err := Left(p)
	if err != nil {
		return MembershipNone, l, err
	}
	bootstrapped, err := Bootstrapped(p)
	if err != nil {
		return MembershipNone, l, err
	}
	joining, err := Joining(p)
	if err != nil {
		return MembershipNone, l, err
	}
	if l.InitialCluster, err = HasInitialCluster(p); err != nil {
		return MembershipNone, l, err
	}
	if l.Pinned, err = Pinned(p); err != nil {
		return MembershipNone, l, err
	}
	if l.EtcdData, err = EtcdHasData(p); err != nil {
		return MembershipNone, l, err
	}
	initialised, err := EtcdInitialised(p)
	if err != nil {
		return MembershipNone, l, err
	}
	l.EtcdDataMissing = initialised && !l.EtcdData
	switch {
	case left:
		return MembershipLeft, l, nil
	case bootstrapped:
		return MembershipBootstrapped, l, nil
	case joining:
		return MembershipJoining, l, nil
	}
	return MembershipNone, l, nil
}

// Pinned reports whether the node is pinned to the addresses etcd advertises.
func Pinned(p Paths) (bool, error) {
	return marked(p.Pin())
}
