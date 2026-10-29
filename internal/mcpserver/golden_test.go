package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/config"
	"github.com/UCR-Research-Computing/clusterwand/internal/core"
)

// Golden tests (F1): every MCP tool, prompt and resource answers byte-identically
// on the recorded fixtures with a site profile loaded. The files under
// testdata/golden/<site>/ were first written by the pre-site-profile code
// (ursa-bifrost v0.10.1 behaviour); any change to them is a reviewed diff.
// Regenerate with: GOLDEN_UPDATE=1 go test ./internal/mcpserver -run Golden

// goldenCall is one recorded MCP interaction.
type goldenCall struct {
	name  string         // file name
	tool  string         // tool name ("" for non-tool interactions)
	args  map[string]any // tool arguments
	tiers []string       // tiers of the session (default R1)
}

// goldenScript is a batch script that passes script_check on every site whose
// catalog has the partition named in it.
func goldenCalls(site goldenSite) []goldenCall {
	sub := map[string]any{"script": site.script}
	return []goldenCall{
		{name: "cluster_status", tool: "cluster_status"},
		{name: "partitions", tool: "partitions"},
		{name: "jobs_list", tool: "jobs_list"},
		{name: "jobs_list_failed", tool: "jobs_list", args: map[string]any{"state": "FAILED", "since": "now-30days"}},
		{name: "job_show_236", tool: "job_show", args: map[string]any{"job_id": "236", "include_script": true}},
		{name: "job_show_260", tool: "job_show", args: map[string]any{"job_id": "260"}},
		{name: "job_explain_236", tool: "job_explain", args: map[string]any{"job_id": "236"}},
		{name: "job_explain_212", tool: "job_explain", args: map[string]any{"job_id": "212"}},
		{name: "job_explain_229", tool: "job_explain", args: map[string]any{"job_id": "229"}},
		{name: "job_explain_253", tool: "job_explain", args: map[string]any{"job_id": "253"}},
		{name: "job_explain_41", tool: "job_explain", args: map[string]any{"job_id": "41"}},
		{name: "job_log_tail_236", tool: "job_log_tail", args: map[string]any{"job_id": "236", "lines": 20}},
		{name: "job_log_tail_grep", tool: "job_log_tail", args: map[string]any{"job_id": "203", "grep": "Error"}},
		{name: "modules_search_empty", tool: "modules_search"},
		{name: "modules_search_gromacs", tool: "modules_search", args: map[string]any{"query": "gromacs"}},
		{name: "modules_search_db", tool: "modules_search", args: map[string]any{"query": "kraken"}},
		{name: "modules_search_miss", tool: "modules_search", args: map[string]any{"query": "zzqx-notathing"}},
		{name: "module_show", tool: "module_show", args: map[string]any{"name": site.module}},
		{name: "recipes", tool: "recipes", args: map[string]any{"query": ""}},
		{name: "script_check_good", tool: "script_check", args: sub},
		{name: "script_check_bad", tool: "script_check", args: map[string]any{"script": site.badScript}},
		{name: "script_check_avx", tool: "script_check", args: map[string]any{"script": site.avxScript}},
		{name: "script_check_spot", tool: "script_check", args: map[string]any{"script": site.spotScript}},
		{name: "interactive_help_nogpu", tool: "interactive_help", args: map[string]any{"gpus": 1}},
		{name: "my_usage", tool: "my_usage", args: map[string]any{"since": "2026-09-01"}},
		{name: "my_usage_state", tool: "my_usage", args: map[string]any{"since": "2026-09-01", "group_by": "state"}},
		{name: "waste_report", tool: "waste_report", args: map[string]any{"since": "2026-09-01", "cpu_threshold": 50, "min_node_hours": 0.01}},
		{name: "my_allocation", tool: "my_allocation"},
		{name: "job_results", tool: "job_results", args: map[string]any{"job_id": "236"}},
		{name: "job_results_read", tool: "job_results", args: map[string]any{"job_id": "236", "read": "log.txt"}},
		{name: "storage_usage", tool: "storage_usage"},
		{name: "files_list", tool: "files_list", args: map[string]any{"path": "~/project"}},
		{name: "files_read", tool: "files_read", args: map[string]any{"path": "~/project/notes.txt"}},
		{name: "files_read_denied", tool: "files_read", args: map[string]any{"path": "~/.ssh/id_ed25519"}},
		{name: "env_check", tool: "env_check", args: map[string]any{"modules": []string{"gcc", "nosuch"}, "commands": []string{"gcc", "python", "nvcc", "mpirun"}}},
		{name: "interactive_help", tool: "interactive_help", args: map[string]any{"cpus": 4, "time": "2:00:00"}},
		{name: "interactive_help_gpu", tool: "interactive_help", args: map[string]any{"partition": site.gpuPartition, "gpus": 1}},
		{name: "job_submit", tool: "job_submit", args: sub, tiers: []string{"R1", "A1"}},
		{name: "job_submit_overcap", tool: "job_submit", args: map[string]any{"script": site.script, "time": "30:00:00"}, tiers: []string{"R1", "A1"}},
		{name: "job_cancel", tool: "job_cancel", args: map[string]any{"job_id": "260"}, tiers: []string{"R1", "A1"}},
		{name: "jobs_list_all", tool: "jobs_list_all", tiers: []string{"R1", "R2"}},
		{name: "job_explain_any", tool: "job_explain_any", args: map[string]any{"job_id": "203"}, tiers: []string{"R1", "R2"}},
		{name: "waste_report_all", tool: "waste_report_all", args: map[string]any{"since": "2026-09-01"}, tiers: []string{"R1", "R2"}},
		{name: "health", tool: "health", tiers: []string{"R1", "R2"}},
		{name: "ticket_draft", tool: "ticket_draft", args: map[string]any{"job_id": "236", "ticket_text": "my job died"}, tiers: []string{"R1", "R2"}},
		{name: "ticket_draft_ok", tool: "ticket_draft", args: map[string]any{"job_id": "261"}, tiers: []string{"R1", "R2"}},
		{name: "usage_report", tool: "usage_report", args: map[string]any{"since": "2026-09-01"}, tiers: []string{"R1", "R2"}},
	}
}

