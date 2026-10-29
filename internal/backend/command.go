// Package backend runs scheduler commands on the cluster.
//
// The allow-list is enforced by construction: a Command can only be made by the
// constructors in this file and files.go, each of which validates its arguments. There is no
// way to build an arbitrary command, and arguments are quoted individually before
// they reach the remote shell.
package backend

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind groups commands by how long their output may be cached.
type Kind int

// Cache kinds.
const (
	KindQueue Kind = iota
	KindNodes
	KindAcct
	KindCatalog
	KindModules
	KindNoCache
	KindStorage // storage_usage: df/du are slow, cached 5 minutes
)

// Command is one allow-listed command. Fields are unexported on purpose.
type Command struct {
	argv []string
	kind Kind
	// okExit lists non-zero exit codes that still carry useful output.
	okExit []int
	// stdin is sent to the remote command (the batch script for sbatch).
	stdin []byte
	// merge sends stderr into stdout (sbatch --test-only reports on stderr).
	merge bool
	// write marks commands that change cluster state (A1 tier).
	write bool
	// big raises the timeout for bulk transfers (results download).
	big bool
	// timeout overrides the default per-command timeout when set.
	timeout time.Duration
	// public marks output that is the same for every user (node state, the
	// whole queue, the partition list, the site catalog). The hosted server
	// may cache it once for everyone instead of once per person. Never set it
	// on a command whose output depends on who runs it (their jobs, files,
	// accounting, modules seen through their environment).
	public bool
}

// Public reports whether the output is the same for every caller.
func (c Command) Public() bool { return c.public }

// Stdin is the data sent to the command, if any.
func (c Command) Stdin() []byte { return c.stdin }

// Merge reports whether stderr is folded into stdout.
func (c Command) Merge() bool { return c.merge }

// Write reports whether the command changes cluster state.
func (c Command) Write() bool { return c.write }

// Argv returns a copy of the argument vector.
func (c Command) Argv() []string { return append([]string(nil), c.argv...) }

// Kind is the cache kind.
func (c Command) Kind() Kind { return c.kind }

// String is the command as it would be typed (for audit and `source`).
func (c Command) String() string {
	q := make([]string, len(c.argv))
	for i, a := range c.argv {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// Backend runs commands.
type Backend interface {
	Run(ctx context.Context, c Command) ([]byte, error)
	Name() string
}

// ErrUnreachable means the command never ran (ssh failed, token expired).
var ErrUnreachable = errors.New("cannot reach the cluster")

var (
	reUser      = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,63}$`)
	reModule    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,80}(/[A-Za-z0-9._+-]{1,40})?$`)
	reSince     = regexp.MustCompile(`^(now-\d{1,4}(days|hours|minutes)|\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}(:\d{2})?)?)$`)
	reStateList = regexp.MustCompile(`^[A-Z_]{2,20}(,[A-Z_]{2,20}){0,10}$`)
)

// ValidJobID accepts "260" and array forms "260_3".
func ValidJobID(id string) error {
	if !regexp.MustCompile(`^\d{1,10}(_\d{1,7})?$`).MatchString(id) {
		return fmt.Errorf("job id %q: expected digits like 260 or 260_3", id)
	}
	return nil
}

// ValidUser checks a cluster user name.
func ValidUser(u string) error {
	if !reUser.MatchString(u) {
		return fmt.Errorf("user %q is not a valid cluster user name", u)
	}
	return nil
}

// ValidSince checks a sacct time (now-7days or 2026-09-01[T10:00]).
func ValidSince(s string) error {
	if !reSince.MatchString(s) {
		return fmt.Errorf("time %q: use now-7days, now-12hours or YYYY-MM-DD", s)
	}
	return nil
}

// ---- constructors (the allow-list) ----------------------------------------

// Whoami is `id -un`.
func Whoami() Command { return Command{argv: []string{"id", "-un"}, kind: KindCatalog} }

// SqueueUser lists one user's queued and running jobs.
func SqueueUser(user string) (Command, error) {
	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"squeue", "--json", "-u", user}, kind: KindQueue}, nil
}

