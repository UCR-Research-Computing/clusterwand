package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discover --from replays a recorded probe: it writes an unapproved draft and
// catalog, refuses to overwrite, records anonymised fixtures, and the draft is
// refused by every tool until a person approves both files.
func TestDiscoverDraftAndReviewGate(t *testing.T) {
	td, _ := filepath.Abs("../../testdata")
	from := filepath.Join(td, "discover", "ursa-25.11")
	out := t.TempDir()
	rec := filepath.Join(t.TempDir(), "rec")
	stdout, stderr, code := runCLI(t, "discover", "--site", "lab", "--from", from, "--out", out, "--record", rec, "--compare", "ursa-major", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	var r discoverResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Parts != 13 || len(r.Compare) == 0 {
		t.Fatalf("result %+v err %v", r, err)
	}
	prof := filepath.Join(out, "lab", "site.yaml")
	catf := filepath.Join(out, "lab", "catalog.json")
	for _, f := range []string{"version.txt", "partitions.txt", "nodes.txt", "config.txt", "env.txt", "modules.txt"} {
		if _, err := os.Stat(filepath.Join(rec, f)); err != nil {
			t.Errorf("record: %v", err)
		}
	}
	// never overwrites
	if _, _, code := runCLI(t, "discover", "--site", "lab", "--from", from, "--out", out); code == 0 {
		t.Error("existing draft overwritten without --force")
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := "site_file: " + prof + "\nbackend: fixture\nfixtures_dir: " + td + "\ncluster_user: alice_ucr_edu\ntiers: [R1]\naudit_path: " + filepath.Join(dir, "a.jsonl") + "\nstate_path: " + filepath.Join(dir, "a1.json") + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// unapproved profile: refused
	if o, e, code := runCLI(t, "partitions", "--config", cfg); code == 0 || !strings.Contains(o+e, "not approved") {
		t.Fatalf("unapproved profile used: %d %s %s", code, o, e)
	}
	b, _ := os.ReadFile(prof)
	if err := os.WriteFile(prof, []byte(strings.Replace(string(b), "approved: false", "approved: true", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// approved profile, draft catalog: refused
	if o, e, code := runCLI(t, "modules", "gcc", "--config", cfg); code == 0 || !strings.Contains(o+e, "draft") {
		t.Fatalf("draft catalog used: %d %s %s", code, o, e)
	}
	// --force never overwrites an approved profile
	if _, _, code := runCLI(t, "discover", "--site", "lab", "--from", from, "--out", out, "--force"); code == 0 {
		t.Error("approved profile overwritten")
	}
	c, _ := os.ReadFile(catf)
	if err := os.WriteFile(catf, []byte(strings.Replace(string(c), `"approved": false`, `"approved": true`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	o, e, code := runCLI(t, "modules", "gcc", "--config", cfg)
	if code != 0 || !strings.Contains(o, "gcc") {
		t.Fatalf("approved draft not used: %d %s %s", code, o, e)
	}
}

func TestDiscoverArgs(t *testing.T) {
	for _, args := range [][]string{
		{"discover"},
		{"discover", "--site", "Bad_Name", "--host", "h"},
		{"discover", "--site", "ok"},
		{"discover", "--site", "ok", "--host", "-oProxyCommand=touch /tmp/x", "--out", t.TempDir()},
		{"discover", "--site", "ok", "--host", "h", "--from", "x"},
	} {
		if _, _, code := runCLI(t, args...); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestDiscoverConfigIsR1(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(p, []byte("site: ursa-major\nbackend: ssh\nssh: {host: other}\ntiers: [R1, R2, A1]\n"), 0o600)
	cfg, err := discoverConfig(p, "newsite")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Tiers, ",") != "R1" || cfg.SSH.Host != "newsite" || cfg.Site != "" || cfg.Backend != "ssh" {
		t.Errorf("config: %+v", cfg)
	}
	if _, err := discoverConfig(p, "-oProxyCommand=x"); err == nil || !strings.Contains(err.Error(), "not a host name") {
		t.Errorf("option as host: %v", err)
	}
}
