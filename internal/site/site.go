// Package site loads site profiles: everything about one cluster that is not
// Slurm itself (paths, partition roles, CPU notes, charge model, wording).
// Code holds generic behaviour and wording; a profile adds what is true for one
// site. Profiles ship embedded from sites/<name>/site.yaml or load from a file.
package site

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/UCR-Research-Computing/clusterwand/sites"
)

// Charge models.
const (
	ChargeUSDPerNodeHour = "usd_per_node_hour" // dollars per node-hour (catalog or rates)
	ChargeSUPerCoreHour  = "su_per_core_hour"  // service units per core-hour (rates)
	ChargeNone           = "none"              // no charge: no cost figures, no cost caps
)

// Paths are the person's folders on the cluster; {user} is the cluster user.
type Paths struct {
	Home     string   `yaml:"home"`
	Scratch  string   `yaml:"scratch"`
	LogRoots []string `yaml:"log_roots"` // job logs and job folders may only be read here
}

// Partitions gives partitions roles; every name must exist in the catalog to matter.
type Partitions struct {
	Probe        string            `yaml:"probe"`         // env_check's one-core job (never the login node)
	GPU          string            `yaml:"gpu"`           // where GPU work goes
	HighMem      string            `yaml:"highmem"`       // large-memory partition
	General      string            `yaml:"general"`       // cheapest general CPU partition
	Fast         string            `yaml:"fast"`          // fast-core MPI partition
	FallbackIdle []string          `yaml:"fallback_idle"` // suggested when a node is already up and idle
	FallbackCold []string          `yaml:"fallback_cold"` // suggested otherwise
	CPU          map[string]string `yaml:"cpu"`           // partition -> CPU note
	CPUDefault   string            `yaml:"cpu_default"`   // CPU note for the rest
	AVX2Only     []string          `yaml:"avx2_only"`     // nodes without AVX-512
	TimeHint     map[string]string `yaml:"time_hint"`     // partition -> text after "Lower --time"
	MultiNodeMPI map[string]string `yaml:"multinode_mpi"` // partition -> note for multi-node MPI
}

// Modules names site modules with a meaning.
type Modules struct {
	Python []string `yaml:"python"` // modules that provide a Python environment
	GPU    []string `yaml:"gpu"`    // modules flagged as GPU builds
}

// Charge is how use is charged.
type Charge struct {
	Model string             `yaml:"model"`
	Rates map[string]float64 `yaml:"rates"` // partition -> rate in the model's unit (overrides the catalog)
	// SU caps for su_per_core_hour sites (config caps.max_su_* win)
	MaxSUPerJob float64 `yaml:"max_su_per_job"`
	MaxSUPerDay float64 `yaml:"max_su_per_day"`
}

// Allocation is wording for optional allocations.
type Allocation struct {
	CoveredBy string `yaml:"covered_by"`
}

// Slurm holds per-site scheduler feature flags. The plain-text formats
// (squeue -O, sacct -P, scontrol -o) are the default and read the same on Slurm
// 20.11 to 25.11; JSON is opt-in for sites whose Slurm prints the data_parser
// clusterwand knows (25.11, v0.0.44).
type Slurm struct {
	JSON bool `yaml:"json"` // read squeue/sacct/scontrol nodes with --json
}

// Discovered says where and when `clusterwand discover` drafted a profile.
type Discovered struct {
	From  string `yaml:"from"`  // the SSH host alias it read
	On    string `yaml:"on"`    // date (YYYY-MM-DD)
	Slurm string `yaml:"slurm"` // Slurm release seen
	By    string `yaml:"by"`    // clusterwand version
}

// Profile is one site.
type Profile struct {
	Name        string     `yaml:"name"`
	Title       string     `yaml:"title"`       // "Big Cluster"
	Description string     `yaml:"description"` // "Example University, on premises"
	Approved    bool       `yaml:"approved"`    // a person reviewed it; nothing unapproved is used
	Help        string     `yaml:"help"`        // who to ask ("Research Computing")
	Catalog     string     `yaml:"catalog"`     // catalog path on the login node
	Connect     string     `yaml:"connect"`     // how people reach the login node (default ssh <host>)
	Paths       Paths      `yaml:"paths"`
	CloudNodes  bool       `yaml:"cloud_nodes"` // nodes power up on demand and bill while up
	Stockout    []string   `yaml:"stockout_reasons"`
	Partitions  Partitions `yaml:"partitions"`
	Modules     Modules    `yaml:"modules"`
	Charge      Charge     `yaml:"charge"`
	Allocation  Allocation `yaml:"allocation"`
	Slurm       Slurm      `yaml:"slurm"`
	// HostKeys pins the login node's SSH host keys ("ssh-ed25519 AAAA...");
	// when set, ssh accepts only these keys for this site (F3, recorded by
	// discover from the keys your own ssh already trusts).
	HostKeys []string `yaml:"host_keys,omitempty"`
	// CatalogFile is a catalog kept next to a site_file profile (relative to
	// the profile's folder), used instead of a catalog on the login node. A
	// draft from discover carries "approved": false and is refused until a
	// person sets it to true.
	CatalogFile string `yaml:"catalog_file,omitempty"`
	// Discovered records where a discover draft came from (informational).
	Discovered *Discovered `yaml:"discovered,omitempty"`
	// Text replaces generic wording by key (see Text). {help}, {title} and
	// {probe} are filled everywhere; some keys take more (documented at the call).
	Texts map[string]string `yaml:"text"`
	// catalogPath is CatalogFile resolved against the profile's folder (File).
	catalogPath string
}

