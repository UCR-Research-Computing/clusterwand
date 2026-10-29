// Command clusterwand is the clusterwand CLI and MCP server.
//
//	clusterwand mcp                  run the MCP server on stdio
//	clusterwand status               cluster status
//	clusterwand jobs [--state S]     your jobs
//	clusterwand job show|explain|log <id>
//	clusterwand modules <query>      search modules
//	clusterwand recipes [query]
//	clusterwand check <script.sh>    static check of a batch script
//	clusterwand usage [--since now-30days] [--by partition|state|user]
//	clusterwand config init|show|path
//	clusterwand doctor
//
// Every command takes --json (machine output) and --config PATH.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/UCR-Research-Computing/clusterwand/internal/config"
	"github.com/UCR-Research-Computing/clusterwand/internal/core"
	"github.com/UCR-Research-Computing/clusterwand/internal/mcpserver"
	"github.com/UCR-Research-Computing/clusterwand/internal/version"
)

const usage = `clusterwand - read-only bridge between AI assistants and your Slurm cluster (over your own SSH)

Usage:
  clusterwand mcp                              run the MCP server on stdio
  clusterwand status                           cluster status (nodes, queue, $/hour)
  clusterwand partitions                       partitions from the cluster catalog
  clusterwand jobs [--state S] [--since T] [--all] [--user U]
  clusterwand job show <id> [--script] [--any]
  clusterwand job explain <id> [--lines N] [--any]
  clusterwand job log <id> [--stderr] [--lines N] [--start N] [--grep RE] [--any]
  clusterwand modules [query]                  search software modules
  clusterwand module [--mpi M] <name>          module show (MPI-built ones under their MPI)
  clusterwand recipes [query]                  known-good recipes and site rules
  clusterwand check <script.sh|->              static check of a batch script
  clusterwand allocation                       your allocation and use
  clusterwand usage [--since T] [--until T] [--by partition|state|user] [--all]
  clusterwand waste [--since T] [--cpu PCT] [--all] [--user U]   avoidable spend
  clusterwand health                           down/drained nodes, stockouts, stuck jobs (R2)
  clusterwand ticket <id> [--text FILE|-]      draft a ticket reply (R2; never sent)
  clusterwand results <id> [--prefix DIR] [--pattern GLOB] [--offset N] [--limit N]
                  [--read FILE [--read-offset N] [--bytes N] [--grep RE]] [--download] [--files a,b]
  clusterwand storage                          space used in your home and scratch folders
  clusterwand ls [PATH] [--pattern GLOB] [--offset N]   a folder under your home or scratch
  clusterwand cat PATH [--offset N] [--bytes N] [--grep RE]   a text file under home or scratch
  clusterwand env [--module M]... [CMD...]     what modules load and which tools you get
  clusterwand interactive [--partition P] [--nodes N] [--cpus N] [--gpus N] [--time T] [--mem M]
  clusterwand submit <script.sh|-> [--partition P] [--nodes N] [--time T] [--name J] [--yes]   (A1)
  clusterwand cancel|hold|release <id> [--yes]                      (A1)
  clusterwand confirm <token>                  confirm a prepared action (A1)
  clusterwand config init|show|path
  clusterwand doctor                           check config, SSH and the catalog
  clusterwand version

Global flags (anywhere): --json  --config PATH
Times: now-7days, now-12hours, YYYY-MM-DD. --all/--any need tier R2 in the config.
`

type moduleResult struct {
	Matches    []core.ModuleHit `json:"matches"`
	Containers []core.Container `json:"containers,omitempty"`
	Datasets   []core.Dataset   `json:"datasets,omitempty"`
}