// goldenSite is one site under golden test: its config and fixture inputs.
type goldenSite struct {
	name         string // testdata/golden/<name>
	fixtures     string // fixtures dir
	user         string
	site         string // config site / site_file ("" = none)
	siteFile     string
	script       string
	badScript    string
	avxScript    string
	spotScript   string
	module       string
	gpuPartition string
	home         string
	scratch      string
}

var ursaGolden = goldenSite{
	name: "ursa-major", fixtures: "../../testdata", user: "alice_ucr_edu", site: "ursa-major",
	script:       "#!/bin/bash\n#SBATCH -p computehigh\n#SBATCH -N 1\n#SBATCH -t 01:00:00\n#SBATCH -J hello\nmodule load python-sci\npython -c 'print(42)' > answer.txt\n",
	badScript:    "#!/bin/bash\n#SBATCH -p standard\n#SBATCH -N 99\n#SBATCH -c 64\n#SBATCH --gres=gpu:1\n#SBATCH -t 1-00:00:00\nmodule load nosuchmod\nmodule load hdf5\npip install numpy\npython x.py\napptainer exec --nv x.sif y\n",
	avxScript:    "#!/bin/bash\n#SBATCH -p standard\n#SBATCH -N 2\n#SBATCH --ntasks-per-node=16\n#SBATCH -t 20:00\ngcc -march=native -O3 x.c\nmpirun ./a.out\n",
	spotScript:   "#!/bin/bash\n#SBATCH -p spot\n#SBATCH -c 4\n#SBATCH -t 1:00:00\n#SBATCH --gres=gpu:1\npython3 x.py\n",
	module:       "gromacs",
	gpuPartition: "gpul4",
	home:         "/home/alice_ucr_edu", scratch: "/scratch/alice_ucr_edu",
}

// goldenNow is a fixed clock just after the fixtures were captured.
var goldenNow = time.Unix(1790875401, 0)

// Golden output must not depend on the machine's time zone (as_of, file times).
// time.Unix values point at the shared local zone, so it is overwritten in place
// (after Go has loaded it once, so the lazy load cannot undo this).
func init() {
	_ = time.Local.String()
	*time.Local = *time.FixedZone("EDT", -4*3600)
}