// SqueueAll lists every job in the queue.
func SqueueAll() Command {
	return Command{argv: []string{"squeue", "--json"}, kind: KindQueue, public: true}
}

// SqueueJob shows one queued or running job. squeue exits 1 for a job that
// already left the queue; that is reported as an empty list, not an error.
func SqueueJob(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"squeue", "--json", "-j", id}, kind: KindQueue, okExit: []int{1}}, nil
}

// SqueueStart is the scheduler's start-time estimate for one pending job.
func SqueueStart(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"squeue", "--json", "--start", "-j", id}, kind: KindQueue, okExit: []int{1}}, nil
}

// SqueueJobs is the live queue rows of the given jobs, never cached: a program
// watching its own jobs must see a finished job at once (job_ids). Callers filter
// the rows to their own user.
func SqueueJobs(ids []string) (Command, error) {
	list, err := jobList(ids)
	if err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"squeue", "--json", "-j", list}, kind: KindNoCache, okExit: []int{1}}, nil
}

// SacctJobs is the accounting rows of the given jobs, never cached (see SqueueJobs).
func SacctJobs(ids []string) (Command, error) {
	list, err := jobList(ids)
	if err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"sacct", "--json", "-j", list}, kind: KindNoCache}, nil
}

func jobList(ids []string) (string, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return "", fmt.Errorf("1 to 100 job ids, got %d", len(ids))
	}
	for _, id := range ids {
		if err := ValidJobID(id); err != nil {
			return "", err
		}
	}
	return strings.Join(ids, ","), nil
}

// SacctJob is the accounting record of one job.
func SacctJob(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"sacct", "--json", "-j", id}, kind: KindAcct}, nil
}

// SacctUser lists a user's jobs that ran between since and until ("" = now).
func SacctUser(user, since, until string) (Command, error) {
	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return sacctRange([]string{"-u", user}, since, until)
}

// SacctAll lists every user's jobs between since and until (staff tier).
func SacctAll(since, until string) (Command, error) {
	return sacctRange([]string{"-a"}, since, until)
}

func sacctRange(who []string, since, until string) (Command, error) {
	if err := ValidSince(since); err != nil {
		return Command{}, err
	}
	argv := append([]string{"sacct", "--json"}, who...)
	argv = append(argv, "-S", since)
	if until != "" {
		if err := ValidSince(until); err != nil {
			return Command{}, err
		}
		argv = append(argv, "-E", until)
	}
	return Command{argv: argv, kind: KindAcct}, nil
}

// SacctStates filters a user's jobs by state list (FAILED,TIMEOUT).
func SacctStates(user, since, states string) (Command, error) {
	c, err := SacctUser(user, since, "")
	if err != nil {
		return c, err
	}
	if !reStateList.MatchString(states) {
		return Command{}, fmt.Errorf("states %q: use names like FAILED,TIMEOUT", states)
	}
	c.argv = append(c.argv, "-s", states)
	return c, nil
}

// sacctSummaryTemplate prints job and step rows as plain text (SPEC section 23):
// --json spends nearly all its time serializing (a week of one person's jobs:
// 19.6 s as JSON, 0.2 s as text). Epoch times keep the parse exact. The
// validated selection (-u user or -a, -S, -E) arrives as positional parameters.
//
// F2: Slurm 21.08 has no Restarts field and refuses the whole list. There the
// template asks for the list without it and puts an empty Restarts column back
// (sed replaces the 15th "|", the one before JobName, with "||"), so the parser
// sees the same 17 columns and reads 0 restarts.
const sacctSummaryTemplate = `SLURM_TIME_FORMAT=%s exec sacct -n -P --noconvert -o ` + slurmFields + ` "$@"`

// sacctSummaryCompat runs sacctSummaryTemplate where sacct knows Restarts.
const sacctSummaryCompat = `if sacct --helpformat | grep -qw Restarts; then ` + sacctSummaryTemplate + `; fi; set -o pipefail; SLURM_TIME_FORMAT=%s sacct -n -P --noconvert -o ` + slurmLegacyFields + ` "$@" | sed 's/|/||/15'`

