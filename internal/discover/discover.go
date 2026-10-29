// Package discover drafts a site profile and catalog for a new cluster from
// read-only probes made on its login node over the person's own SSH (F3):
//
//	sinfo --version                    Slurm release
//	scontrol -o show partition         partitions, limits, who may use them
//	scontrol --version; scontrol -o show nodes   cores, memory, GPUs, features, cloud
//	scontrol show config               a few cluster-wide keys (kept: ConfigKeys)
//	bash -lc <env template>            $HOME, candidate scratch folders, module system
//	bash -lc 'module -t avail 2>&1'    the module list
//
// Nothing runs on a compute node and nothing is written on the cluster. The
// draft is written with approved: false (profile and catalog); clusterwand
// refuses both until a person has reviewed them and set approved: true.
// This package only parses and renders; core runs the probes.
package discover

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

// Probe is the raw output of the read-only probes.
type Probe struct {
	Host       string // the SSH alias it read
	User       string // id -un
	Version    string // sinfo --version ("slurm 25.11.4")
	Partitions string // scontrol -o show partition
	Nodes      string // scontrol --version; scontrol -o show nodes
	Config     string // scontrol show config, filtered to ConfigKeys (FilterConfig)
	Env        string // the env template's tab-separated lines
	Modules    string // module -t avail 2>&1
}

// ConfigKeys are the `scontrol show config` keys discover keeps; everything
// else (hosts, addresses, paths of daemons) is dropped before it is used or
// recorded.
var ConfigKeys = []string{"ClusterName", "SLURM_VERSION", "SelectType", "SelectTypeParameters",
	"AccountingStorageEnforce", "AccountingStorageTRES", "AccountingStoreFlags", "PrivateData",
	"SuspendTime", "SuspendProgram", "ResumeProgram", "DefMemPerCPU", "MaxArraySize", "PriorityType",
	"JobSubmitPlugins"}

