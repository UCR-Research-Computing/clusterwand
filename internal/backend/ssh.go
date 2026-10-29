package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UCR-Research-Computing/clusterwand/internal/config"
)

// SSH runs commands on the login node over one multiplexed ssh connection.
type SSH struct {
	cfg  config.SSH
	mu   sync.Mutex
	argv []string // ssh argv without destination
	dest string
	// pin, when set, is the known_hosts file holding the only host keys ssh
	// may accept (F3: a site profile's host_keys), under alias pinAlias.
	pin, pinAlias string
}

// Pin makes ssh accept only keys for the login node: they are written to
// dir/<site> (0600) and ssh is told to check against that file alone, under
// the alias clusterwand-<site>, refusing any other key. Call before first use.
func (s *SSH) Pin(site string, keys []string, dir string) error {
	if len(keys) == 0 {
		return errors.New("no host keys to pin")
	}
	if strings.ContainsAny(site, "/\x00 \n") || site == "" || site == "." || site == ".." {
		return fmt.Errorf("site name %q cannot name a known_hosts file", site)
	}
	alias := "clusterwand-" + site
	var b strings.Builder
	for _, k := range keys {
		if strings.ContainsAny(k, "\n\r\x00") || strings.Count(k, " ") != 1 {
			return fmt.Errorf("host key %.40q is not \"<type> <base64>\"", k)
		}
		b.WriteString(alias + " " + k + "\n")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f := filepath.Join(dir, site)
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.pin, s.pinAlias, s.argv = f, alias, nil
	s.mu.Unlock()
	return nil
}

// Pinned is the known_hosts file in force ("" when the host key is not pinned).
func (s *SSH) Pinned() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pin
}

// NewSSH returns an SSH backend; the connection is set up on first use.
func NewSSH(cfg config.SSH) *SSH { return &SSH{cfg: cfg} }

// Name is the backend label used in `source`.
func (s *SSH) Name() string {
	return "ssh:" + s.cfg.Host
}

func (s *SSH) base(ctx context.Context) ([]string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.argv != nil {
		return s.argv, s.dest, nil
	}
	if s.cfg.Host == "" {
		return nil, "", fmt.Errorf("%w: ssh.host is not set", ErrUnreachable)
	}
	argv, dest := []string{"ssh"}, s.cfg.Host
	dir := controlDir(os.Getenv("XDG_RUNTIME_DIR"), os.TempDir())
	persist := s.cfg.ControlPersist
	if persist <= 0 {
		persist = 900
	}
	timeout := s.cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 30
	}
	prefix := "cw-"
	if s.pin != "" {
		// a pinned connection never reuses an unpinned master (and vice versa);
		// command-line options win over ~/.ssh/config
		prefix = "ck-"
		argv = append(argv,
			"-o", "StrictHostKeyChecking=yes",
			"-o", "UserKnownHostsFile=\""+s.pin+"\"",
			"-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "HostKeyAlias="+s.pinAlias,
			"-o", "UpdateHostKeys=no",
			"-o", "CheckHostIP=no",
		)
	}
	argv = append(argv,
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+filepath.Join(dir, prefix+"%C"),
		"-o", fmt.Sprintf("ControlPersist=%d", persist),
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=30",
		"-o", fmt.Sprintf("ConnectTimeout=%d", timeout),
		"-T",
	)
	s.argv, s.dest = argv, dest
	return argv, dest, nil
}

// Run executes c on the login node.
func (s *SSH) Run(ctx context.Context, c Command) ([]byte, error) {
	argv, dest, err := s.base(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout(c))
	defer cancel()
	full := append(append([]string{}, argv[1:]...), "--", dest, RemoteLine(c))
	cmd := exec.CommandContext(ctx, argv[0], full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.merge {
		cmd.Stderr = &stdout
	}
	if c.stdin != nil {
		cmd.Stdin = bytes.NewReader(c.stdin)
	}
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s timed out after %s", c.argv[0], Timeout(c))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			if code == 255 { // ssh itself failed; rebuild next time
				s.mu.Lock()
				s.argv = nil
				s.mu.Unlock()
				return nil, fmt.Errorf("%w: %s", ErrUnreachable, lastLine(stderr.String()))
			}
			if okExit(c, code) {
				return stdout.Bytes(), nil
			}
			msg := stderr.String()
			if c.merge {
				msg = stdout.String()
			}
			return stdout.Bytes(), fmt.Errorf("%s exited %d: %s", c.argv[0], code, lastLine(msg))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300]
	}
	if l == "" {
		return "no error text"
	}
	return l
}

// maxSocketPath is the shortest Unix socket path limit in use (macOS: 104
// bytes including the NUL; Linux allows 108).
const maxSocketPath = 103

// controlDir picks the folder for the SSH control socket. ssh names it
// <dir>/cw-<40 hex> and, while creating it, adds a 17-character random suffix;
// macOS's per-user TMPDIR (/var/folders/...) is too long for that, so a
// folder that would overflow the socket limit falls back to /tmp.
func controlDir(runtime, tmp string) string {
	for _, d := range []string{runtime, tmp} {
		d = strings.TrimSuffix(d, "/")
		if d != "" && len(d)+len("/cw-")+40+17 <= maxSocketPath {
			return d
		}
	}
	return "/tmp"
}
