package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/config"
)

// F1 site profiles: the config picks a profile; the profile fills paths,
// catalog, probe partition and the charge model.

func TestResolveSite(t *testing.T) {
	// a cluster name matching a shipped profile selects it
	c := config.Default()
	c.Cluster = "ursa-major"
	c, sp, err := ResolveSite(c)
	if err != nil || sp.Name != "ursa-major" || c.CatalogPath != "/apps/docs/catalog.json" || c.EnvPartition != "check" ||
		strings.Join(c.LogRoots, ",") != "/home/{user}/,/scratch/{user}/" {
		t.Fatalf("by cluster name: %+v %v %v", c, sp, err)
	}
	// config values win over the profile
	c = config.Default()
	c.Site, c.CatalogPath, c.EnvPartition, c.LogRoots = "ursa-major", "/x/c.json", "debug", []string{"/data/{user}/"}
	c, _, _ = ResolveSite(c)
	if c.CatalogPath != "/x/c.json" || c.EnvPartition != "debug" || c.LogRoots[0] != "/data/{user}/" || c.Cluster != "ursa-major" {
		t.Errorf("overrides lost: %+v", c)
	}
	// nothing set: the generic profile (no catalog, no probe, home only)
	c, sp, err = ResolveSite(config.Default())
	if err != nil || sp.Name != "generic" || c.CatalogPath != "" || c.EnvPartition != "" || strings.Join(c.LogRoots, ",") != "/home/{user}/" {
		t.Errorf("generic: %+v %+v %v", c, sp, err)
	}
	// unknown or unapproved profiles are errors, never silently generic
	c = config.Default()
	c.Site = "nope"
	if _, _, err := ResolveSite(c); err == nil {
		t.Error("unknown site accepted")
	}
	p := filepath.Join(t.TempDir(), "s.yaml")
	_ = os.WriteFile(p, []byte("name: draft\n"), 0o600)
	c = config.Default()
	c.SiteFile = p
	if _, _, err := ResolveSite(c); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("unapproved site: %v", err)
	}
	c.Backend, c.FixturesDir = "fixture", "x"
	if _, err := New(c); err == nil {
		t.Error("New accepted an unapproved site")
	}
}

// siteService is a service over the Ursa fixtures with another site profile
// (same catalog file, different charge model).
func siteService(t *testing.T, profile string) (*Service, *backend.Fixture) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(p, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SiteFile = p
	cfg.Backend = "fixture"
	cfg.FixturesDir = "../../testdata"
	cfg.ClusterUser = "alice_ucr_edu"
	cfg.Tiers = []string{"R1", "R2", "A1"}
	cfg.AuditPath = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.StatePath = filepath.Join(t.TempDir(), "a1.json")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Unix(1790875401, 0) }
	return s, s.Backend.(*backend.Fixture)
}

const suSite = `name: su
approved: true
catalog: /apps/docs/catalog.json
paths: {home: "/home/{user}", scratch: "/scratch/{user}"}
partitions: {probe: check}
charge:
  model: su_per_core_hour
  rates: {computehigh: 2, standard: 1}
  max_su_per_job: 50
  max_su_per_day: 100
`

const script1h = "#!/bin/bash\n#SBATCH -p computehigh\n#SBATCH -N 1\n#SBATCH -t 01:00:00\nmodule load python-sci\npython3 x.py\n"

func TestSUChargeModel(t *testing.T) {
	ctx := context.Background()
	s, _ := siteService(t, suSite)
	// 22 cores x 2 SU/core-hour x 1 h = 44 SU, under the 50 SU job cap
	plan, err := s.PrepareSubmit(ctx, SubmitInput{Script: script1h})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Charge == nil || plan.Charge.Unit != "SU" || plan.Charge.WorstCase != 44 || plan.Charge.DayCap != 100 ||
		plan.WorstCaseUSD != 0 || plan.USDPerNodeH != 0 || plan.DayCapUSD != 0 {
		t.Fatalf("SU plan: %+v %+v", plan, plan.Charge)
	}
	// 2 h = 88 SU: over the job cap, in SU wording
	if _, err := s.PrepareSubmit(ctx, SubmitInput{Script: script1h, Time: "2:00:00"}); err == nil || !strings.Contains(err.Error(), "88.0 SU") || !strings.Contains(err.Error(), "50.0 SU per-job cap") {
		t.Errorf("SU job cap: %v", err)
	}
	// a partition without a rate cannot be priced: refused, never free
	if _, err := s.PrepareSubmit(ctx, SubmitInput{Script: strings.Replace(script1h, "computehigh", "highmem", 1)}); err == nil || !strings.Contains(err.Error(), "charge.rates") {
		t.Errorf("unpriced partition: %v", err)
	}
	// script_check carries the estimate in SU, never in the dollar field
	sc, err := s.ScriptCheck(ctx, script1h)
	if err != nil || sc.EstCostUSD != nil || sc.EstCharge == nil || *sc.EstCharge != 44 || sc.ChargeUnit != "SU" {
		t.Errorf("script_check: %+v %v", sc, err)
	}
	// usage: SU in est_charge, nothing in est_cost_usd
	u, err := s.Usage(ctx, UsageInput{Since: "2026-09-01"})
	if err != nil || u.ChargeUnit != "SU" || u.Total.CostUSD != 0 || u.Total.Charge <= 0 {
		t.Errorf("usage: %+v %v", u.Total, err)
	}
	// dollar views never show the SU figures as dollars
	ps, _ := s.Partitions(ctx)
	for _, p := range ps {
		if p.USD != nil {
			t.Errorf("partition %s shows dollars on an SU site", p.Name)
		}
	}
	// show_cost_tiers hide SU figures from R1 as they hide dollars
	s.Cfg.Tiers, s.Cfg.ShowCostTiers = []string{"R1"}, []string{"R2"}
	if sc, _ := s.ScriptCheck(ctx, script1h); sc.EstCharge != nil {
		t.Error("SU estimate shown to R1 despite show_cost_tiers")
	}
}