// slurmLegacyFields is slurmFields without Restarts.
const slurmLegacyFields = "JobIDRaw,User,Partition,State,Reason,ExitCode,NodeList,NNodes,Submit,Start,End,ElapsedRaw,TotalCPU,AllocTRES,MaxRSS,JobName"

// slurmFields mirrors slurm.SacctFields (the backend does not import slurm;
// TestSacctSummaryFields keeps the two equal).
const slurmFields = "JobIDRaw,User,Partition,State,Reason,ExitCode,NodeList,NNodes,Submit,Start,End,ElapsedRaw,TotalCPU,AllocTRES,MaxRSS,Restarts,JobName"

// SacctSummaryUser lists one user's jobs between since and until ("" = now) as
// plain-text rows for slurm.ParseSacctRows.
func SacctSummaryUser(user, since, until string) (Command, error) {
	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return sacctSummary([]string{"-u", user}, since, until)
}

// SacctSummaryUsers lists multiple users' jobs between since and until ("" = now) as
// plain-text rows for slurm.ParseSacctRows.
func SacctSummaryUsers(users []string, since, until string) (Command, error) {
	if len(users) == 0 {
		return Command{}, errors.New("users required")
	}
	for _, u := range users {
		if err := ValidUser(u); err != nil {
			return Command{}, err
		}
	}
	return sacctSummary([]string{"-u", strings.Join(users, ",")}, since, until)
}

// SacctSummaryAll is SacctSummaryUser for every user (staff tier, health).
func SacctSummaryAll(since, until string) (Command, error) {
	return sacctSummary([]string{"-a"}, since, until)
}

func sacctSummary(who []string, since, until string) (Command, error) {
	r, err := sacctRange(who, since, until)
	if err != nil {
		return Command{}, err
	}
	argv := append([]string{"bash", "-c", sacctSummaryCompat, "clusterwand"}, r.argv[2:]...)
	return Command{argv: argv, kind: KindAcct}, nil
}

// SacctSummaryTemplateForTest lets fixtures answer the summary template.
func SacctSummaryTemplateForTest() string { return sacctSummaryCompat }

// SacctFieldsForTest is the field list the template prints.
func SacctFieldsForTest() string { return slurmFields }

// Sinfo is `sinfo --json`.
func Sinfo() Command {
	return Command{argv: []string{"sinfo", "--json"}, kind: KindNodes, public: true}
}

// PartitionSharing is `sinfo -h -o %R|%h`: each partition's OverSubscribe setting
// (EXCLUSIVE, NO, YES, FORCE...), which says whether jobs share nodes. The JSON
// output of Slurm 25.11 carries no such field (its oversubscribe flags stay empty
// for EXCLUSIVE partitions), so the format string is the reliable source.
func PartitionSharing() Command {
	return Command{argv: []string{"sinfo", "-h", "-o", "%R|%h"}, kind: KindNodes, public: true}
}

// Nodes is `scontrol show nodes --json`.
func Nodes() Command {
	return Command{argv: []string{"scontrol", "show", "nodes", "--json"}, kind: KindNodes, public: true}
}

// ---- F2: plain-text Slurm reads (the primary path) ----------------------------
//
// Text formats read the same on Slurm 20.11 to 25.11; --json needs Slurm 25.11's
// data_parser and is only used on sites whose profile sets slurm.json. Fields are
// split by \x1f (ASCII unit separator, never in a name or path) and times print as
// epoch seconds. Selections (-u user, -j ids) are validated and arrive as
// positional parameters of a fixed template.

// squeueTextFields mirrors slurm.SqueueFields (TestTextFieldsMatch keeps them equal).
const squeueTextFields = "JobID,ArrayJobID,ArrayTaskID,UserName,Account,Partition,State,NumNodes,NumCPUs,tres-alloc,TimeLimit,SubmitTime,StartTime,EndTime,RestartCnt,Reason,NodeList,WorkDir,StdOut,StdErr,Name"

// squeueTextTemplate prints one job per line, every field but the last (Name)
// followed by \x1f; ":" with no width means "do not pad or truncate".
var squeueTextTemplate = `d=$'\037'; SLURM_TIME_FORMAT=%s exec squeue -h -O "` + squeueFormat() + `" "$@"`