// FilterConfig keeps only the ConfigKeys lines of `scontrol show config`,
// normalised to "Key = Value".
func FilterConfig(raw string) string {
	keep := map[string]bool{}
	for _, k := range ConfigKeys {
		keep[k] = true
	}
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(l, "=")
		k = strings.TrimSpace(k)
		if ok && keep[k] {
			out = append(out, k+" = "+strings.TrimSpace(v))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// Partition is one partition as discovered.
type Partition struct {
	Name          string   `json:"name"`
	Default       bool     `json:"default"`
	MaxNodes      int      `json:"max_nodes"`
	CPUsPerNode   int      `json:"cpus_per_node"`
	MemGBPerNode  float64  `json:"mem_gb_per_node"`
	GPUsPerNode   int      `json:"gpus_per_node"`
	GPUType       *string  `json:"gpu_type"`
	TimeLimit     string   `json:"time_limit"`
	State         string   `json:"state"`
	USDPerNodeHr  *float64 `json:"usd_per_node_hour"`
	Spot          bool     `json:"spot"`
	OverSubscribe string   `json:"oversubscribe,omitempty"`
	UseFor        string   `json:"use_for"`
	// Restricted: AllowAccounts/DenyAccounts/AllowGroups limit who may use it
	// (clusterwand reads those live; they are not copied into the draft).
	Restricted bool     `json:"restricted,omitempty"`
	features   []string // node features shared by every node
	minutes    int      // MaxTime in minutes (-1: no limit)
}

// Draft is what discover found.
type Draft struct {
	Name       string
	Host       string
	On         string // date of the probe
	By         string // clusterwand version
	Slurm      string // release ("25.11.4")
	Cluster    string // ClusterName
	Home       string // /home/{user}
	Scratch    string // /scratch/{user}
	Probe      string
	GPU        string
	HighMem    string
	General    string
	AVX2Only   []string
	CloudNodes bool
	Python     []string
	Lmod       string // Lmod version ("" = Tcl modules or none)
	HasModules bool
	HostKeys   []string
	Partitions []Partition
	Modules    []string
	Config     map[string]string
	Notes      []string // what a reviewer should look at
}

var (
	reVersion = regexp.MustCompile(`^slurm(-wlm)? (\d+\.\d+(\.\d+)?)`)
	reGPU     = regexp.MustCompile(`(?:^|,)gpu(?::([A-Za-z0-9_.-]+))?:(\d+)`)
	reModule  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,80}(/[A-Za-z0-9._+-]{1,40})?$`)
	probeName = map[string]bool{"check": true, "debug": true, "test": true, "short": true, "devel": true, "dev": true, "interactive": true}
)

// Build turns probes into a draft. name is the site name, on the date, by the
// clusterwand version. It never guesses: a role it cannot infer stays empty
// and gets a note.
func Build(name string, p Probe, on, by string) (*Draft, error) {
	d := &Draft{Name: name, Host: p.Host, On: on, By: by, Config: map[string]string{}}
	m := reVersion.FindStringSubmatch(strings.TrimSpace(p.Version))
	if m == nil {
		return nil, fmt.Errorf("sinfo --version printed %q, not a Slurm release", firstLine(p.Version))
	}
	d.Slurm = m[2]
	for _, l := range strings.Split(p.Config, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			d.Config[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	d.Cluster = d.Config["ClusterName"]

	nodes, err := parseNodes(p.Nodes)
	if err != nil {
		return nil, err
	}
	parts, err := parsePartitions(p.Partitions, nodes)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("scontrol -o show partition listed no partitions")
	}
	d.Partitions = parts
	for _, n := range nodes {
		if contains(n.state, "CLOUD") {
			d.CloudNodes = true
		}
	}
	d.env(p.Env, p.User)
	d.Modules = parseModules(p.Modules)
	for _, mod := range d.Modules {
		base, _, _ := strings.Cut(mod, "/")
		if (base == "python" || base == "python3" || strings.HasPrefix(base, "python-") ||
			base == "anaconda" || base == "anaconda3" || base == "miniconda" || base == "miniconda3" || base == "miniforge" || base == "miniforge3") && !contains(d.Python, base) {
			d.Python = append(d.Python, base)
		}
	}
	d.roles()
	return d, nil
}

type node struct {
	name     string
	cpus     int
	memMB    int
	gpus     int
	gpuType  string
	parts    []string
	state    []string
	features []string
}

func parseNodes(text string) ([]node, error) {
	var out []node
	for i, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "slurm ") {
			continue
		}
		f := slurm.Fields(l)
		if f["NodeName"] == "" {
			return nil, fmt.Errorf("scontrol node line %d has no NodeName", i+1)
		}
		n := node{name: f["NodeName"], cpus: atoi(f["CPUTot"]), memMB: atoi(f["RealMemory"]),
			parts: split(f["Partitions"]), state: strings.Split(f["State"], "+"), features: split(f["AvailableFeatures"])}
		if g := reGPU.FindStringSubmatch(f["Gres"]); g != nil {
			n.gpus, n.gpuType = atoi(g[2]), g[1]
		}
		out = append(out, n)
	}
	return out, nil
}

func parsePartitions(text string, nodes []node) ([]Partition, error) {
	var out []Partition
	for i, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		f := slurm.Fields(l)
		name := f["PartitionName"]
		if name == "" {
			return nil, fmt.Errorf("scontrol partition line %d has no PartitionName", i+1)
		}
		p := Partition{Name: name, Default: f["Default"] == "YES", MaxNodes: atoi(f["TotalNodes"]),
			State: strings.ToLower(f["State"]), OverSubscribe: f["OverSubscribe"], UseFor: ""}
		p.Restricted = restricted(f["AllowAccounts"]) || restricted(f["DenyAccounts"]) || restricted(f["AllowGroups"])
		p.TimeLimit, p.minutes = timeLimit(f["MaxTime"])
		first := true
		for _, n := range nodes {
			if !contains(n.parts, name) {
				continue
			}
			// the smallest node is what every job in the partition can count on
			if first || n.cpus < p.CPUsPerNode {
				p.CPUsPerNode = n.cpus
			}
			if gb := math.Round(float64(n.memMB) / 1024); first || gb < p.MemGBPerNode {
				p.MemGBPerNode = gb
			}
			if first || n.gpus < p.GPUsPerNode {
				p.GPUsPerNode = n.gpus
				if n.gpuType != "" {
					t := n.gpuType
					p.GPUType = &t
				} else {
					p.GPUType = nil
				}
			}
			if first {
				p.features = append([]string(nil), n.features...)
			} else {
				p.features = intersect(p.features, n.features)
			}
			first = false
		}
		p.Spot = contains(p.features, "spot")
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func restricted(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "ALL", "(null)", "N/A":
		return false
	}
	return true
}

// timeLimit is the catalog's time_limit ("infinite" when unlimited) and minutes
// (-1 when unlimited or unknown).
func timeLimit(s string) (string, int) {
	s = strings.TrimSpace(s)
	n := slurm.LimitMinutes(s)
	if !n.Set || n.Infinite {
		return "infinite", -1
	}
	return s, int(n.Number)
}

func (d *Draft) env(text, user string) {
	for _, l := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(l, "\r"), "\t")
		if !ok {
			continue
		}
		switch k {
		case "home":
			d.Home = userDir(v, user)
		case "dir":
			if d.Scratch == "" && v != "" && !strings.HasPrefix(v, strings.TrimSuffix(strings.ReplaceAll(d.Home, "{user}", user), "/")+"/") {
				d.Scratch = userDir(v, user)
			}
		case "lmod":
			d.Lmod = v
		case "module":
			d.HasModules = true
		}
	}
	if d.Home == "" {
		d.Notes = append(d.Notes, "home folder: $HOME did not contain your user name once; set paths.home by hand")
	}
	if d.Scratch == "" {
		d.Notes = append(d.Notes, "scratch: no scratch folder named after you was found; set paths.scratch if the site has one")
	}
}

// userDir turns /home/alice into /home/{user}; "" when the user name is not
// exactly one path element.
func userDir(p, user string) string {
	if user == "" || !strings.HasPrefix(p, "/") {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	n := 0
	for i, e := range parts {
		if e == user {
			parts[i] = "{user}"
			n++
		}
	}
	if n != 1 {
		return ""
	}
	return strings.Join(parts, "/")
}

func parseModules(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if i := strings.IndexByte(l, '('); i > 0 {
			l = strings.TrimSpace(l[:i]) // Lmod markers: (D), (default), (L)
		}
		if l == "" || strings.HasSuffix(l, ":") || strings.HasSuffix(l, "/") || !reModule.MatchString(l) || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// roles infers partition roles from facts only (see the package doc).
func (d *Draft) roles() {
	open := func(p Partition) bool { return !p.Restricted && p.State == "up" }
	var general *Partition
	for i, p := range d.Partitions {
		if p.Default {
			d.General = p.Name
			general = &d.Partitions[i]
		}
	}
	if d.General == "" {
		d.Notes = append(d.Notes, "general: no partition is marked Default=YES; pick the cheapest general CPU partition")
	}
	// probe: an open partition with a short limit (<= 1 hour), shortest first;
	// else an open partition with a test-like name
	best := -1
	for i, p := range d.Partitions {
		if open(p) && p.minutes > 0 && p.minutes <= 60 && (best < 0 || p.minutes < d.Partitions[best].minutes) {
			best = i
		}
	}
	if best < 0 {
		for i, p := range d.Partitions {
			if open(p) && probeName[p.Name] {
				best = i
				break
			}
		}
	}
	if best >= 0 {
		d.Probe = d.Partitions[best].Name
		d.Notes = append(d.Notes, fmt.Sprintf("probe: %s (time limit %s) was picked for env_check's one-core job; check that short jobs start there quickly", d.Probe, d.Partitions[best].TimeLimit))
	} else {
		d.Notes = append(d.Notes, "probe: no short open partition found; env_check stays off until partitions.probe is set")
	}
	// gpu: the open partition with GPUs and the most nodes
	for _, p := range d.Partitions {
		if open(p) && p.GPUsPerNode > 0 {
			if g, ok := d.part(d.GPU); !ok || p.MaxNodes > g.MaxNodes {
				d.GPU = p.Name
			}
		}
	}
	// highmem: the open partition with the most memory per node, if it has at
	// least 1.5x the general partition's
	for _, p := range d.Partitions {
		if open(p) && p.GPUsPerNode == 0 && (general == nil || p.MemGBPerNode >= 1.5*general.MemGBPerNode) && p.Name != d.General {
			if h, ok := d.part(d.HighMem); !ok || p.MemGBPerNode > h.MemGBPerNode {
				d.HighMem = p.Name
			}
		}
	}
	// AVX2 only: every node advertises avx2 and none avx512*
	for _, p := range d.Partitions {
		if contains(p.features, "avx2") && !hasPrefix(p.features, "avx512") {
			d.AVX2Only = append(d.AVX2Only, p.Name)
		}
	}
	d.Notes = append(d.Notes,
		"not inferred (set by hand if the site has them): partitions.fast, fallback_idle/fallback_cold, cpu notes, stockout_reasons, charge rates, help, site wording (text)",
		"catalog: the draft catalog.json lists partitions and modules only (no prices, recipes, datasets or use_for); if the site publishes its own catalog, point `catalog:` at it instead")
	if !d.HasModules && len(d.Modules) == 0 {
		d.Notes = append(d.Notes, "modules: no module command in a login shell")
	}
	for _, p := range d.Partitions {
		if p.Restricted {
			d.Notes = append(d.Notes, "lab-locked partitions (AllowAccounts/DenyAccounts/AllowGroups) are read live by clusterwand; the draft does not copy who may use them")
			break
		}
	}
}

func (d *Draft) part(name string) (Partition, bool) {
	for _, p := range d.Partitions {
		if p.Name == name && name != "" {
			return p, true
		}
	}
	return Partition{}, false
}

// ---- rendering ---------------------------------------------------------------------

// ProfileYAML renders the draft site.yaml (approved: false).
func (d *Draft) ProfileYAML() []byte {
	var b strings.Builder
	q := func(s string) string {
		out, _ := yaml.Marshal(s)
		return strings.TrimSpace(string(out))
	}
	list := func(xs []string) string {
		qs := make([]string, len(xs))
		for i, x := range xs {
			qs[i] = q(x)
		}
		return "[" + strings.Join(qs, ", ") + "]"
	}
	fmt.Fprintf(&b, "# DRAFT site profile written by `clusterwand discover` on %s from ssh host %s.\n", d.On, q(d.Host))
	b.WriteString("# Read-only probes on the login node only: sinfo --version, scontrol -o show partition,\n")
	b.WriteString("# scontrol -o show nodes, scontrol show config (selected keys), $HOME and scratch\n")
	b.WriteString("# folders named after you, module -t avail. Nothing was written on the cluster.\n#\n")
	b.WriteString("# clusterwand refuses this profile until a person has reviewed it and set\n")
	b.WriteString("# approved: true (and \"approved\": true in catalog.json). Review notes:\n")
	for _, n := range d.Notes {
		fmt.Fprintf(&b, "#   - %s\n", n)
	}
	fmt.Fprintf(&b, "name: %s\n", d.Name)
	fmt.Fprintf(&b, "title: %s\n", q(d.Name))
	desc := "Slurm " + d.Slurm
	if d.Cluster != "" {
		desc = "Slurm cluster " + d.Cluster + ", Slurm " + d.Slurm
	}
	fmt.Fprintf(&b, "description: %s\n", q(desc))
	b.WriteString("approved: false\n")
	b.WriteString("catalog_file: catalog.json\n")
	if d.Home != "" || d.Scratch != "" {
		b.WriteString("paths:\n")
		if d.Home != "" {
			fmt.Fprintf(&b, "  home: %s\n", q(d.Home))
		}
		if d.Scratch != "" {
			fmt.Fprintf(&b, "  scratch: %s\n", q(d.Scratch))
		}
	}
	fmt.Fprintf(&b, "cloud_nodes: %t\n", d.CloudNodes)
	b.WriteString("partitions:\n")
	for _, kv := range [][2]string{{"probe", d.Probe}, {"gpu", d.GPU}, {"highmem", d.HighMem}, {"general", d.General}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "  %s: %s\n", kv[0], kv[1])
		}
	}
	if len(d.AVX2Only) > 0 {
		fmt.Fprintf(&b, "  avx2_only: %s\n", list(d.AVX2Only))
	}
	if len(d.Python) > 0 {
		b.WriteString("modules:\n")
		fmt.Fprintf(&b, "  python: %s\n", list(d.Python))
	}
	b.WriteString("slurm:\n  json: false\n")
	b.WriteString("charge:\n  model: none\n")
	if len(d.HostKeys) > 0 {
		b.WriteString("# The login node's host keys, as your own ssh trusted them; once approved,\n")
		b.WriteString("# clusterwand connects only when the node shows one of these.\n")
		fmt.Fprintf(&b, "host_keys:\n")
		for _, k := range d.HostKeys {
			fmt.Fprintf(&b, "  - %s\n", q(k))
		}
	}
	b.WriteString("discovered:\n")
	fmt.Fprintf(&b, "  from: %s\n  on: %s\n  slurm: %s\n  by: %s\n", q(d.Host), q(d.On), q(d.Slurm), q(d.By))
	return []byte(b.String())
}

// catalogDoc is the draft catalog file (schema clusterwand-catalog/1).
type catalogDoc struct {
	Schema     string      `json:"schema"`
	Cluster    string      `json:"cluster"`
	Generated  string      `json:"generated"`
	Generator  string      `json:"generator"`
	Approved   bool        `json:"approved"`
	Partitions []Partition `json:"partitions"`
	Rules      []string    `json:"rules"`
	Modules    struct {
		Core         []string            `json:"core"`
		MPIDependent map[string]struct{} `json:"mpi_dependent"`
	} `json:"modules"`
	HowToLoad string `json:"how_to_load,omitempty"`
}

// CatalogJSON renders the draft catalog.json ("approved": false).
func (d *Draft) CatalogJSON() []byte {
	c := catalogDoc{Schema: "clusterwand-catalog/1", Cluster: firstNonEmpty(d.Cluster, d.Name), Generated: d.On,
		Generator: "clusterwand discover " + d.By, Approved: false, Partitions: d.Partitions, Rules: []string{}}
	c.Modules.Core = append([]string{}, d.Modules...)
	c.Modules.MPIDependent = map[string]struct{}{}
	if d.HasModules || len(d.Modules) > 0 {
		c.HowToLoad = "module load <name>[/<version>]"
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return append(b, '\n')
}

// ---- helpers -----------------------------------------------------------------------

func atoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func split(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(null)" {
		return nil
	}
	return strings.Split(s, ",")
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func hasPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(strings.ToLower(x), p) {
			return true
		}
	}
	return false
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(l) > 80 {
		l = l[:80]
	}
	return l
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
