package discover

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"

	"github.com/UCR-Research-Computing/clusterwand/internal/site"
)

// Runner runs a local program and returns its stdout (tests replace it).
type Runner func(name string, args ...string) ([]byte, error)

// LocalRunner runs local programs (ssh -G, ssh-keygen -F); never anything remote.
func LocalRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// HostKeys returns the host keys the person's own OpenSSH already trusts for
// host: `ssh -G host` gives the name ssh checks (HostKeyAlias, else HostName and
// Port) and the known_hosts files, `ssh-keygen -F` finds the entries. Revoked
// and @cert-authority lines are skipped. Keys come back as "<type> <base64>",
// sorted and de-duplicated. Discover calls this only after a successful
// BatchMode connection, so the node showed one of these keys.
func HostKeys(run Runner, host string) ([]string, error) {
	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\n\x00") || host == "" {
		return nil, fmt.Errorf("ssh host %q is not a host name or alias", host)
	}
	out, err := run("ssh", "-G", "--", host)
	if err != nil {
		return nil, fmt.Errorf("ssh -G %s: %w", host, err)
	}
	cfg := map[string]string{}
	var files []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		switch k {
		case "userknownhostsfile", "globalknownhostsfile":
			files = append(files, strings.Fields(v)...)
		default:
			cfg[k] = v
		}
	}
	name := cfg["hostkeyalias"]
	if name == "" {
		name = cfg["hostname"]
	}
	if p := cfg["port"]; p != "" && p != "22" && cfg["hostkeyalias"] == "" {
		name = "[" + name + "]:" + p
	}
	if name == "" {
		return nil, fmt.Errorf("ssh -G %s printed no host name", host)
	}
	seen := map[string]bool{}
	var keys []string
	for _, f := range files {
		if f == "/dev/null" || f == "none" {
			continue
		}
		f = expandHome(f)
		b, _ := run("ssh-keygen", "-F", name, "-f", f) // exit 1: not in this file
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "@") {
				continue
			}
			fs := strings.Fields(l)
			if len(fs) < 3 {
				continue
			}
			k := fs[1] + " " + fs[2]
			if site.ValidHostKey(k) == nil && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no trusted host key for %s (as %s) in %s; connect once with plain ssh and check the fingerprint first", host, name, strings.Join(files, ", "))
	}
	sortStrings(keys)
	return keys, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := userHome(); err == nil {
			return h + p[1:]
		}
	}
	return p
}