func squeueFormat() string {
	fs := strings.Split(squeueTextFields, ",")
	for i := range fs {
		if i < len(fs)-1 {
			fs[i] += ":$d"
		} else {
			fs[i] += ":"
		}
	}
	return strings.Join(fs, ",")
}

// sacctTextFields mirrors slurm.SacctJobFields; sacctLegacyFields drops the
// fields older Slurm lacks (21.08 has no Restarts, StdOut or StdErr).
const (
	sacctTextFields   = "JobID,JobIDRaw,UID,User,Account,Partition,State,Reason,ExitCode,NodeList,NNodes,Submit,Start,End,ElapsedRaw,TotalCPU,TimelimitRaw,AllocTRES,ReqTRES,MaxRSS,Restarts,WorkDir,StdOut,StdErr,SubmitLine,JobName"
	sacctLegacyFields = "JobID,JobIDRaw,UID,User,Account,Partition,State,Reason,ExitCode,NodeList,NNodes,Submit,Start,End,ElapsedRaw,TotalCPU,TimelimitRaw,AllocTRES,ReqTRES,MaxRSS,WorkDir,SubmitLine,JobName"
)

// sacctTextTemplate prints the per-job accounting record with a header line (the
// parser maps columns by name), picking the field list this sacct knows.
const sacctTextTemplate = `f=` + sacctTextFields + `; sacct --helpformat | grep -qw StdOut || f=` + sacctLegacyFields + `; SLURM_TIME_FORMAT=%s exec sacct -P --noconvert --delimiter=$'\037' -o "$f" "$@"`

// nodesTextTemplate prints the Slurm release, then one node per line.
const nodesTextTemplate = `scontrol --version; SLURM_TIME_FORMAT=%s exec scontrol -o show nodes`

// SqueueAllText lists every job in the queue (text).
func SqueueAllText() Command {
	return Command{argv: []string{"bash", "-c", squeueTextTemplate, "clusterwand"}, kind: KindQueue, public: true}
}

// SqueueUserText lists one user's queued and running jobs (text).
func SqueueUserText(user string) (Command, error) {
	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"bash", "-c", squeueTextTemplate, "clusterwand", "-u", user}, kind: KindQueue}, nil
}

// SqueueJobText shows one queued or running job (text); a job that left the queue
// makes squeue exit 1, which reads as no rows.
func SqueueJobText(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"bash", "-c", squeueTextTemplate, "clusterwand", "-j", id}, kind: KindQueue, okExit: []int{1}}, nil
}

// SqueueJobsText is the live queue rows of the given jobs, never cached (text).
func SqueueJobsText(ids []string) (Command, error) {
	list, err := jobList(ids)
	if err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"bash", "-c", squeueTextTemplate, "clusterwand", "-j", list}, kind: KindNoCache, okExit: []int{1}}, nil
}

// SacctJobText is the accounting record of one job, steps included (text).
func SacctJobText(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"bash", "-c", sacctTextTemplate, "clusterwand", "-j", id}, kind: KindAcct}, nil
}

// SacctJobsText is the accounting records of the given jobs, never cached (text).
func SacctJobsText(ids []string) (Command, error) {
	list, err := jobList(ids)
	if err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"bash", "-c", sacctTextTemplate, "clusterwand", "-j", list}, kind: KindNoCache}, nil
}

// SacctScript is a job's stored batch script (`sacct -n -B -j <id>`, Slurm
// 21.08+); "NONE" when the cluster does not store scripts.
func SacctScript(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"sacct", "-n", "-B", "-j", id}, kind: KindAcct}, nil
}

// NodesText is `scontrol -o show nodes` after the Slurm release line.
func NodesText() Command {
	return Command{argv: []string{"bash", "-c", nodesTextTemplate, "clusterwand"}, kind: KindNodes, public: true}
}

// PartitionsText is `scontrol -o show partition`: limits and who may use each
// partition (AllowAccounts, DenyAccounts, AllowGroups).
func PartitionsText() Command {
	return Command{argv: []string{"scontrol", "-o", "show", "partition"}, kind: KindNodes, public: true}
}

