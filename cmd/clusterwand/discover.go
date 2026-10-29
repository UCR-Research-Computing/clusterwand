package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/UCR-Research-Computing/clusterwand/internal/config"
	"github.com/UCR-Research-Computing/clusterwand/internal/core"
	"github.com/UCR-Research-Computing/clusterwand/internal/discover"
	"github.com/UCR-Research-Computing/clusterwand/internal/site"
	"github.com/UCR-Research-Computing/clusterwand/internal/version"
)

var reSiteName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// hostKeysFn finds trusted host keys (tests replace it).
var hostKeysFn = func(host string) ([]string, error) { return discover.HostKeys(discover.LocalRunner, host) }

// nowFn is the clock for the draft's date (tests replace it).
var nowFn = time.Now

type discoverResult struct {
	Site     string          `json:"site"`
	Profile  string          `json:"profile"`
	Catalog  string          `json:"catalog"`
	Recorded string          `json:"recorded,omitempty"`
	Slurm    string          `json:"slurm"`
	Parts    int             `json:"partitions"`
	Modules  int             `json:"modules"`
	HostKeys int             `json:"host_keys"`
	Notes    []string        `json:"notes"`
	Compare  []discover.Diff `json:"compare,omitempty"`
}

// discoverCmd drafts sites/<name>/site.yaml and catalog.json from read-only
// probes on a login node (F3). Both say approved: false and are refused by
// clusterwand until a person reviews them.
func discoverCmd(args []string, stdout, stderr io.Writer, g globals) int {
	fs := newFlags("discover")
	name := fs.String("site", "", "site name (lowercase, digits, dashes)")
	host := fs.String("host", "", "ssh host or alias from your ~/.ssh/config")
	outDir := fs.String("out", "sites", "folder that gets <site>/site.yaml and catalog.json")
	record := fs.String("record", "", "also write anonymised probe fixtures to this folder")
	from := fs.String("from", "", "replay a recorded probe folder instead of connecting")
	compare := fs.String("compare", "", "compare the draft with a reviewed profile (shipped name or file)")
	compareCat := fs.String("compare-catalog", "", "a local copy of the reviewed profile's catalog JSON")
	force := fs.Bool("force", false, "overwrite an existing draft")
	if err := fs.Parse(args); err != nil {
		return fail(stdout, stderr, g, err)
	}
	if !reSiteName.MatchString(*name) {
		return fail(stdout, stderr, g, errors.New("usage: clusterwand discover --site <name> --host <ssh alias> [--out DIR] [--record DIR]"))
	}
	if (*host == "") == (*from == "") {
		return fail(stdout, stderr, g, errors.New("discover needs --host <ssh alias> (or --from <recorded folder>)"))
	}
	dir := filepath.Join(*outDir, *name)
	prof, catf := filepath.Join(dir, "site.yaml"), filepath.Join(dir, "catalog.json")
	if !*force {
		for _, f := range []string{prof, catf} {
			if _, err := os.Stat(f); err == nil {
				return fail(stdout, stderr, g, fmt.Errorf("%s already exists; discover never overwrites a profile (use another --site, --out, or --force for a draft)", f))
			}
		}
	}
	if *force {
		if b, err := os.ReadFile(prof); err == nil {
			if sp, err := site.Parse(b); err == nil && sp.Approved {
				return fail(stdout, stderr, g, fmt.Errorf("%s is an approved profile; discover will not overwrite it", prof))
			}
		}
	}

	var probe *discover.Probe
	var keys []string
	if *from != "" {
		p, err := discover.FromFiles(func(n string) (string, error) {
			b, err := os.ReadFile(filepath.Join(*from, n))
			return string(b), err
		})
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		probe = &p
	} else {
		cfg, err := discoverConfig(g.config, *host)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		svc, err := core.New(cfg)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		defer func() { _ = svc.Close() }()
		r, err := core.Call(context.Background(), svc, "cli", "discover", "R1", map[string]any{"site": *name, "host": *host}, false, svc.DiscoverProbe)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		probe = r.Data
		// the probes above connected in BatchMode with ssh's own host-key
		// check, so the node showed one of the keys ssh trusts for it
		keys, err = hostKeysFn(*host)
		if err != nil {
			return fail(stdout, stderr, g, fmt.Errorf("host key: %w", err))
		}
	}
	d, err := discover.Build(*name, *probe, nowFn().Format("2006-01-02"), version.Version)
	if err != nil {
		return fail(stdout, stderr, g, err)
	}
	d.HostKeys = keys
	if len(keys) == 0 {
		d.Notes = append(d.Notes, "host_keys: none recorded (replayed probe); run discover against the host to pin them")
	}
	res := discoverResult{Site: *name, Profile: prof, Catalog: catf, Slurm: d.Slurm, Parts: len(d.Partitions), Modules: len(d.Modules), HostKeys: len(keys), Notes: d.Notes}
	if *compare != "" {
		hand, err := loadProfile(*compare)
		if err != nil {
			return fail(stdout, stderr, g, err)
		}
		var cat []byte
		if *compareCat != "" {
			if cat, err = os.ReadFile(*compareCat); err != nil {
				return fail(stdout, stderr, g, err)
			}
		}
		if res.Compare, err = discover.Compare(d, hand, cat); err != nil {
			return fail(stdout, stderr, g, err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(stdout, stderr, g, err)
	}
	if err := os.WriteFile(prof, d.ProfileYAML(), 0o644); err != nil {
		return fail(stdout, stderr, g, err)
	}
	if err := os.WriteFile(catf, d.CatalogJSON(), 0o644); err != nil {
		return fail(stdout, stderr, g, err)
	}
	if *record != "" {
		if err := os.MkdirAll(*record, 0o755); err != nil {
			return fail(stdout, stderr, g, err)
		}
		for n, body := range discover.Anonymize(*probe).ToFiles() {
			if err := os.WriteFile(filepath.Join(*record, n), []byte(body), 0o644); err != nil {
				return fail(stdout, stderr, g, err)
			}
		}
		res.Recorded = *record
	}
	return emit(stdout, g, res, func(w io.Writer) { printDiscover(w, res) })
}

// discoverConfig is the config for a discover run: the person's own config
// file when there is one (audit path, timeouts), else defaults; always the
// SSH backend to host with the generic profile (the site has none yet) and
// R1 only.
func discoverConfig(path, host string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return cfg, err
	}
	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\n\x00") {
		return cfg, fmt.Errorf("ssh host %q is not a host name or alias", host)
	}
	cfg.Backend, cfg.SSH.Host = "ssh", host
	cfg.Site, cfg.SiteFile, cfg.Cluster, cfg.CatalogPath, cfg.ClusterUser = "", "", "", "", ""
	cfg.LogRoots, cfg.EnvPartition = nil, ""
	cfg.Tiers = []string{"R1"}
	return cfg, cfg.Validate()
}