type globals struct {
	json   bool
	config string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// splitGlobals pulls --json and --config out of args wherever they appear.
func splitGlobals(args []string) (globals, []string, error) {
	var g globals
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			g.json = true
		case a == "--config":
			if i+1 >= len(args) {
				return g, nil, errors.New("--config needs a path")
			}
			i++
			g.config = args[i]
		case strings.HasPrefix(a, "--config="):
			g.config = strings.TrimPrefix(a, "--config=")
		default:
			rest = append(rest, a)
		}
	}
	return g, rest, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	g, args, err := splitGlobals(args)
	if err != nil {
		return fail(stdout, stderr, g, err)
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		return emit(stdout, g, map[string]string{"version": version.Version}, func(w io.Writer) {
			fmt.Fprintln(w, "clusterwand", version.Version)
		})
	case "config":
		return configCmd(rest, stdout, stderr, g)
	}

	cfg, err := config.Load(g.config)
	if err != nil {
		return fail(stdout, stderr, g, err)
	}
	svc, err := core.New(cfg)
	if err != nil {
		return fail(stdout, stderr, g, err)
	}
	defer func() { _ = svc.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "mcp":
		srv := mcpserver.New(svc)
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "clusterwand mcp:", err)
			return 1
		}
		return 0
	case "status":
		r, err := core.Call(ctx, svc, "cli", "cluster_status", "R1", nil, false, svc.ClusterStatus)
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printStatus(w, r.Data, svc.Site) })
	case "partitions":
		r, err := core.Call(ctx, svc, "cli", "partitions", "R1", nil, false, svc.Partitions)
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printPartitions(w, r.Data) })
	case "jobs":
		fs := newFlags("jobs")
		state := fs.String("state", "", "state filter")
		since := fs.String("since", "now-7days", "accounting window start")
		limit := fs.Int("limit", 50, "max rows")
		all := fs.Bool("all", false, "every user (R2)")
		user := fs.String("user", "", "one user (R2)")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		tool, tier := "jobs_list", "R1"
		if *all || *user != "" {
			tool, tier = "jobs_list_all", "R2"
		}
		in := core.JobsListInput{User: *user, All: *all && *user == "", State: *state, Since: *since, Limit: *limit}
		r, err := core.Call(ctx, svc, "cli", tool, tier, map[string]any{"state": *state, "since": *since, "user": *user, "all": *all}, false,
			func(ctx context.Context) ([]core.JobSummary, error) { return svc.JobsList(ctx, in) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printJobs(w, r.Data, *all || *user != "") })
	case "job":
		return jobCmd(ctx, svc, rest, stdout, stderr, g)
	case "modules":
		q := strings.Join(rest, " ")
		r, err := core.Call(ctx, svc, "cli", "modules_search", "R1", map[string]any{"query": q}, false,
			func(ctx context.Context) (moduleResult, error) {
				cat, err := svc.Catalog(ctx)
				if err != nil {
					return moduleResult{}, err
				}
				return moduleResult{Matches: cat.SearchModules(q), Containers: cat.SearchContainers(q), Datasets: cat.SearchDatasets(q)}, nil
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printModules(w, r.Data) })
	case "module":
		fs := newFlags("module")
		mpi := fs.String("mpi", "", "for MPI-built packages: which MPI build (openmpi, mpich, intel-oneapi-mpi)")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		if fs.NArg() != 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand module [--mpi openmpi] <name>"))
		}
		name := fs.Arg(0)
		r, err := core.Call(ctx, svc, "cli", "module_show", "R1", map[string]any{"name": name, "mpi": *mpi}, false,
			func(ctx context.Context) (*core.ModuleShowResult, error) { return svc.ModuleShow(ctx, name, *mpi) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			if r.Data == nil {
				return
			}
			for _, n := range r.Data.Notes {
				fmt.Fprintln(w, "# "+n)
			}
			fmt.Fprint(w, r.Data.Show)
		})
	case "recipes":
		q := strings.Join(rest, " ")
		r, err := core.Call(ctx, svc, "cli", "recipes", "R1", map[string]any{"query": q}, false,
			func(ctx context.Context) ([]core.Recipe, error) {
				cat, err := svc.Catalog(ctx)
				if err != nil {
					return nil, err
				}
				return cat.SearchRecipes(q), nil
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printRecipes(w, r.Data) })
	case "check":
		if len(rest) != 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand check <script.sh|->"))
		}
		var b []byte
		if rest[0] == "-" {
			b, err = io.ReadAll(io.LimitReader(os.Stdin, int64(cfg.Limits.ScriptBytes)+1))
		} else {
			b, err = os.ReadFile(rest[0])
		}
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		r, err := core.Call(ctx, svc, "cli", "script_check", "R1", map[string]any{"file": rest[0]}, false,
			func(ctx context.Context) (*core.ScriptCheck, error) { return svc.ScriptCheck(ctx, string(b)) })
		code := out(stdout, stderr, g, r, err, func(w io.Writer) { printCheck(w, r.Data) })
		if code == 0 && r.Data != nil && !r.Data.OK {
			return 2
		}
		return code
	case "usage":
		fs := newFlags("usage")
		since := fs.String("since", "now-30days", "start")
		until := fs.String("until", "", "end")
		by := fs.String("by", "partition", "partition|state|user")
		all := fs.Bool("all", false, "every user (R2)")
		user := fs.String("user", "", "one user (R2)")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		tool, tier := "my_usage", "R1"
		if *all || *user != "" {
			tool, tier = "usage_report", "R2"
		}
		in := core.UsageInput{User: *user, All: *all && *user == "", Since: *since, Until: *until, GroupBy: *by}
		r, err := core.Call(ctx, svc, "cli", tool, tier, map[string]any{"since": *since, "until": *until, "by": *by, "user": *user, "all": *all}, false,
			func(ctx context.Context) (*core.Usage, error) { return svc.Usage(ctx, in) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printUsage(w, r.Data) })
	case "allocation":
		r, err := core.Call(ctx, svc, "cli", "my_allocation", "R1", nil, false,
			func(ctx context.Context) (*core.MyAllocation, error) { return svc.MyAllocation(ctx) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printAllocation(w, r.Data, svc.Site) })
	case "waste":
		fs := newFlags("waste")
		since := fs.String("since", "now-7days", "start")
		cpu := fs.Float64("cpu", 25, "CPU efficiency threshold percent")
		minNH := fs.Float64("min-node-hours", 0.25, "ignore smaller jobs")
		all := fs.Bool("all", false, "every user (R2)")
		user := fs.String("user", "", "one user (R2)")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		tool, tier := "waste_report", "R1"
		if *all || *user != "" {
			tool, tier = "waste_report_all", "R2"
		}
		in := core.WasteInput{Since: *since, All: *all && *user == "", User: *user, CPUThreshold: *cpu, MinNodeHours: *minNH}
		r, err := core.Call(ctx, svc, "cli", tool, tier, map[string]any{"since": *since, "cpu": *cpu, "user": *user, "all": *all}, false,
			func(ctx context.Context) (*core.WasteReport, error) { return svc.Waste(ctx, in) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printWaste(w, r.Data) })
	case "health":
		r, err := core.Call(ctx, svc, "cli", "health", "R2", nil, false, svc.Health)
		code := out(stdout, stderr, g, r, err, func(w io.Writer) { printHealth(w, r.Data) })
		if code == 0 && r.Data != nil && !r.Data.OK {
			return 2
		}
		return code
	case "ticket":
		if len(rest) < 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand ticket <job id> [--text FILE|-]"))
		}
		id := rest[0]
		fs := newFlags("ticket")
		textFile := fs.String("text", "", "file with the researcher's message ('-' = stdin)")
		if err := fs.Parse(rest[1:]); err != nil {
			return fail(stdout, stderr, g, err)
		}
		var text string
		if *textFile != "" {
			var b []byte
			if *textFile == "-" {
				b, err = io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
			} else {
				b, err = os.ReadFile(*textFile)
			}
			if err != nil {
				return fail(stdout, stderr, g, err)
			}
			text = string(b)
		}
		r, err := core.Call(ctx, svc, "cli", "ticket_draft", "R2", map[string]any{"job_id": id, "ticket_text_chars": len(text)}, false,
			func(ctx context.Context) (*core.TicketDraft, error) {
				return svc.TicketDraft(ctx, core.TicketDraftInput{JobID: id, TicketText: text, AnyUser: true})
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printTicket(w, r.Data) })
	case "results":
		if len(rest) < 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand results <job id> [--read FILE] [--download] [--files a,b]"))
		}
		id := rest[0]
		fs := newFlags("results")
		read := fs.String("read", "", "show one text file")
		dl := fs.Bool("download", false, "copy the job folder to results_dir/<id>")
		files := fs.String("files", "", "comma-separated files to download")
		prefix := fs.String("prefix", "", "only files under this subfolder")
		pattern := fs.String("pattern", "", "glob on the file name")
		offset := fs.Int("offset", 0, "listing page start")
		limit := fs.Int("limit", 0, "files per page")
		roff := fs.Int64("read-offset", 0, "byte offset for --read")
		rbytes := fs.Int("bytes", 0, "bytes for --read")
		grep := fs.String("grep", "", "search --read file")
		if err := fs.Parse(rest[1:]); err != nil {
			return fail(stdout, stderr, g, err)
		}
		in := core.ResultsInput{JobID: id, Read: *read, Download: *dl || *files != "", Prefix: *prefix, Pattern: *pattern,
			Offset: *offset, Limit: *limit, ReadOffset: *roff, ReadBytes: *rbytes, Grep: *grep}
		if *files != "" {
			in.Files = strings.Split(*files, ",")
		}
		r, err := core.Call(ctx, svc, "cli", "job_results", "R1", map[string]any{"job_id": id, "read": *read, "download": in.Download}, false,
			func(ctx context.Context) (*core.Results, error) { return svc.JobResults(ctx, in) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printResults(w, r.Data) })
	case "storage":
		r, err := core.Call(ctx, svc, "cli", "storage_usage", "R1", nil, false, svc.StorageUsage)
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			for _, f := range r.Data.Filesystems {
				fmt.Fprintf(w, "%-10s %5.1f%% used, %.1f GB free (%s)\n", f.Mount, f.UsedPct, float64(f.FreeBytes)/(1<<30), f.SharedNote)
			}
			fmt.Fprintf(w, "home %.2f GB, scratch %.2f GB\n", float64(r.Data.HomeBytes)/(1<<30), float64(r.Data.ScratchByte)/(1<<30))
			for _, f := range r.Data.Folders {
				if f.Partial {
					fmt.Fprintf(w, "  %10s  %s\n", "unknown", f.Path)
				} else {
					fmt.Fprintf(w, "  %8.2f GB  %s\n", float64(f.Bytes)/(1<<30), f.Path)
				}
			}
			for _, n := range r.Data.Notes {
				fmt.Fprintln(w, "note:", n)
			}
		})
	case "ls":
		p := ""
		args := rest
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			p, args = args[0], args[1:]
		}
		fs := newFlags("ls")
		pattern := fs.String("pattern", "", "glob on the name")
		offset := fs.Int("offset", 0, "page start")
		limit := fs.Int("limit", 0, "entries per page")
		if err := fs.Parse(args); err != nil {
			return fail(stdout, stderr, g, err)
		}
		r, err := core.Call(ctx, svc, "cli", "files_list", "R1", map[string]any{"path": p, "pattern": *pattern, "offset": *offset}, false,
			func(ctx context.Context) (*core.DirListing, error) {
				return svc.FilesList(ctx, core.FilesListInput{Path: p, Pattern: *pattern, Offset: *offset, Limit: *limit})
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			fmt.Fprintf(w, "%s (%d entries)\n", r.Data.Path, r.Data.Total)
			for _, e := range r.Data.Entries {
				fmt.Fprintf(w, "  %-5s %12d  %s  %s\n", e.Type, e.Bytes, e.Modified, e.Name)
			}
			for _, n := range r.Data.Notes {
				fmt.Fprintln(w, "note:", n)
			}
			if r.Data.NextOffset > 0 {
				fmt.Fprintf(w, "more: --offset %d\n", r.Data.NextOffset)
			}
		})
	case "cat":
		if len(rest) < 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand cat PATH [--offset N] [--bytes N] [--grep RE]"))
		}
		p := rest[0]
		fs := newFlags("cat")
		offset := fs.Int64("offset", 0, "byte offset")
		nb := fs.Int("bytes", 0, "bytes to read")
		grep := fs.String("grep", "", "search instead of reading")
		if err := fs.Parse(rest[1:]); err != nil {
			return fail(stdout, stderr, g, err)
		}
		r, err := core.Call(ctx, svc, "cli", "files_read", "R1", map[string]any{"path": p, "offset": *offset, "grep": *grep}, false,
			func(ctx context.Context) (*core.FileRead, error) {
				return svc.FilesRead(ctx, core.FilesReadInput{Path: p, Offset: *offset, Bytes: *nb, Grep: *grep})
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			if r.Data.Grep != nil {
				fmt.Fprintf(w, "# %s: %d match(es) in %d lines (redacted)\n%s", r.Data.Path, r.Data.Grep.Matches, r.Data.Grep.TotalLines, r.Data.Grep.Lines.Text)
				return
			}
			fmt.Fprint(w, r.Data.Text.Text)
			if !r.Data.Chunk.EOF {
				fmt.Fprintf(w, "\n# more: --offset %d (of %d bytes)\n", r.Data.Chunk.NextOffset, r.Data.Chunk.FileBytes)
			}
		})
	case "env":
		fs := newFlags("env")
		var mods multiFlag
		fs.Var(&mods, "module", "module to load (repeatable)")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		cmds := fs.Args()
		r, err := core.Call(ctx, svc, "cli", "env_check", "R1", map[string]any{"modules": []string(mods), "commands": cmds}, false,
			func(ctx context.Context) (*core.EnvCheck, error) { return svc.EnvCheck(ctx, mods, cmds) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			for _, m := range r.Data.Modules {
				if m.Loaded {
					fmt.Fprintf(w, "module %-24s loaded\n", m.Module)
				} else {
					fmt.Fprintf(w, "module %-24s FAILED: %s\n", m.Module, m.Error)
				}
			}
			fmt.Fprintf(w, "loaded: %s\n", strings.Join(r.Data.Loaded, " "))
			for _, c := range r.Data.Commands {
				p := c.Path
				if p == "" {
					p = "not found"
				}
				fmt.Fprintf(w, "%-10s %s  %s\n", c.Command, p, c.Version)
			}
			for _, n := range r.Data.Notes {
				fmt.Fprintln(w, "note:", n)
			}
		})
	case "interactive":
		fs := newFlags("interactive")
		part := fs.String("partition", "", "partition")
		nodes := fs.Int("nodes", 0, "nodes")
		cpus := fs.Int("cpus", 0, "cpus")
		gpus := fs.Int("gpus", 0, "gpus")
		tl := fs.String("time", "", "time limit")
		mem := fs.String("mem", "", "memory")
		if err := fs.Parse(rest); err != nil {
			return fail(stdout, stderr, g, err)
		}
		in := core.InteractiveInput{Partition: *part, Nodes: *nodes, CPUs: *cpus, GPUs: *gpus, Time: *tl, Memory: *mem}
		r, err := core.Call(ctx, svc, "cli", "interactive_help", "R1", map[string]any{"partition": *part, "time": *tl}, false,
			func(ctx context.Context) (*core.InteractiveHelp, error) { return svc.InteractiveHelp(ctx, in) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			fmt.Fprintf(w, "1. connect:  %s\n2. session:  %s\n   or:       %s\n", r.Data.Connect, r.Data.Command, r.Data.Alternative)
			if r.Data.USDPerHour > 0 {
				fmt.Fprintf(w, "cost: $%.2f/hour, up to $%.2f for %s\n", r.Data.USDPerHour, r.Data.WorstCaseUSD, r.Data.TimeLimit)
			}
			for _, x := range append(r.Data.Warnings, r.Data.Notes...) {
				fmt.Fprintln(w, "-", x)
			}
		})
	case "submit":
		if len(rest) < 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand submit <script.sh|-> [--partition P] [--nodes N] [--time T] [--name J] [--yes]"))
		}
		src := rest[0]
		fs := newFlags("submit")
		part := fs.String("partition", "", "partition")
		nodes := fs.Int("nodes", 0, "nodes")
		tl := fs.String("time", "", "time limit")
		name := fs.String("name", "", "job name")
		yes := fs.Bool("yes", false, "confirm without asking (still within caps)")
		if err := fs.Parse(rest[1:]); err != nil {
			return fail(stdout, stderr, g, err)
		}
		var b []byte
		if src == "-" {
			b, err = io.ReadAll(io.LimitReader(os.Stdin, int64(cfg.Limits.ScriptBytes)+1))
		} else {
			b, err = os.ReadFile(src)
		}
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		r, err := core.Call(ctx, svc, "cli", "job_submit", "A1", map[string]any{"file": src, "partition": *part, "nodes": *nodes, "time": *tl}, false,
			func(ctx context.Context) (*core.SubmitPlan, error) {
				in := core.SubmitInput{Script: string(b), Partition: *part, Nodes: *nodes, Time: *tl, JobName: *name}
				return svc.PrepareSubmit(ctx, in)
			})
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		if g.json && !*yes {
			return emit(stdout, g, r, nil)
		}
		if !g.json {
			printSubmitPlan(stdout, r.Data)
		}
		if !*yes && !askYes(stdout, "Submit this job?") {
			fmt.Fprintln(stdout, "Not submitted.")
			return 0
		}
		return confirmCmd(ctx, svc, "submit", r.Data.Token, stdout, stderr, g)
	case "cancel", "hold", "release":
		if len(rest) < 1 {
			return fail(stdout, stderr, g, fmt.Errorf("usage: clusterwand %s <job id> [--yes]", cmd))
		}
		id := rest[0]
		fs := newFlags(cmd)
		yes := fs.Bool("yes", false, "confirm without asking")
		if err := fs.Parse(rest[1:]); err != nil {
			return fail(stdout, stderr, g, err)
		}
		r, err := core.Call(ctx, svc, "cli", "job_"+cmd, "A1", map[string]any{"job_id": id}, false,
			func(ctx context.Context) (*core.ActionPlan, error) { return svc.PrepareAction(ctx, cmd, id) })
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		if g.json && !*yes {
			return emit(stdout, g, r, nil)
		}
		if !g.json {
			fmt.Fprintf(stdout, "%s job %s (%s, %s)\n  %s\n", strings.ToUpper(cmd[:1])+cmd[1:], r.Data.JobID, r.Data.JobName, r.Data.State, r.Data.Effect)
		}
		if !*yes && !askYes(stdout, "Go ahead?") {
			fmt.Fprintln(stdout, "Nothing changed.")
			return 0
		}
		return confirmCmd(ctx, svc, cmd, r.Data.Token, stdout, stderr, g)
	case "confirm":
		if len(rest) != 1 {
			return fail(stdout, stderr, g, errors.New("usage: clusterwand confirm <token>"))
		}
		return confirmCmd(ctx, svc, "", rest[0], stdout, stderr, g)
	case "doctor":
		return doctor(ctx, svc, stdout, g)
	}
	return fail(stdout, stderr, g, fmt.Errorf("unknown command %q (clusterwand --help)", cmd))
}

