package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/UCR-Research-Computing/clusterwand/internal/config"
	"github.com/UCR-Research-Computing/clusterwand/internal/core"
)

// Golden tests (F1) for the CLI's text renderers: the same fixtures and fixed
// clock as internal/mcpserver's golden tests, rendered as people see them.
// Regenerate with: GOLDEN_UPDATE=1 go test ./cmd/clusterwand -run Golden

func init() {
	_ = time.Local.String()
	*time.Local = *time.FixedZone("EDT", -4*3600)
}

func goldenCLIService(t *testing.T, tiers ...string) *core.Service {
	t.Helper()
	cfg := config.Default()
	applyCLIGoldenSite(&cfg)
	cfg.Backend = "fixture"
	cfg.FixturesDir = "../../testdata"
	cfg.ClusterUser = "alice_ucr_edu"
	cfg.Tiers = tiers
	cfg.AuditPath = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.StatePath = filepath.Join(t.TempDir(), "a1.json")
	s, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Unix(1790875401, 0) }
	return s
}

func TestGoldenCLIText(t *testing.T) {
	ctx := context.Background()
	s := goldenCLIService(t, "R1", "R2", "A1")
	var b bytes.Buffer
	section := func(name string, fn func(w io.Writer) error) {
		b.WriteString("==== " + name + "\n")
		if err := fn(&b); err != nil {
			b.WriteString("error: " + err.Error() + "\n")
		}
	}
	section("usage", func(w io.Writer) error { _, err := io.WriteString(w, usage); return err })
	section("status", func(w io.Writer) error {
		r, err := core.Call(ctx, s, "cli", "cluster_status", "R1", nil, false, s.ClusterStatus)
		if err == nil {
			printStatus(w, r.Data)
		}
		return err
	})
	section("partitions", func(w io.Writer) error {
		r, err := core.Call(ctx, s, "cli", "partitions", "R1", nil, false, s.Partitions)
		if err == nil {
			printPartitions(w, r.Data)
		}
		return err
	})
	section("jobs", func(w io.Writer) error {
		js, err := s.JobsList(ctx, core.JobsListInput{Since: "now-30days"})
		if err == nil {
			printJobs(w, js, false)
		}
		return err
	})
	for _, id := range []string{"236", "212", "253"} {
		section("job show "+id, func(w io.Writer) error {
			d, err := s.JobShow(ctx, core.JobShowInput{JobID: id})
			if err == nil {
				printJob(w, d)
			}
			return err
		})
		section("job explain "+id, func(w io.Writer) error {
			e, err := s.JobExplain(ctx, id, false, 0)
			if err == nil {
				printExplain(w, e)
			}
			return err
		})
	}
	section("recipes", func(w io.Writer) error {
		cat, err := s.Catalog(ctx)
		if err == nil {
			printRecipes(w, cat.SearchRecipes("gromacs"))
		}
		return err
	})
	section("modules", func(w io.Writer) error {
		cat, err := s.Catalog(ctx)
		if err == nil {
			printModules(w, moduleResult{Matches: cat.SearchModules("blast"), Containers: cat.SearchContainers("blast"), Datasets: cat.SearchDatasets("blast")})
		}
		return err
	})
	section("check", func(w io.Writer) error {
		c, err := s.ScriptCheck(ctx, "#!/bin/bash\n#SBATCH -p highmem\n#SBATCH -t 2:00:00\nmodule load r\nRscript a.R\n")
		if err == nil {
			printCheck(w, c)
		}
		return err
	})
	section("usage", func(w io.Writer) error {
		u, err := s.Usage(ctx, core.UsageInput{Since: "2026-09-01"})
		if err == nil {
			printUsage(w, u)
		}
		return err
	})
	section("allocation", func(w io.Writer) error {
		a, err := s.MyAllocation(ctx)
		if err == nil {
			printAllocation(w, a)
		}
		return err
	})
	section("waste", func(w io.Writer) error {
		r, err := s.Waste(ctx, core.WasteInput{Since: "2026-09-01", CPUThreshold: 50, MinNodeHours: 0.01, All: true})
		if err == nil {
			printWaste(w, r)
		}
		return err
	})
	section("health", func(w io.Writer) error {
		h, err := s.Health(ctx)
		if err == nil {
			printHealth(w, h)
		}
		return err
	})
	section("ticket", func(w io.Writer) error {
		tk, err := s.TicketDraft(ctx, core.TicketDraftInput{JobID: "203", AnyUser: true})
		if err == nil {
			printTicket(w, tk)
		}
		return err
	})
	section("submit", func(w io.Writer) error {
		p, err := s.PrepareSubmit(ctx, core.SubmitInput{Script: "#!/bin/bash\n#SBATCH -p computehigh\n#SBATCH -N 1\n#SBATCH -t 01:00:00\n#SBATCH -J hello\nmodule load python-sci\npython3 x.py\n"})
		if err == nil {
			p.Token = "<confirm_token>"
			printSubmitPlan(w, p)
		}
		return err
	})

	got := b.Bytes()
	p := filepath.Join("..", "..", "testdata", "golden", goldenCLISiteName, "cli.txt")
	if os.Getenv("GOLDEN_UPDATE") != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		wl, gl := bytes.Split(want, []byte("\n")), bytes.Split(got, []byte("\n"))
		for i := 0; i < len(wl) || i < len(gl); i++ {
			var x, y []byte
			if i < len(wl) {
				x = wl[i]
			}
			if i < len(gl) {
				y = gl[i]
			}
			if !bytes.Equal(x, y) {
				t.Fatalf("cli.txt differs at line %d\n  want: %s\n  got:  %s", i+1, x, y)
			}
		}
	}
}
