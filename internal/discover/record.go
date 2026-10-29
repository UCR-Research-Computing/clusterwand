package discover

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func sortStrings(xs []string) { sort.Strings(xs) }

var userHome = os.UserHomeDir

// FakeUser replaces the real user name in recorded fixtures.
const FakeUser = "alice_ucr_edu"

var (
	// private IPv4 ranges (10/8, 172.16/12, 192.168/16, 100.64/10)
	rePrivateIP = regexp.MustCompile(`\b(10\.\d{1,3}|172\.(1[6-9]|2\d|3[01])|192\.168|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7]))\.\d{1,3}\.\d{1,3}\b`)
	// who may use a partition: account and group names are people's labs
	reAccess = regexp.MustCompile(`\b(AllowAccounts|DenyAccounts|AllowGroups|DenyQos|AllowQos)=(\S+)`)
	// "Reason=... [admin@1793000000]" and Owner= name a person
	reReasonBy = regexp.MustCompile(`\[[^\]@\s]+@`)
	reOwner    = regexp.MustCompile(`\bOwner=\S+`)
)

// Anonymize returns a copy of p fit to commit as a test fixture: the user name
// becomes FakeUser, private IP addresses 10.0.0.1, every account and group in a
// partition's access lists lab-a, lab-b ... (ALL stays), and the people named
// in node reasons and owners "admin". The scontrol config keys are already
// filtered (FilterConfig).
func Anonymize(p Probe) Probe {
	labs := map[string]string{}
	scrub := func(s string) string {
		if p.User != "" {
			s = regexp.MustCompile(`\b`+regexp.QuoteMeta(p.User)+`\b`).ReplaceAllString(s, FakeUser)
		}
		s = rePrivateIP.ReplaceAllString(s, "10.0.0.1")
		s = reAccess.ReplaceAllStringFunc(s, func(m string) string {
			k, v, _ := strings.Cut(m, "=")
			var out []string
			for _, a := range strings.Split(v, ",") {
				if a == "ALL" || a == "(null)" || a == "N/A" {
					out = append(out, a)
					continue
				}
				if _, ok := labs[a]; !ok {
					labs[a] = labName(len(labs))
				}
				out = append(out, labs[a])
			}
			return k + "=" + strings.Join(out, ",")
		})
		s = reReasonBy.ReplaceAllString(s, "[admin@")
		s = reOwner.ReplaceAllStringFunc(s, func(m string) string {
			if m == "Owner=N/A" {
				return m
			}
			return "Owner=admin"
		})
		return s
	}
	out := Probe{Host: "login", User: FakeUser, Version: p.Version, Partitions: scrub(p.Partitions),
		Nodes: scrub(p.Nodes), Config: scrub(FilterConfig(p.Config)), Env: scrub(p.Env), Modules: scrub(p.Modules)}
	return out
}

// labName is lab-a ... lab-z, then lab-27, lab-28 ...
func labName(i int) string {
	if i < 26 {
		return "lab-" + string(rune('a'+i))
	}
	return "lab-" + strconv.Itoa(i+1)
}

// Files are the recorded probe files, in a fixed order.
var Files = []string{"version.txt", "partitions.txt", "nodes.txt", "config.txt", "env.txt", "modules.txt"}

// ToFiles maps a probe to file contents (see Files).
func (p Probe) ToFiles() map[string]string {
	return map[string]string{"version.txt": p.Version, "partitions.txt": p.Partitions, "nodes.txt": p.Nodes,
		"config.txt": p.Config, "env.txt": p.Env, "modules.txt": p.Modules}
}

// FromFiles reads a recorded probe back (user is the recorded FakeUser).
func FromFiles(read func(name string) (string, error)) (Probe, error) {
	p := Probe{Host: "login", User: FakeUser}
	dst := map[string]*string{"version.txt": &p.Version, "partitions.txt": &p.Partitions, "nodes.txt": &p.Nodes,
		"config.txt": &p.Config, "env.txt": &p.Env, "modules.txt": &p.Modules}
	for _, f := range Files {
		s, err := read(f)
		if err != nil {
			return p, err
		}
		*dst[f] = s
	}
	return p, nil
}