// CatalogFilePath is the local catalog file, resolved ("" when none).
func (p *Profile) CatalogFilePath() string { return p.catalogPath }

var (
	reName      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	rePartition = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	// reHostKey is one public host key: type and base64 blob, nothing else
	// (no host pattern, no marker, no options).
	reHostKey = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]{40,}={0,2}$`)
)

// ValidHostKey checks one pinned host key ("ssh-ed25519 AAAA...").
func ValidHostKey(k string) error {
	if !reHostKey.MatchString(k) {
		return fmt.Errorf("host key %.40q is not \"<type> <base64>\"", k)
	}
	return nil
}

// Generic is the empty profile: generic wording, no roles, no charge, home in
// /home/<user>.
func Generic() *Profile {
	p := &Profile{Name: "generic", Approved: true}
	p.fill()
	return p
}

// fill sets defaults: charge model none, home /home/{user}, and job logs
// readable under home and scratch.
func (p *Profile) fill() {
	if p.Charge.Model == "" {
		p.Charge.Model = ChargeNone
	}
	if p.Paths.Home == "" {
		p.Paths.Home = "/home/{user}"
	}
	if len(p.Paths.LogRoots) == 0 {
		p.Paths.LogRoots = []string{p.Paths.Home + "/"}
		if p.Paths.Scratch != "" {
			p.Paths.LogRoots = append(p.Paths.LogRoots, p.Paths.Scratch+"/")
		}
	}
}

// Parse reads and validates a profile.
func Parse(b []byte) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("site profile: %w", err)
	}
	p.fill()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks a profile; an unapproved profile is refused.
func (p *Profile) Validate() error {
	if !reName.MatchString(p.Name) {
		return fmt.Errorf("site profile: name %q must be lowercase letters, digits and dashes", p.Name)
	}
	if !p.Approved {
		return fmt.Errorf("site profile %s is not approved: review it, then set approved: true", p.Name)
	}
	for _, k := range p.HostKeys {
		if err := ValidHostKey(k); err != nil {
			return fmt.Errorf("site profile %s: %w", p.Name, err)
		}
	}
	if c := p.CatalogFile; c != "" && (path.IsAbs(c) || path.Clean(c) != c || strings.HasPrefix(c, "../") || c == ".." || !strings.HasSuffix(c, ".json")) {
		return fmt.Errorf("site profile %s: catalog_file %q must be a clean .json path inside the profile's folder", p.Name, c)
	}
	if p.Catalog != "" && p.CatalogFile != "" {
		return fmt.Errorf("site profile %s: set catalog or catalog_file, not both", p.Name)
	}
	if p.Catalog != "" && (!path.IsAbs(p.Catalog) || path.Clean(p.Catalog) != p.Catalog || !strings.HasSuffix(p.Catalog, ".json")) {
		return fmt.Errorf("site profile %s: catalog %q must be an absolute, clean .json path", p.Name, p.Catalog)
	}
	for _, d := range []string{p.Paths.Home, p.Paths.Scratch} {
		if d != "" && !validUserDir(d) {
			return fmt.Errorf("site profile %s: folder %q must be absolute, clean and contain {user}", p.Name, d)
		}
	}
	for _, r := range p.Paths.LogRoots {
		if !strings.HasSuffix(r, "/") || !validUserDir(strings.TrimSuffix(r, "/")) {
			return fmt.Errorf("site profile %s: log root %q must be an absolute folder with {user}, ending in /", p.Name, r)
		}
	}
	parts := p.Partitions
	names := []string{parts.Probe, parts.GPU, parts.HighMem, parts.General, parts.Fast}
	names = append(names, parts.FallbackIdle...)
	names = append(names, parts.FallbackCold...)
	names = append(names, parts.AVX2Only...)
	for _, m := range []map[string]string{parts.CPU, parts.TimeHint, parts.MultiNodeMPI} {
		for k := range m {
			names = append(names, k)
		}
	}
	for k := range p.Charge.Rates {
		names = append(names, k)
	}
	for _, n := range names {
		if n != "" && !rePartition.MatchString(n) {
			return fmt.Errorf("site profile %s: %q is not a partition name", p.Name, n)
		}
	}
	switch p.Charge.Model {
	case ChargeUSDPerNodeHour, ChargeNone:
	case ChargeSUPerCoreHour:
		if len(p.Charge.Rates) == 0 {
			return fmt.Errorf("site profile %s: su_per_core_hour needs charge.rates per partition", p.Name)
		}
	default:
		return fmt.Errorf("site profile %s: charge.model must be %s, %s or %s", p.Name, ChargeUSDPerNodeHour, ChargeSUPerCoreHour, ChargeNone)
	}
	for k, v := range p.Charge.Rates {
		if v < 0 {
			return fmt.Errorf("site profile %s: charge rate for %s is negative", p.Name, k)
		}
	}
	return nil
}

func validUserDir(d string) bool {
	return path.IsAbs(d) && path.Clean(d) == d && strings.Count(d, "{user}") == 1 && !strings.ContainsAny(d, " \x00\n")
}

// Embedded loads a profile shipped in the binary.
func Embedded(name string) (*Profile, error) {
	if !reName.MatchString(name) {
		return nil, fmt.Errorf("site name %q is not valid", name)
	}
	b, err := fs.ReadFile(sites.FS, name+"/site.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no site profile named %q (shipped: %s); use site_file for your own", name, strings.Join(Names(), ", "))
	}
	if err != nil {
		return nil, err
	}
	sp, err := Parse(b)
	if err == nil && sp.CatalogFile != "" {
		return nil, fmt.Errorf("site profile %s: catalog_file only works in your own site_file profile", name)
	}
	return sp, err
}

// File loads a profile from a file; catalog_file is resolved next to it.
func File(p string) (*Profile, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("site profile: %w", err)
	}
	sp, err := Parse(b)
	if err != nil {
		return nil, err
	}
	if sp.CatalogFile != "" {
		sp.catalogPath = filepath.Join(filepath.Dir(p), filepath.FromSlash(sp.CatalogFile))
	}
	return sp, nil
}

// Names lists the embedded profiles.
func Names() []string {
	var out []string
	ents, _ := fs.ReadDir(sites.FS, ".")
	for _, e := range ents {
		if e.IsDir() {
			if _, err := fs.Stat(sites.FS, e.Name()+"/site.yaml"); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

// Text returns the site's wording for key, else def, with {help}, {title} and
// {probe} filled in, then each extra name/value pair ({partition} ...).
func (p *Profile) Text(key, def string, kv ...string) string {
	t := def
	if v, ok := p.Texts[key]; ok {
		t = v
	}
	pairs := append([]string{"{help}", firstNonEmpty(p.Help, "the cluster's support team"), "{title}", firstNonEmpty(p.Title, "the cluster"),
		"{probe}", p.Partitions.Probe, "{gpu}", p.Partitions.GPU, "{highmem}", p.Partitions.HighMem,
		"{general}", p.Partitions.General, "{fast}", p.Partitions.Fast}, kv...)
	return strings.NewReplacer(pairs...).Replace(t)
}

// HasText reports whether the site words key itself.
func (p *Profile) HasText(key string) bool { _, ok := p.Texts[key]; return ok }

// Dir expands a {user} folder ("" when the profile has none).
func Dir(tmpl, user string) string {
	if tmpl == "" {
		return ""
	}
	return strings.ReplaceAll(tmpl, "{user}", user)
}

// CPUNote is what a partition's nodes can run ("" when the site does not say).
func (p *Profile) CPUNote(part string) string {
	if n, ok := p.Partitions.CPU[part]; ok {
		return n
	}
	return p.Partitions.CPUDefault
}

// AVX2Only reports nodes without AVX-512.
func (p *Profile) AVX2Only(part string) bool { return contains(p.Partitions.AVX2Only, part) }

// IsStockout reports a node reason that means the provider has no capacity.
func (p *Profile) IsStockout(reason string) bool {
	for _, s := range p.Stockout {
		if s != "" && strings.Contains(reason, s) {
			return true
		}
	}
	return false
}

// Model is the charge model (none when unset).
func (p *Profile) Model() string { return firstNonEmpty(p.Charge.Model, ChargeNone) }

// Unit is the charge unit people see: "USD", "SU" or "" (no charge).
func (p *Profile) Unit() string {
	switch p.Model() {
	case ChargeUSDPerNodeHour:
		return "USD"
	case ChargeSUPerCoreHour:
		return "SU"
	}
	return ""
}

// TitleOr is the site's title, or def when it has none.
func (p *Profile) TitleOr(def string) string { return firstNonEmpty(p.Title, def) }

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
