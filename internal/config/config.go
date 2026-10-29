// Package config loads ~/.config/clusterwand/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SSH says how to reach the login node: any host or alias your own OpenSSH
// config understands (keys, agent, ProxyJump and MFA stay in ~/.ssh/config).
type SSH struct {
	Host           string `yaml:"host"`
	ControlPersist int    `yaml:"control_persist"`
	ConnectTimeout int    `yaml:"connect_timeout"`
}

// TTL holds cache lifetimes per data kind.
type TTL struct {
	Queue   Duration `yaml:"queue"`
	Nodes   Duration `yaml:"nodes"`
	Acct    Duration `yaml:"acct"`
	Catalog Duration `yaml:"catalog"`
	Modules Duration `yaml:"modules"`
}

// Limits caps output sizes.
type Limits struct {
	LogLines    int `yaml:"log_lines"`
	ListRows    int `yaml:"list_rows"`
	ScriptBytes int `yaml:"script_bytes"`
	UntrustedCh int `yaml:"untrusted_chars"`
	CallsPerMin int `yaml:"calls_per_min"`
}

// Caps bound what the A1 tier may start. A plan over any cap is refused.
type Caps struct {
	MaxNodes          int     `yaml:"max_nodes"`
	MaxHours          float64 `yaml:"max_hours"`
	MaxCostPerJobUSD  float64 `yaml:"max_cost_usd_per_job"`
	MaxCostPerDayUSD  float64 `yaml:"max_cost_usd_per_day"`
	MaxSubmitsPerDay  int     `yaml:"max_submits_per_day"`
	ConfirmTTLMinutes int     `yaml:"confirm_ttl_minutes"`
	// Service-unit caps for sites charged in SU (charge model su_per_core_hour);
	// 0 = the site profile's charge.max_su_per_job / max_su_per_day.
	MaxSUPerJob float64 `yaml:"max_su_per_job,omitempty"`
	MaxSUPerDay float64 `yaml:"max_su_per_day,omitempty"`
}

// Config is the whole file.
type Config struct {
	// Site names a shipped site profile (sites/<name>/site.yaml); SiteFile is
	// a profile of your own. With neither, a cluster name that matches a shipped
	// profile selects it, else a generic profile is used. Values set here
	// (catalog_path, log_roots, env_check_partition, usd_per_node_hour) win over
	// the profile.
	Site          string             `yaml:"site,omitempty"`
	SiteFile      string             `yaml:"site_file,omitempty"`
	Cluster       string             `yaml:"cluster"`
	ClusterUser   string             `yaml:"cluster_user,omitempty"` // default: `id -un` on the login node
	Backend       string             `yaml:"backend"`                // ssh | fixture
	FixturesDir   string             `yaml:"fixtures_dir,omitempty"`
	SSH           SSH                `yaml:"ssh"`
	CatalogPath   string             `yaml:"catalog_path,omitempty"` // default: the site profile's catalog
	LogRoots      []string           `yaml:"log_roots,omitempty"`    // allowed log prefixes; {user} expands; default: the site's
	Costs         map[string]float64 `yaml:"usd_per_node_hour,omitempty"`
	ShowCost      bool               `yaml:"show_cost"`
	ShowCostTiers []string           `yaml:"show_cost_tiers,omitempty"`
	Tiers         []string           `yaml:"tiers"` // tiers granted to the local user
	TTL           TTL                `yaml:"cache_ttl"`
	Limits        Limits             `yaml:"limits"`
	AuditPath     string             `yaml:"audit_path"`
	Caps          Caps               `yaml:"caps"`
	StatePath     string             `yaml:"state_path"`  // A1 plans, tokens and spend ledger
	ResultsDir    string             `yaml:"results_dir"` // local folder for downloaded results
	JobsRoot      string             `yaml:"jobs_root"`   // remote folder under $HOME for submitted jobs
	// EnvPartition is where env_check runs its one-core job (never the login
	// node); default: the site profile's probe partition.
	EnvPartition string `yaml:"env_check_partition,omitempty"`
	// Allocation configures optional allocations (local single-user CLI path).
	Allocation *AllocationConfig `yaml:"allocation,omitempty"`
	Path       string            `yaml:"-"`
}

// AllocationLabConfig is optional lab allocation in CLI/local config.
type AllocationLabConfig struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	AllocationUSD float64  `yaml:"allocation_usd"`
	Period        string   `yaml:"period,omitempty"`
	Members       []string `yaml:"members,omitempty"`
}

// AllocationConfig configures allocation for the local single-user CLI path.
type AllocationConfig struct {
	PersonUSD float64              `yaml:"person_usd,omitempty"`
	Lab       *AllocationLabConfig `yaml:"lab,omitempty"`
}

// Duration is a time.Duration that reads "20s" / "10m" in YAML.
type Duration struct{ time.Duration }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("duration %q: %w", n.Value, err)
	}
	d.Duration = v
	return nil
}

