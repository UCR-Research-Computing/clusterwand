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
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	persist := s.cfg.ControlPersist
	if persist <= 0 {
		persist = 900
	}
	timeout := s.cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 30
	}
	argv = append(argv,
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+filepath.Join(dir, "cw-%C"),
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
