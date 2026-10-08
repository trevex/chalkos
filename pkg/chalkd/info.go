package chalkd

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/storage"
)

// globalVariable is the EFI vendor GUID of SecureBoot and SetupMode.
const globalVariable = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

func (s *Server) Info(ctx context.Context, _ *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	release := readOSRelease(s.Paths.OSRelease)
	resp := &nodev1.InfoResponse{
		Mode:        s.Mode,
		Version:     release["IMAGE_VERSION"],
		ImageId:     release["IMAGE_ID"],
		Installer:   s.Installer,
		SecureBoot:  s.secureBoot(),
		Fingerprint: s.CurrentFingerprint(),
	}
	if boot, err := s.Host.ResolvePath(s.Paths.BootDisk); err == nil {
		resp.BootDisk = boot.Device
	}
	if entries, err := os.ReadDir(s.Paths.TPM); err == nil && len(entries) > 0 {
		resp.Tpm = true
	}
	resp.Hostname, _ = os.Hostname()
	return connect.NewResponse(resp), nil
}

func (s *Server) secureBoot() nodev1.SecureBoot {
	read := func(name string) (byte, bool) {
		data, err := os.ReadFile(filepath.Join(s.Paths.EFIVars, name+"-"+globalVariable))
		// Four bytes of attributes precede the value.
		if err != nil || len(data) < 5 {
			return 0, false
		}
		return data[4], true
	}
	if setup, ok := read("SetupMode"); ok && setup == 1 {
		return nodev1.SecureBoot_SECURE_BOOT_SETUP_MODE
	}
	if enabled, ok := read("SecureBoot"); ok && enabled == 1 {
		return nodev1.SecureBoot_SECURE_BOOT_ENABLED
	}
	return nodev1.SecureBoot_SECURE_BOOT_DISABLED
}

func readOSRelease(path string) map[string]string {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return values
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			values[k] = strings.Trim(v, `"`)
		}
	}
	return values
}

func (s *Server) Disks(ctx context.Context, _ *connect.Request[nodev1.DisksRequest]) (*connect.Response[nodev1.DisksResponse], error) {
	disks, err := s.Host.Disks()
	if err != nil {
		return nil, failed(connect.CodeInternal, "list disks: %v", err)
	}
	boot, bootErr := s.Host.ResolvePath(s.Paths.BootDisk)
	pins, err := storage.ReadPins(filepath.Join(s.Paths.StateDir, "storage", "disks.json"))
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	resp := &nodev1.DisksResponse{}
	for _, d := range disks {
		disk := &nodev1.Disk{
			Name:   d.Name,
			Device: d.Device,
			Model:  d.Identity.Model,
			Size:   d.Identity.Size,
			Serial: d.Identity.Serial,
			Wwn:    d.Identity.WWN,
			Type:   d.Identity.Type,
			Path:   d.Identity.Path,
		}
		switch {
		case bootErr == nil && d.Name == boot.Name:
			disk.Usage = "boot"
		default:
			for _, name := range sortedNames(pins.Disks) {
				if name != storage.SystemDisk && pins.Disks[name].Identity.Same(d.Identity) {
					disk.Usage = "disk " + name
				}
			}
		}
		parts, err := s.Host.Partitions(d)
		if err != nil {
			return nil, failed(connect.CodeInternal, "partitions of %s: %v", d.Device, err)
		}
		for _, p := range parts {
			disk.Partitions = append(disk.Partitions, &nodev1.Partition{
				Device:  p.Device,
				Number:  uint32(p.Number),
				Size:    p.Size,
				Type:    p.Type,
				Label:   p.Label,
				Uuid:    p.UUID,
				Content: p.Content,
			})
		}
		resp.Disks = append(resp.Disks, disk)
	}
	return connect.NewResponse(resp), nil
}

// CurrentFingerprint returns the SHA-256 of the certificate chalkd serves now, which changes
// when the node certificate is renewed.
func (s *Server) CurrentFingerprint() string {
	if s.Certificate != nil {
		return s.Certificate.Fingerprint()
	}
	return s.Fingerprint
}