func loadProfile(ref string) (*site.Profile, error) {
	if strings.ContainsAny(ref, "/.") {
		return site.File(ref)
	}
	return site.Embedded(ref)
}

func printDiscover(w io.Writer, r discoverResult) {
	fmt.Fprintf(w, "Drafted site %s (Slurm %s): %d partitions, %d modules, %d host key(s) pinned\n", r.Site, r.Slurm, r.Parts, r.Modules, r.HostKeys)
	fmt.Fprintf(w, "  %s\n  %s\n", r.Profile, r.Catalog)
	if r.Recorded != "" {
		fmt.Fprintf(w, "  anonymised probe recorded in %s\n", r.Recorded)
	}
	fmt.Fprintln(w, "Both say approved: false; clusterwand refuses them until you review them and set approved: true.")
	fmt.Fprintln(w, "Review notes:")
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  - %s\n", n)
	}
	if len(r.Compare) > 0 {
		same := 0
		for _, c := range r.Compare {
			if c.Same {
				same++
			}
		}
		fmt.Fprintf(w, "Compared with the reviewed profile: %d of %d fields agree\n", same, len(r.Compare))
		for _, c := range r.Compare {
			mark := "same"
			if !c.Same {
				mark = "DIFF"
				if !c.Inferable {
					mark = "hand"
				}
			}
			fmt.Fprintf(w, "  %-4s %-34s draft=%q reviewed=%q", mark, c.Field, c.Draft, c.Reviewed)
			if c.Note != "" {
				fmt.Fprintf(w, "  (%s)", c.Note)
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintf(w, "Use it: site_file: %s in ~/.config/clusterwand/config.yaml, after review.\n", r.Profile)
}