// confirmCmd runs a prepared action; kind "" looks the kind up from the token.
func confirmCmd(ctx context.Context, svc *core.Service, kind, token string, stdout, stderr io.Writer, g globals) int {
	var r core.Result[*core.Confirmed]
	var err error
	if kind == "" {
		kind, err = svc.PendingKind(token)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
	}
	tool := "job_" + kind + "_confirm"
	if kind == "submit" {
		r, err = core.Call(ctx, svc, "cli", tool, "A1", nil, false, func(ctx context.Context) (*core.Confirmed, error) { return svc.ConfirmSubmit(ctx, token) })
	} else {
		r, err = core.Call(ctx, svc, "cli", tool, "A1", nil, false, func(ctx context.Context) (*core.Confirmed, error) { return svc.ConfirmAction(ctx, kind, token) })
	}
	return out(stdout, stderr, g, r, err, func(w io.Writer) {
		fmt.Fprintln(w, r.Data.Message)
		if r.Data.RemoteDir != "" {
			fmt.Fprintf(w, "folder on the cluster: %s\n", r.Data.RemoteDir)
		}
		if r.Data.JobID != "" && kind == "submit" {
			fmt.Fprintf(w, "watch: clusterwand job show %s    results: clusterwand results %s --download\n", r.Data.JobID, r.Data.JobID)
		}
	})
}