func TestSUSiteWithoutCapsRefusesSubmit(t *testing.T) {
	s, _ := siteService(t, strings.Replace(strings.Replace(suSite, "  max_su_per_job: 50\n", "", 1), "  max_su_per_day: 100\n", "", 1))
	if _, err := s.PrepareSubmit(context.Background(), SubmitInput{Script: script1h}); err == nil || !strings.Contains(err.Error(), "sets no SU caps") {
		t.Fatalf("no SU caps: %v", err)
	}
	// config caps fill the gap
	s.Cfg.Caps.MaxSUPerJob, s.Cfg.Caps.MaxSUPerDay = 60, 60
	if _, err := s.PrepareSubmit(context.Background(), SubmitInput{Script: script1h}); err != nil {
		t.Fatalf("config SU caps: %v", err)
	}
}

func TestNoChargeModel(t *testing.T) {
	ctx := context.Background()
	s, _ := siteService(t, "name: free\napproved: true\ncatalog: /apps/docs/catalog.json\npartitions: {probe: check}\n")
	plan, err := s.PrepareSubmit(ctx, SubmitInput{Script: script1h})
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorstCaseUSD != 0 || plan.Charge != nil || plan.DayCapUSD != 0 {
		t.Errorf("free plan carries a cost: %+v", plan)
	}
	// node and hour caps still hold on a free site
	if _, err := s.PrepareSubmit(ctx, SubmitInput{Script: script1h, Time: "30:00:00"}); err == nil {
		t.Error("hour cap not enforced on a site without charge")
	}
	b, _ := json.Marshal(mustStatus(t, s))
	if strings.Contains(string(b), "usd") {
		t.Errorf("status shows dollars without a charge model: %s", b)
	}
	u, _ := s.Usage(ctx, UsageInput{Since: "2026-09-01"})
	if u.Total.CostUSD != 0 || u.Total.Charge != 0 || strings.Contains(strings.Join(u.Notes, " "), "No price") {
		t.Errorf("usage on a free site: %+v %v", u.Total, u.Notes)
	}
}

func mustStatus(t *testing.T, s *Service) *ClusterStatus {
	t.Helper()
	r, err := Call(context.Background(), s, "test", "cluster_status", "R1", nil, false, s.ClusterStatus)
	if err != nil {
		t.Fatal(err)
	}
	return r.Data
}

// Site paths drive the file roots: another site's home is the only root.
func TestSitePathsAreTheRoots(t *testing.T) {
	s, fx := siteService(t, "name: p\napproved: true\npaths: {home: \"/u/{user}\"}\n")
	fx.Paths = map[string]string{"/u/alice_ucr_edu/a.txt": "hi", "/home/alice_ucr_edu/b.txt": "no", "/scratch/alice_ucr_edu/c.txt": "no"}
	ctx := context.Background()
	if _, err := s.FilesRead(ctx, FilesReadInput{Path: "~/a.txt"}); err != nil {
		t.Errorf("site home refused: %v", err)
	}
	for _, p := range []string{"/home/alice_ucr_edu/b.txt", "/scratch/alice_ucr_edu/c.txt"} {
		if _, err := s.FilesRead(ctx, FilesReadInput{Path: p}); err == nil {
			t.Errorf("%s read outside the site's roots", p)
		}
	}
	if err := s.checkLogPath("/scratch/alice_ucr_edu/x.log", "alice_ucr_edu"); err == nil {
		t.Error("log outside the site's log roots accepted")
	}
	// no probe partition: env_check refuses instead of guessing one
	if _, err := s.EnvCheck(ctx, nil, nil); err == nil || !strings.Contains(err.Error(), "never runs on the login node") {
		t.Errorf("env_check without a probe: %v", err)
	}
	// no catalog: tools that need it say so
	if _, err := s.Partitions(ctx); err == nil || !strings.Contains(err.Error(), "no cluster catalog") {
		t.Errorf("no catalog: %v", err)
	}
}
