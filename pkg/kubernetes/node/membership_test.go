package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMembership(t *testing.T) {
	const (
		left           = "left"
		bootstrapped   = "bootstrapped"
		joining        = "joining"
		initialCluster = "initial-cluster"
		pin            = "pin"
		etcdData       = "etcd-data"
		initialised    = "etcd-initialised"
	)
	for name, tc := range map[string]struct {
		markers   []string
		want      Membership
		leftovers Leftovers
	}{
		"fresh":                       {nil, MembershipNone, Leftovers{}},
		"initial cluster only":        {[]string{initialCluster}, MembershipNone, Leftovers{InitialCluster: true}},
		"pinned only":                 {[]string{pin}, MembershipNone, Leftovers{Pinned: true}},
		"etcd data only":              {[]string{etcdData}, MembershipNone, Leftovers{EtcdData: true}},
		"joining":                     {[]string{joining}, MembershipJoining, Leftovers{}},
		"joining with what it wrote":  {[]string{joining, initialCluster, pin, etcdData}, MembershipJoining, Leftovers{InitialCluster: true, Pinned: true, EtcdData: true}},
		"bootstrapped":                {[]string{bootstrapped, pin, etcdData, initialised}, MembershipBootstrapped, Leftovers{Pinned: true, EtcdData: true}},
		"bootstrapped before etcd":    {[]string{bootstrapped, pin}, MembershipBootstrapped, Leftovers{Pinned: true}},
		"etcd data missing":           {[]string{bootstrapped, pin, initialised}, MembershipBootstrapped, Leftovers{Pinned: true, EtcdDataMissing: true}},
		"joined, joining not removed": {[]string{bootstrapped, joining, initialCluster, pin}, MembershipBootstrapped, Leftovers{InitialCluster: true, Pinned: true}},
		"left":                        {[]string{left}, MembershipLeft, Leftovers{}},
		"left before clearing":        {[]string{left, bootstrapped, pin, etcdData, initialised}, MembershipLeft, Leftovers{Pinned: true, EtcdData: true}},
		"left a join":                 {[]string{left, joining, initialCluster}, MembershipLeft, Leftovers{InitialCluster: true}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			p := Paths{State: filepath.Join(root, "state"), EtcdData: filepath.Join(root, "etcd")}
			files := map[string]string{
				left:           p.Left(),
				bootstrapped:   p.Bootstrapped(),
				joining:        p.Joining(),
				initialCluster: p.EtcdInitialCluster(),
				pin:            p.Pin(),
				etcdData:       filepath.Join(p.EtcdData, "member", "snap", "db"),
				initialised:    p.EtcdInitialised(),
			}
			for _, m := range tc.markers {
				write(t, files[m], "x\n")
			}
			got, leftovers, err := ReadMembership(p)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || leftovers != tc.leftovers {
				t.Errorf("got %v %+v, want %v %+v", got, leftovers, tc.want, tc.leftovers)
			}
		})
	}
}

func TestReadMembershipUnreadable(t *testing.T) {
	root := t.TempDir()
	// A marker below a file cannot be stat'ed: the error is not that it does not exist.
	write(t, filepath.Join(root, "state"), "")
	p := Paths{State: filepath.Join(root, "state"), EtcdData: filepath.Join(root, "etcd")}
	if _, _, err := ReadMembership(p); err == nil {
		t.Fatal("unreadable markers: no error")
	}
	if err := os.Remove(p.State); err != nil {
		t.Fatal(err)
	}
	if m, _, err := ReadMembership(p); err != nil || m != MembershipNone {
		t.Fatalf("no state: %v, %v", m, err)
	}
}

func TestPinned(t *testing.T) {
	p := Paths{State: t.TempDir()}
	if pinned, err := Pinned(p); err != nil || pinned {
		t.Errorf("not pinned: %v, %v", pinned, err)
	}
	write(t, p.Pin(), "192.168.100.11\n")
	if pinned, err := Pinned(p); err != nil || !pinned {
		t.Errorf("pinned: %v, %v", pinned, err)
	}
}