func askYes(w io.Writer, q string) bool {
	fmt.Fprintf(w, "%s [y/N] ", q)
	var a string
	if _, err := fmt.Fscanln(os.Stdin, &a); err != nil {
		return false
	}
	a = strings.ToLower(strings.TrimSpace(a))
	return a == "y" || a == "yes"
}

func jobCmd(ctx context.Context, svc *core.Service, args []string, stdout, stderr io.Writer, g globals) int {
	if len(args) < 2 {
		return fail(stdout, stderr, g, errors.New("usage: clusterwand job show|explain|log <id>"))
	}
	sub, id, rest := args[0], args[1], args[2:]
	fs := newFlags("job " + sub)
	anyUser := fs.Bool("any", false, "another user's job (R2)")
	lines := fs.Int("lines", 0, "log lines")
	script := fs.Bool("script", false, "include the batch script")
	stderrStream := fs.Bool("stderr", false, "read stderr instead of stdout")
	start := fs.Int("start", 0, "first log line (default: the end)")
	grep := fs.String("grep", "", "search the whole log")
	if err := fs.Parse(rest); err != nil {
		return fail(stdout, stderr, g, err)
	}
	tier := "R1"
	suffix := ""
	if *anyUser {
		tier, suffix = "R2", "_any"
	}
	switch sub {
	case "show":
		r, err := core.Call(ctx, svc, "cli", "job_show"+suffix, tier, map[string]any{"job_id": id, "script": *script}, false,
			func(ctx context.Context) (*core.JobDetail, error) {
				return svc.JobShow(ctx, core.JobShowInput{JobID: id, AnyUser: *anyUser, IncludeScript: *script})
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printJob(w, r.Data) })
	case "explain":
		r, err := core.Call(ctx, svc, "cli", "job_explain"+suffix, tier, map[string]any{"job_id": id}, false,
			func(ctx context.Context) (*core.Explanation, error) { return svc.JobExplain(ctx, id, *anyUser, *lines) })
		return out(stdout, stderr, g, r, err, func(w io.Writer) { printExplain(w, r.Data) })
	case "log":
		stream := "stdout"
		if *stderrStream {
			stream = "stderr"
		}
		r, err := core.Call(ctx, svc, "cli", "job_log_tail"+suffix, tier, map[string]any{"job_id": id, "stream": stream, "start_line": *start, "grep": *grep}, false,
			func(ctx context.Context) (*core.LogTail, error) {
				return svc.JobLog(ctx, core.LogInput{JobID: id, Stream: stream, Lines: *lines, StartLine: *start, Grep: *grep, AnyUser: *anyUser})
			})
		return out(stdout, stderr, g, r, err, func(w io.Writer) {
			if r.Data.Grep != nil {
				fmt.Fprintf(w, "# %s (%s): %d match(es) in %d lines, redacted\n%s", r.Data.Path, r.Data.Stream, r.Data.Grep.Matches, r.Data.TotalLines, r.Data.Grep.Lines.Text)
				return
			}
			fmt.Fprintf(w, "# %s (%s, lines %d-%d of %d, redacted)\n%s", r.Data.Path, r.Data.Stream, r.Data.FirstLine, r.Data.LastLine, r.Data.TotalLines, r.Data.Tail.Text)
			if r.Data.Next != "" {
				fmt.Fprintln(w, "#", r.Data.Next)
			}
		})
	}
	return fail(stdout, stderr, g, fmt.Errorf("unknown job subcommand %q (show, explain, log)", sub))
}

