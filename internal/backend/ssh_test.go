package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UCR-Research-Computing/clusterwand/internal/config"
)

// The control socket must fit the Unix socket limit: macOS's long per-user
// TMPDIR falls back to /tmp.
func TestControlDirFitsSocketLimit(t *testing.T) {
	mac := "/var/folders/cx/b1gzbdc14x7f6km0l6_fyq1r0000gp/T/"
	for _, c := range []struct{ runtime, tmp, want string }{
		{"/run/user/1000", mac, "/run/user/1000"},
		{"", mac, "/tmp"},
		{"", "/tmp/", "/tmp"},
		{"", "/var/tmp", "/var/tmp"},
		{"", "", "/tmp"},
	} {
		if got := controlDir(c.runtime, c.tmp); got != c.want {
			t.Errorf("controlDir(%q, %q) = %q, want %q", c.runtime, c.tmp, got, c.want)
		}
	}
}

// A pinned site checks the host key against its own file only, strictly.
func TestPinHostKeys(t *testing.T) {
	s := NewSSH(config.SSH{Host: "login"})
	dir := t.TempDir()
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	if err := s.Pin("lab", []string{key}, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "lab"))
	if err != nil || string(b) != "clusterwand-lab "+key+"\n" {
		t.Fatalf("known_hosts %q %v", b, err)
	}
	argv, _, err := s.base(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(argv, " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=\"" + filepath.Join(dir, "lab") + "\"", "GlobalKnownHostsFile=/dev/null", "HostKeyAlias=clusterwand-lab", "/ck-%C"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q: %s", want, line)
		}
	}
	for _, bad := range [][]string{nil, {key + "\nProxyCommand x"}, {"ssh-ed25519"}} {
		if err := NewSSH(config.SSH{Host: "h"}).Pin("lab", bad, dir); err == nil {
			t.Errorf("Pin(%q) accepted", bad)
		}
	}
	if err := NewSSH(config.SSH{Host: "h"}).Pin("../x", []string{key}, dir); err == nil {
		t.Error("site name with a path accepted")
	}
	u := NewSSH(config.SSH{Host: "h"})
	argv, _, _ = u.base(context.Background())
	if strings.Contains(strings.Join(argv, " "), "StrictHostKeyChecking") {
		t.Error("unpinned connection changed host-key checking")
	}
}
