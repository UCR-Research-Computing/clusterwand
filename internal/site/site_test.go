package site

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedUrsa(t *testing.T) {
	p, err := Embedded("ursa-major")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model() != ChargeUSDPerNodeHour || p.Unit() != "USD" || p.Catalog != "/apps/docs/catalog.json" || p.Partitions.Probe != "check" || !p.CloudNodes {
		t.Fatalf("profile: %+v", p)
	}
	if Dir(p.Paths.Home, "x") != "/home/x" || Dir(p.Paths.Scratch, "x") != "/scratch/x" {
		t.Errorf("paths: %+v", p.Paths)
	}
	if !p.AVX2Only("standard") || p.AVX2Only("computehigh") || p.CPUNote("spot") != "AVX2 only (e2)" || p.CPUNote("gpul4") != "AVX-512" {
		t.Error("cpu notes")
	}
	if !p.IsStockout("ZONE_RESOURCE_POOL_EXHAUSTED") || p.IsStockout("Not responding") {
		t.Error("stockout reasons")
	}
	if got := Names(); len(got) == 0 || got[0] != "ursa-major" {
		t.Errorf("names: %v", got)
	}
}

func TestGenericHasNoSite(t *testing.T) {
	g := Generic()
	if g.Model() != ChargeNone || g.Unit() != "" || g.Catalog != "" || g.Partitions.Probe != "" || g.CloudNodes {
		t.Fatalf("generic: %+v", g)
	}
	if Dir(g.Paths.Home, "bob") != "/home/bob" || len(g.Paths.LogRoots) != 1 || g.Paths.LogRoots[0] != "/home/{user}/" {
		t.Errorf("generic paths: %+v", g.Paths)
	}
	if g.Text("k", "def {help}") != "def the cluster's support team" {
		t.Error(g.Text("k", "def {help}"))
	}
}

func TestTextPlaceholders(t *testing.T) {
	p := &Profile{Help: "the desk", Title: "Big", Partitions: Partitions{Probe: "debug", GPU: "a100"},
		Texts: map[string]string{"x": "ask {help} on {title}, probe {probe}, gpu {gpu}, {n}"}}
	if got := p.Text("x", "", "{n}", "7"); got != "ask the desk on Big, probe debug, gpu a100, 7" {
		t.Error(got)
	}
	if got := p.Text("missing", "default"); got != "default" {
		t.Error(got)
	}
}

// Validation guards: every rule a profile can break.
func TestValidate(t *testing.T) {
	ok := "name: s\napproved: true\n"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("minimal profile refused: %v", err)
	}
	for name, body := range map[string]string{
		"unapproved":        "name: s\n",
		"approved false":    "name: s\napproved: false\n",
		"bad name":          "name: S_1\napproved: true\n",
		"relative catalog":  ok + "catalog: docs/catalog.json\n",
		"unclean catalog":   ok + "catalog: /a/../catalog.json\n",
		"non-json catalog":  ok + "catalog: /a/catalog.yaml\n",
		"home without user": ok + "paths: {home: /home}\n",
		"relative scratch":  ok + "paths: {scratch: \"scratch/{user}\"}\n",
		"log root no slash": ok + "paths: {log_roots: [\"/home/{user}\"]}\n",
		"log root no user":  ok + "paths: {log_roots: [\"/tmp/\"]}\n",
		"bad probe":         ok + "partitions: {probe: \"check; id\"}\n",
		"bad fallback":      ok + "partitions: {fallback_cold: [\"-w n1\"]}\n",
		"bad rate name":     ok + "charge: {model: usd_per_node_hour, rates: {\"a b\": 1}}\n",
		"bad model":         ok + "charge: {model: euros}\n",
		"su without rates":  ok + "charge: {model: su_per_core_hour}\n",
		"negative rate":     ok + "charge: {model: su_per_core_hour, rates: {cpu: -1}}\n",
		"unknown field":     ok + "partitons: {}\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name != "unknown field" && strings.Contains(err.Error(), "yaml:") {
			t.Errorf("%s: refused for YAML syntax, not by the guard: %v", name, err)
		}
	}
}

func TestFileAndEmbeddedErrors(t *testing.T) {
	if _, err := Embedded("../etc"); err == nil {
		t.Error("path in site name accepted")
	}
	if _, err := Embedded("no-such-site"); err == nil || !strings.Contains(err.Error(), "ursa-major") {
		t.Errorf("unknown site: %v", err)
	}
	p := filepath.Join(t.TempDir(), "s.yaml")
	_ = os.WriteFile(p, []byte("name: mine\napproved: true\npaths: {home: \"/u/{user}\", scratch: \"/s/{user}\"}\n"), 0o600)
	sp, err := File(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sp.Paths.LogRoots, ",") != "/u/{user}/,/s/{user}/" {
		t.Errorf("default log roots: %v", sp.Paths.LogRoots)
	}
}