func configCmd(args []string, stdout, stderr io.Writer, g globals) int {
	path := g.config
	if path == "" {
		path = config.DefaultPath()
	}
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "path":
		fmt.Fprintln(stdout, path)
		return 0
	case "init":
		if _, err := os.Stat(path); err == nil {
			return fail(stdout, stderr, g, fmt.Errorf("%s already exists; edit it or remove it first", path))
		}
		if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
			return fail(stdout, stderr, g, err)
		}
		if err := os.WriteFile(path, []byte(config.Example), 0o600); err != nil {
			return fail(stdout, stderr, g, err)
		}
		fmt.Fprintln(stdout, "wrote", path)
		return 0
	case "show":
		c, err := config.Load(path)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		return emit(stdout, g, c, func(w io.Writer) {
			b, _ := json.MarshalIndent(c, "", "  ")
			fmt.Fprintln(w, string(b))
		})
	}
	return fail(stdout, stderr, g, fmt.Errorf("unknown config subcommand %q (init, show, path)", sub))
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// out prints a result as JSON or text, or the error.
func out[T any](stdout, stderr io.Writer, g globals, r core.Result[T], err error, text func(io.Writer)) int {
	if err != nil {
		return fail(stdout, stderr, g, err)
	}
	return emit(stdout, g, r, text)
}

func emit(stdout io.Writer, g globals, v any, text func(io.Writer)) int {
	if g.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return 0
	}
	text(stdout)
	return 0
}

func fail(stdout, stderr io.Writer, g globals, err error) int {
	if g.json {
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(stderr, "clusterwand:", err)
	}
	return 1
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}