// Assoc is the caller's Slurm associations: default account, then one row per
// account (and partition, when the association names one).
func Assoc(user string) (Command, error) {
	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"sacctmgr", "-n", "-P", "show", "user", user, "withassoc", "format=DefaultAccount,Account,Partition"}, kind: KindCatalog}, nil
}

// Groups is `id -Gn`: the caller's Unix groups (partitions with AllowGroups).
func Groups() Command { return Command{argv: []string{"id", "-Gn"}, kind: KindCatalog} }

// SlurmVersion is `sinfo --version`.
func SlurmVersion() Command {
	return Command{argv: []string{"sinfo", "--version"}, kind: KindCatalog, public: true}
}

// ---- F3: discover (read-only probes of a new site) ------------------------------

// ShowConfig is `scontrol show config` (readable by every user; discover keeps
// only a few keys from it).
func ShowConfig() Command {
	return Command{argv: []string{"scontrol", "show", "config"}, kind: KindCatalog, public: true}
}

// moduleAvailTemplate lists every module the login shell can load, one per
// line (Lmod and Tcl Environment Modules both print to stderr; folder headers
// end in ":").
const moduleAvailTemplate = `module -t avail 2>&1`

// ModuleAvail is `module -t avail` in a login shell (exit 1: no module command,
// the empty answer means "no module system").
func ModuleAvail() Command {
	return Command{argv: []string{"bash", "-lc", moduleAvailTemplate}, kind: KindModules, okExit: []int{1, 127}}
}

// discoverEnvTemplate reports the caller's home, which candidate scratch
// folders of theirs exist (only folders named after $USER, plus $SCRATCH), and
// the module system. Nothing is written; only `test -d` and the environment.
const discoverEnvTemplate = `printf 'home\t%s\n' "$HOME"; printf 'scratch_env\t%s\n' "${SCRATCH:-}"; ` +
	`for d in "${SCRATCH:-}" "/scratch/$USER" "/scratch/users/$USER" "/work/$USER" "/bigdata/$USER" "/rhome/$USER" "/lustre/scratch/$USER" "/gpfs/scratch/$USER"; do ` +
	`[ -n "$d" ] && [ -d "$d" ] && printf 'dir\t%s\n' "$d"; done; ` +
	`printf 'lmod\t%s\n' "${LMOD_VERSION:-}"; type module >/dev/null 2>&1 && printf 'module\tyes\n'; true`

// DiscoverEnv is discoverEnvTemplate in a login shell (module only exists there).
func DiscoverEnv() Command {
	return Command{argv: []string{"bash", "-lc", discoverEnvTemplate}, kind: KindCatalog}
}

// DiscoverTemplatesForTest lets tests answer the discover templates.
func DiscoverTemplatesForTest() (moduleAvail, env string) {
	return moduleAvailTemplate, discoverEnvTemplate
}

// TextTemplatesForTest lets fixtures answer the text templates.
func TextTemplatesForTest() (squeue, sacct, nodes string) {
	return squeueTextTemplate, sacctTextTemplate, nodesTextTemplate
}

// TextFieldsForTest are the field lists the templates print.
func TextFieldsForTest() (squeue, sacct, legacy string) {
	return squeueTextFields, sacctTextFields, sacctLegacyFields
}

// Catalog reads the cluster's published catalog (fixed path from config).
func Catalog(p string) (Command, error) {
	if !path.IsAbs(p) || path.Clean(p) != p || !strings.HasSuffix(p, ".json") {
		return Command{}, fmt.Errorf("catalog path %q must be an absolute, clean .json path", p)
	}
	return Command{argv: []string{"cat", "--", p}, kind: KindCatalog, public: true}, nil
}

// Tail reads the last n lines of a file. The caller must have checked the path
// against the job record and the allowed roots (see core.checkLogPath).
func Tail(p string, n int) (Command, error) {
	if !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\x00\n") {
		return Command{}, fmt.Errorf("log path %q must be absolute and clean", p)
	}
	if n < 1 || n > 2000 {
		return Command{}, fmt.Errorf("lines must be 1-2000")
	}
	return Command{argv: []string{"tail", "-n", strconv.Itoa(n), "--", p}, kind: KindNoCache}, nil
}

