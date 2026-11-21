package uki

import (
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// policyFile is the module policy's file in the UKI's first initrd (modulepolicy.PolicyFile).
const policyFile = "etc/chalkos/module-policy"

// PolicySlot finds the image's module policy file in the first archive of a UKI's .initrd
// section, an uncompressed newc cpio the image build puts first, and returns the file offset and
// size of its data. ok is false for a UKI without one, such as an image built without the
// module policy.
func PolicySlot(r io.ReaderAt) (offset, size int64, ok bool, err error) {
	f, err := pe.NewFile(r)
	if err != nil {
		return 0, 0, false, err
	}
	s := f.Section(".initrd")
	if s == nil {
		return 0, 0, false, nil
	}
	end := int64(min(s.VirtualSize, s.Size))
	pos := int64(0)
	for pos+110 <= end {
		h := make([]byte, 110)
		if _, err := s.ReadAt(h, pos); err != nil {
			return 0, 0, false, err
		}
		if string(h[:6]) != "070701" {
			// The first archive is not a newc cpio, such as a compressed initrd alone.
			return 0, 0, false, nil
		}
		field := func(i int) (int64, error) {
			return strconv.ParseInt(string(h[6+8*i:14+8*i]), 16, 64)
		}
		fileSize, err1 := field(6)
		nameSize, err2 := field(11)
		if err := errors.Join(err1, err2); err != nil || nameSize < 1 {
			return 0, 0, false, fmt.Errorf("the UKI's initrd has a broken cpio header at %d", pos)
		}
		name := make([]byte, nameSize-1)
		if _, err := s.ReadAt(name, pos+110); err != nil {
			return 0, 0, false, err
		}
		data := align4(pos + 110 + nameSize)
		switch string(name) {
		case "TRAILER!!!":
			return 0, 0, false, nil
		case policyFile:
			if data+fileSize > end {
				return 0, 0, false, errors.New("the module policy runs past the UKI's initrd")
			}
			return int64(s.Offset) + data, fileSize, true, nil
		}
		pos = align4(data + fileSize)
	}
	return 0, 0, false, nil
}

func align4(n int64) int64 { return (n + 3) &^ 3 }