func goldenService(t *testing.T, gs goldenSite, tiers []string) *core.Service {
	t.Helper()
	cfg := config.Default()
	applyGoldenSite(&cfg, gs)
	cfg.StatePath = filepath.Join(t.TempDir(), "a1.json")
	cfg.ResultsDir = filepath.Join(t.TempDir(), "results")
	cfg.Backend = "fixture"
	cfg.FixturesDir = gs.fixtures
	cfg.ClusterUser = gs.user
	cfg.AuditPath = filepath.Join(t.TempDir(), "audit.jsonl")
	if len(tiers) > 0 {
		cfg.Tiers = tiers
	}
	s, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return goldenNow }
	fx := s.Backend.(*backend.Fixture)
	jobDir := gs.home + "/deep-research-lab/run_76"
	fx.Files = map[string]map[string]string{jobDir: {
		"answer.txt": "42\n", "out/data.csv": "a,b\n1,2\n", "log.txt": "API_TOKEN=supersecret123 done\n",
	}}
	fx.Paths = map[string]string{
		gs.home + "/project/notes.txt":  "hello\nAPI_TOKEN=abcdef123456 here\n",
		gs.home + "/project/run.sbatch": "#!/bin/bash\n",
		gs.home + "/.ssh/id_ed25519":    "PRIVATE",
		gs.home + "/project/aws.pem":    "KEY",
		gs.home + "/project/sub/":       "",
		gs.scratch + "/big/out.dat":     "data",
	}
	fx.Out = map[string]string{
		"df": "Filesystem 1-blocks Used Available Capacity Mounted on\nfs1:/homeshare 2704467296256 99455336448 2467556229120 4% " + firstDir(gs.home) +
			"\nfs1:/scratchshare 2704467296256 1718616064 2565292949504 1% " + firstDir(gs.scratch) + "\n",
		backend.DuTemplateForTest(): "8218501120\t" + gs.home + "/clusterwand-jobs\n?\t" + gs.home + "/huge\n2000\t" + gs.home + "/.cache\n100\t" + gs.scratch + "/big\n",
		backend.EnvTemplateForTest(): "J\t475\tnode-probe-0\nM\tgcc\tok\nM\tnosuch\tfail\tLmod has detected the following error: The following module(s) are unknown: \"nosuch\"\nL\ngcc/13.5.0\n" +
			"C\tgcc\t/opt/gcc/13.5.0/bin/gcc\tgcc 13.5.0\nC\tpython\t\t\nC\tnvcc\t\t\nC\tmpirun\t\t\n",
	}
	return s
}

// firstDir is the mount point a path lives on in the fake df ("/home/x" -> "/home").
func firstDir(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) > 2 {
		return "/" + strings.Join(parts[:len(parts)-1], "/")
	}
	return "/" + parts[0]
}

func goldenSession(t *testing.T, s *core.Service) *mcp.ClientSession {
	t.Helper()
	srv := New(s)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "golden", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

var reToken = regexp.MustCompile(`bf1-[A-Za-z0-9_-]+`)

// goldenJSON renders v stably (indented, tokens masked).
func goldenJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = reToken.ReplaceAll(b, []byte("<confirm_token>"))
	return append(b, '\n')
}

func checkGolden(t *testing.T, dir, name string, got []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if os.Getenv("GOLDEN_UPDATE") != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v (GOLDEN_UPDATE=1 writes it)", name, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from golden:\n%s", name, firstDiff(string(want), string(got)))
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d\n  want: %s\n  got:  %s", i+1, x, y)
		}
	}
	return "(no line differs; trailing bytes)"
}

// runGolden records every interaction for one site and returns all output text
// (for leak checks).
func runGolden(t *testing.T, gs goldenSite) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "golden", gs.name)
	var all strings.Builder
	ctx := context.Background()
	record := func(name string, b []byte) {
		all.Write(b)
		checkGolden(t, dir, name+".json", b)
	}

	// server identity, instructions, tool list, prompts and resources (every tier)
	cs := goldenSession(t, goldenService(t, gs, []string{"R1", "R2", "A1"}))
	ir := cs.InitializeResult()
	// the version is left out: it changes every release, the rest must not
	record("_initialize", goldenJSON(t, map[string]any{"name": ir.ServerInfo.Name, "title": ir.ServerInfo.Title, "instructions": ir.Instructions}))
	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := tl.Tools
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	record("_tools", goldenJSON(t, tools))
	pl, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompts := map[string]any{"list": pl.Prompts}
	for _, p := range pl.Prompts {
		args := map[string]string{}
		for _, a := range p.Arguments {
			args[a.Name] = "<" + a.Name + ">"
		}
		g, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: p.Name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		prompts[p.Name] = g.Messages[0].Content.(*mcp.TextContent).Text
	}
	record("_prompts", goldenJSON(t, prompts))
	rl, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := map[string]any{"list": rl.Resources}
	for _, r := range rl.Resources {
		rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatal(err)
		}
		res[r.URI] = rr.Contents[0].Text
	}
	record("_resources", goldenJSON(t, res))

	// every tool call, each in a fresh session (no shared cache between calls)
	for _, c := range goldenCalls(gs) {
		s := goldenService(t, gs, c.tiers)
		cs := goldenSession(t, s)
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		out := map[string]any{}
		if r.IsError {
			var texts []string
			for _, x := range r.Content {
				if tc, ok := x.(*mcp.TextContent); ok {
					texts = append(texts, tc.Text)
				}
			}
			out["error"] = strings.Join(texts, "\n")
		} else {
			out["result"] = r.StructuredContent
		}
		record(c.name, goldenJSON(t, out))
	}
	return all.String()
}