// ModuleShow is `module show NAME` (needs a login shell for Lmod).
func ModuleShow(name string) (Command, error) {
	return ModuleShowUnder(name, "")
}

// ModuleShowUnder is `module show NAME` after `module load PREREQ`. The site's Lmod
// tree is hierarchical: MPI-built packages (hdf5, fftw, petsc...) only exist in the
// module path once an MPI is loaded, so showing them needs that MPI first. An empty
// prereq is a plain `module show`.
func ModuleShowUnder(name, prereq string) (Command, error) {
	if !reModule.MatchString(name) {
		return Command{}, fmt.Errorf("module name %q has unexpected characters", name)
	}
	line := "module -t show " + name + " 2>&1"
	if prereq != "" {
		if !reModule.MatchString(prereq) {
			return Command{}, fmt.Errorf("module name %q has unexpected characters", prereq)
		}
		line = "module load " + prereq + " >/dev/null 2>&1; " + line
	}
	// exit 1 is Lmod's "not found": its warning (on stdout via 2>&1) is the answer
	return Command{argv: []string{"bash", "-lc", line}, kind: KindModules, okExit: []int{1}}, nil
}

// ---- quoting ----------------------------------------------------------------

var reSafe = regexp.MustCompile(`^[A-Za-z0-9_./=:,@%+-]+$`)

// Quote makes one argument safe for a POSIX shell.
func Quote(s string) string {
	if s != "" && reSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RemoteLine is the single string ssh passes to the remote shell.
func RemoteLine(c Command) string { return c.String() }

func okExit(c Command, code int) bool {
	for _, x := range c.okExit {
		if x == code {
			return true
		}
	}
	return false
}

// Timeout picks a per-command timeout.
func Timeout(c Command) time.Duration {
	switch {
	case c.timeout > 0:
		return c.timeout
	case c.big:
		return 15 * time.Minute
	case c.kind == KindAcct, c.write:
		return 120 * time.Second
	}
	return 60 * time.Second
}

// ---- A1: state-changing commands ---------------------------------------------

var (
	rePartition = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	// a Slurm partition list for sbatch -p: 1-4 valid names joined by commas, nothing else
	rePartitionList = regexp.MustCompile(`^[a-z0-9_-]{1,32}(,[a-z0-9_-]{1,32}){0,3}$`)
	reJobName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reJobsDir       = regexp.MustCompile(`^clusterwand-jobs/\d{8}-\d{6}-[a-z0-9-]{1,40}$`)
	reComment       = regexp.MustCompile(`^clusterwand:[0-9a-f]{12}$`)
	reRelPath       = regexp.MustCompile(`^[A-Za-z0-9._+][A-Za-z0-9._+/ -]{0,250}$`) // no leading "-" (option injection)
)

// SubmitOpts are the resources clusterwand enforces on the sbatch command line.
// Command-line options override #SBATCH directives in the script, so these are
// the limits that actually apply, whatever the script says.
type SubmitOpts struct {
	Partition string
	Nodes     int
	TimeMin   int
	JobName   string
	Comment   string // clusterwand:<plan hash prefix>
}

func (o SubmitOpts) validate() error {
	if !rePartitionList.MatchString(o.Partition) {
		return fmt.Errorf("partition %q is not a valid name", o.Partition)
	}
	if o.Nodes < 1 || o.Nodes > 1000 {
		return fmt.Errorf("nodes must be 1-1000")
	}
	if o.TimeMin < 1 || o.TimeMin > 60*24*30 {
		return fmt.Errorf("time limit must be 1 minute to 30 days")
	}
	if o.JobName != "" && !reJobName.MatchString(o.JobName) {
		return fmt.Errorf("job name %q has unexpected characters", o.JobName)
	}
	if !reComment.MatchString(o.Comment) {
		return fmt.Errorf("comment %q is not a clusterwand plan tag", o.Comment)
	}
	return nil
}

func (o SubmitOpts) flags() []string {
	f := []string{"--partition=" + o.Partition, fmt.Sprintf("--nodes=%d", o.Nodes),
		fmt.Sprintf("--time=%d", o.TimeMin), "--comment=" + o.Comment}
	if o.JobName != "" {
		f = append(f, "--job-name="+o.JobName)
	}
	return f
}

// SbatchTestOnly asks the scheduler whether and when the job would start,
// without submitting it (`sbatch --test-only`, script on stdin).
func SbatchTestOnly(o SubmitOpts, script []byte) (Command, error) {
	if err := o.validate(); err != nil {
		return Command{}, err
	}
	argv := append([]string{"sbatch", "--test-only"}, o.flags()...)
	return Command{argv: argv, kind: KindNoCache, stdin: script, merge: true}, nil
}

// submitTemplate is fixed text: the job folder and sbatch flags arrive as
// positional parameters, never spliced into the shell code.
const submitTemplate = `set -e; d="$HOME/$1"; shift; if [ -e "$d" ]; then echo "clusterwand: $d already exists" >&2; exit 3; fi; mkdir -p "$d"; cat > "$d/job.sbatch"; cd "$d"; exec sbatch --parsable --chdir="$d" "$@" job.sbatch`

// SubmitBatch writes the script into a new ~/clusterwand-jobs/<dir> folder and
// submits it with the enforced options. Returns the job id on stdout.
func SubmitBatch(dir string, o SubmitOpts, script []byte) (Command, error) {
	if !reJobsDir.MatchString(dir) {
		return Command{}, fmt.Errorf("job folder %q is not a clusterwand-jobs path", dir)
	}
	if err := o.validate(); err != nil {
		return Command{}, err
	}
	if len(script) == 0 {
		return Command{}, fmt.Errorf("empty script")
	}
	argv := append([]string{"bash", "-c", submitTemplate, "clusterwand", dir}, o.flags()...)
	return Command{argv: argv, kind: KindNoCache, stdin: script, write: true}, nil
}

// Scancel cancels one job.
func Scancel(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"scancel", id}, kind: KindNoCache, write: true}, nil
}

