package backend

import "testing"

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