// MarshalYAML writes the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// rePartitionName matches a Slurm partition name.
var rePartitionName = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Default returns a configuration with every default filled in (no SSH target).
func Default() Config {
	return Config{
		Backend:  "ssh",
		SSH:      SSH{ControlPersist: 900, ConnectTimeout: 30},
		ShowCost: true,
		Tiers:    []string{"R1"},
		TTL: TTL{
			Queue:   Duration{20 * time.Second},
			Nodes:   Duration{30 * time.Second},
			Acct:    Duration{60 * time.Second},
			Catalog: Duration{time.Hour},
			Modules: Duration{time.Hour},
		},
		Limits: Limits{
			LogLines:    1000,
			ListRows:    200,
			ScriptBytes: 64 * 1024,
			UntrustedCh: 16000,
			CallsPerMin: 60,
		},
		AuditPath:  "~/.local/share/clusterwand/audit.jsonl",
		StatePath:  "~/.local/share/clusterwand/a1.json",
		ResultsDir: "~/clusterwand-results",
		JobsRoot:   "clusterwand-jobs",
		Caps: Caps{MaxNodes: 4, MaxHours: 24, MaxCostPerJobUSD: 25, MaxCostPerDayUSD: 50,
			MaxSubmitsPerDay: 20, ConfirmTTLMinutes: 10},
	}
}

// DefaultPath is ~/.config/clusterwand/config.yaml (XDG_CONFIG_HOME honoured).
func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "clusterwand", "config.yaml")
}

// ErrNoConfig means the config file does not exist yet.
var ErrNoConfig = errors.New("no config file")

// Load reads path (or the default path) over the defaults.
func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	c := Default()
	c.Path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("%w at %s (run `clusterwand config init`)", ErrNoConfig, path)
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.Validate()
}

// Validate checks the fields that would otherwise fail later and obscurely.
func (c Config) Validate() error {
	switch c.Backend {
	case "ssh":
		if c.SSH.Host == "" {
			return errors.New("config: ssh backend needs ssh.host (a host or alias from your ~/.ssh/config)")
		}
	case "fixture":
		if c.FixturesDir == "" {
			return errors.New("config: fixture backend needs fixtures_dir")
		}
	default:
		return fmt.Errorf("config: unknown backend %q (ssh or fixture)", c.Backend)
	}
	if c.EnvPartition != "" && !rePartitionName.MatchString(c.EnvPartition) {
		return errors.New("config: env_check_partition must be a partition name (default: the site's probe partition)")
	}
	if c.Site != "" && c.SiteFile != "" {
		return errors.New("config: set site or site_file, not both")
	}
	if c.Caps.MaxSUPerJob < 0 || c.Caps.MaxSUPerDay < 0 {
		return errors.New("config: SU caps must not be negative")
	}
	if c.JobsRoot != "clusterwand-jobs" {
		return errors.New("config: jobs_root must be clusterwand-jobs (other folders are not supported yet)")
	}
	if c.Caps.MaxNodes < 1 || c.Caps.MaxHours <= 0 || c.Caps.MaxCostPerJobUSD <= 0 || c.Caps.MaxCostPerDayUSD <= 0 || c.Caps.ConfirmTTLMinutes < 1 {
		return errors.New("config: caps must all be positive")
	}
	for _, t := range c.Tiers {
		switch t {
		case "R1", "R2", "A1":
		default:
			return fmt.Errorf("config: unknown tier %q (R1, R2, A1)", t)
		}
	}
	return nil
}

// HasTier reports whether the local user holds a tier.
func (c Config) HasTier(t string) bool {
	for _, x := range c.Tiers {
		if x == t {
			return true
		}
	}
	return false
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// Example is the file `clusterwand config init` writes.
const Example = `# clusterwand configuration
# Site profile: a shipped one (site: <name>, see sites/ in the repo) or your own
# file (site_file: ~/.config/clusterwand/mysite.yaml). It holds the cluster's
# paths, partition roles, catalog location and charge model.
site: ursa-major
backend: ssh              # ssh | fixture

ssh:
  # Any host or alias your own OpenSSH config reaches without a prompt
  # (keys, agent, ProxyJump, MFA: see docs/SSH.md)
  host: mycluster
  control_persist: 900
  connect_timeout: 30

# The site profile sets these; uncomment to override
# catalog_path: /path/to/catalog.json        # partitions, prices, modules, recipes
# log_roots: ["/home/{user}/"]               # job logs are read only under these
# env_check_partition: debug                 # env_check's one-core job

# Cost figures in usage and status output (rates come from the catalog or the
# site profile; override dollars per node-hour per partition below)
show_cost: true
# usd_per_node_hour: {mypartition: 1.50}

# Tiers for the local user: R1 own jobs + cluster facts, R2 staff (all users),
# A1 submit/cancel/hold/release own jobs (two-step confirm, capped below)
tiers: [R1]

# A1 caps: a plan over any of these is refused (worst case = nodes x hours x price)
caps: {max_nodes: 4, max_hours: 24, max_cost_usd_per_job: 25, max_cost_usd_per_day: 50,
       max_submits_per_day: 20, confirm_ttl_minutes: 10}
results_dir: ~/clusterwand-results   # clusterwand job results <id> downloads here
jobs_root: clusterwand-jobs           # submitted jobs run in ~/clusterwand-jobs/<stamp>-<name> on the cluster
state_path: ~/.local/share/clusterwand/a1.json

cache_ttl: {queue: 20s, nodes: 30s, acct: 60s, catalog: 1h, modules: 1h}
limits: {log_lines: 200, list_rows: 200, script_bytes: 65536, untrusted_chars: 16000, calls_per_min: 60}
audit_path: ~/.local/share/clusterwand/audit.jsonl
`