// Hold keeps a pending job from starting.
func Hold(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"scontrol", "hold", id}, kind: KindNoCache, write: true}, nil
}

// Release lets a held job start.
func Release(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"scontrol", "release", id}, kind: KindNoCache, write: true}, nil
}

// ---- results (read) -----------------------------------------------------------

func validDir(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\n") {
		return fmt.Errorf("folder %q must be an absolute, clean path", p)
	}
	return nil
}

// ValidRelPath checks a path inside a job folder: relative, no "..", no hidden
// leading dash, printable.
func ValidRelPath(rel string) error {
	if !reRelPath.MatchString(rel) || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") || rel == ".." || strings.Contains(rel, "/../") {
		return fmt.Errorf("file %q must be a relative path inside the job folder", rel)
	}
	return nil
}

// ListFiles lists regular files under a job folder: size, mtime, relative path.
func ListFiles(dir string) (Command, error) {
	if err := validDir(dir); err != nil {
		return Command{}, err
	}
	return Command{argv: []string{"find", dir, "-maxdepth", "4", "-type", "f", "-printf", `%s\t%T@\t%P\n`}, kind: KindNoCache}, nil
}

// Head reads the first n bytes of a file.
func Head(p string, n int) (Command, error) {
	if err := validDir(p); err != nil {
		return Command{}, err
	}
	if n < 1 || n > 1<<20 {
		return Command{}, fmt.Errorf("bytes must be 1-1048576")
	}
	return Command{argv: []string{"head", "-c", strconv.Itoa(n), "--", p}, kind: KindNoCache}, nil
}

// TarDir streams a gzip tar of a job folder (or listed files inside it).
func TarDir(dir string, files []string) (Command, error) {
	if err := validDir(dir); err != nil {
		return Command{}, err
	}
	argv := []string{"tar", "-C", dir, "-czf", "-", "--exclude-vcs"}
	if len(files) == 0 {
		argv = append(argv, ".")
	} else {
		argv = append(argv, "--")
		for _, f := range files {
			if err := ValidRelPath(f); err != nil {
				return Command{}, err
			}
			argv = append(argv, f)
		}
	}
	return Command{argv: argv, kind: KindNoCache, big: true}, nil
}