func TestGoldenUrsaMajor(t *testing.T) {
	runGolden(t, ursaGolden)
}

// bigGolden is a synthetic second site (scripts/make_second_site.py): another
// cluster's names, paths and users, charged in service units, no cloud nodes,
// no site wording. Its fixtures hold no Ursa string, so any Ursa string in an
// answer comes from the code.
var bigGolden = goldenSite{
	name: "bigcluster", fixtures: "../../testdata/sites/bigcluster", user: "bob42",
	siteFile:     "../../testdata/sites/bigcluster-profile/site.yaml",
	script:       "#!/bin/bash\n#SBATCH -p fast\n#SBATCH -N 1\n#SBATCH -t 01:00:00\n#SBATCH -J hello\nmodule load python-sci\npython -c 'print(42)' > answer.txt\n",
	badScript:    "#!/bin/bash\n#SBATCH -p cpu\n#SBATCH -N 99\n#SBATCH -c 64\n#SBATCH --gres=gpu:1\n#SBATCH -t 1-00:00:00\nmodule load nosuchmod\nmodule load hdf5\npip install numpy\npython x.py\napptainer exec --nv x.sif y\n",
	avxScript:    "#!/bin/bash\n#SBATCH -p preempt\n#SBATCH -N 2\n#SBATCH --ntasks-per-node=16\n#SBATCH -t 20:00\ngcc -march=native -O3 x.c\nmpirun ./a.out\n",
	spotScript:   "#!/bin/bash\n#SBATCH -p preempt\n#SBATCH -c 4\n#SBATCH -t 1:00:00\n#SBATCH --gres=gpu:1\npython3 x.py\n",
	module:       "gromacs",
	gpuPartition: "gpu",
	home:         "/users/bob42", scratch: "/work/bob42",
}

// ursaWords must never appear in another site's answers: Ursa Major's
// partitions, paths, provider and wording.
var ursaWords = regexp.MustCompile(`(?i)ursa|\bucr\b|ucr[ _-]|google|\bgcp\b|slurm-gcp|us-central|computehigh|gpul4|nvmescratch|\bhighmem\b|\bstandard\b partition|on standard|\bspot\b partition|\bcheck\b partition|/scratch/|/apps/|/data/shared|\be2\b|sapphire|\bL4\b|rocky (linux|8)|python-sci/python-ml|research computing`)

// reDollars finds a non-zero dollar figure in an answer.
var reDollars = regexp.MustCompile(`"[a-z_]*usd[a-z_]*": [1-9]|\$[0-9]+\.[0-9]{2}\b`)

func TestGoldenSecondSite(t *testing.T) {
	out := runGolden(t, bigGolden)
	// the site charges service units: no answer may carry a dollar figure
	for _, line := range strings.Split(out, "\n") {
		if reDollars.MatchString(line) && !strings.Contains(line, "hpc://catalog") {
			t.Errorf("dollar figure on an SU site: %s", strings.TrimSpace(line))
		}
	}
	seen := map[string]bool{}
	for _, m := range ursaWords.FindAllString(out, -1) {
		seen[m] = true
	}
	if len(seen) > 0 {
		var ws []string
		for w := range seen {
			ws = append(ws, w)
		}
		sort.Strings(ws)
		for _, line := range strings.Split(out, "\n") {
			if ursaWords.MatchString(line) {
				t.Logf("leak: %s", strings.TrimSpace(line))
			}
		}
		t.Fatalf("Ursa Major strings in another site's answers: %v", ws)
	}
}
