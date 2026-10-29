package slurm

// Plain-text Slurm output (F2): the primary path. `--json` needs a matching
// data_parser plugin (Slurm 25.11 prints v0.0.44, older releases other shapes or
// nothing), while the text formats below read the same on Slurm 20.11 through
// 25.11:
//
//	squeue -h -O <SqueueFields>           (one job per line, fields split by \x1f)
//	sacct -P --noconvert --delimiter=\x1f (header line names the fields)
//	scontrol -o show nodes | partition    (one record per line, Key=Value)
//	sacctmgr -n -P show user <u> withassoc format=DefaultAccount,Account,Partition
//
// Every parser fills the same structs the JSON decoders fill, so the code above
// this package does not know which path produced them. Fields an older Slurm does
// not print stay zero; a line that cannot be read is an error, never a guess.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Delim separates fields in the squeue and sacct text formats: the ASCII unit
// separator cannot occur in a job name, path or submit line, unlike "|".
const Delim = "\x1f"

// SqueueFields is the squeue -O list; Name is last so nothing can follow it.
var SqueueFields = []string{"JobID", "ArrayJobID", "ArrayTaskID", "UserName", "Account", "Partition", "State",
	"NumNodes", "NumCPUs", "tres-alloc", "TimeLimit", "SubmitTime", "StartTime", "EndTime", "RestartCnt",
	"Reason", "NodeList", "WorkDir", "StdOut", "StdErr", "Name"}

// SacctJobFields is the per-job sacct list (job_show, job_explain, job_ids).
// JobName is last. Printed with a header, so a shorter list parses the same.
var SacctJobFields = []string{"JobID", "JobIDRaw", "UID", "User", "Account", "Partition", "State", "Reason",
	"ExitCode", "NodeList", "NNodes", "Submit", "Start", "End", "ElapsedRaw", "TotalCPU", "TimelimitRaw",
	"AllocTRES", "ReqTRES", "MaxRSS", "Restarts", "WorkDir", "StdOut", "StdErr", "SubmitLine", "JobName"}

// LegacySacctFields are the sacct fields Slurm before 23.02 lacks (21.08 answers
// `Invalid field requested: "Restarts"`). Without StdOut a finished job's log path
// is unknown; clusterwand then says so rather than guess slurm-<id>.out.
var LegacySacctFields = []string{"Restarts", "StdOut", "StdErr"}

// Without returns fields minus drop, keeping the order.
func Without(fields, drop []string) []string {
	var out []string
	for _, f := range fields {
		keep := true
		for _, d := range drop {
			if strings.EqualFold(f, d) {
				keep = false
			}
		}
		if keep {
			out = append(out, f)
		}
	}
	return out
}

// ---- squeue ---------------------------------------------------------------------

// ParseSqueueText reads `squeue -h -O <SqueueFields>` printed with
// SLURM_TIME_FORMAT=%s and \x1f after every field but the last.
func ParseSqueueText(b []byte) ([]QueueJob, error) {
	var jobs []QueueJob
	n := len(SqueueFields)
	for i, line := range lines(b) {
		f := strings.SplitN(line, Delim, n)
		if len(f) != n {
			return nil, fmt.Errorf("squeue row %d: %d fields, want %d", i+1, len(f), n)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(f[0]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("squeue row %d: job id %q", i+1, f[0])
		}
		j := QueueJob{JobID: id, ArrayJobID: numOf(f[1]), ArrayTaskID: numOf(f[2]), UserName: f[3], Account: f[4],
			Partition: f[5], JobState: []string{f[6]}, NodeCount: numOf(f[7]), CPUs: numOf(f[8]), TresAllocStr: null(f[9]),
			TimeLimit: limitOf(f[10]), SubmitTime: numOf(f[11]), StartTime: numOf(f[12]), EndTime: numOf(f[13]),
			RestartCnt: numOf(f[14]), StateReason: f[15], Nodes: null(f[16]), WorkDir: null(f[17]), Name: f[20]}
		if j.ArrayJobID.Int() == id && !j.ArrayTaskID.Set {
			j.ArrayJobID = Num{Set: true} // not an array job: --json says 0
		}
		// the memory the job holds per node, from its allocation (--json gives
		// memory_per_cpu or memory_per_node; their product with the job's size is
		// the same total)
		if mem := tresOf(j.TresAllocStr).Get("mem"); mem > 0 && j.NodeCount.Int() > 0 {
			j.MemoryPerNode = Num{Set: true, Number: float64(mem) / float64(j.NodeCount.Int())}
		}
		p := PatternJob{JobID: id, ArrayJobID: j.ArrayJobID.Int(), ArrayTaskID: j.ArrayTaskID, User: j.UserName,
			Name: j.Name, Nodes: j.Nodes, WorkDir: j.WorkDir}
		j.StdoutExpanded = ExpandPattern(null(f[18]), p)
		j.StderrExpanded = ExpandPattern(null(f[19]), p)
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// ---- sacct (one job) ------------------------------------------------------------

// ParseSacctText reads `sacct -P --noconvert --delimiter=\x1f -o <fields>` with
// its header line (SLURM_TIME_FORMAT=%s). The header names the columns, so any
// subset of SacctJobFields with JobName last parses. Step rows (<id>.batch,
// <id>.0, <id>.extern) become Steps of their job.
func ParseSacctText(b []byte) ([]AcctJob, error) {
	ls := lines(b)
	if len(ls) == 0 {
		return []AcctJob{}, nil
	}
	head := strings.Split(ls[0], Delim)
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"jobidraw", "state", "jobname"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("sacct header %q has no %s column", ls[0], need)
		}
	}
	if col["jobname"] != len(head)-1 {
		return nil, fmt.Errorf("sacct header: JobName must be the last column")
	}
	var jobs []AcctJob
	index := map[string]int{}
	for n, line := range ls[1:] {
		f := strings.SplitN(line, Delim, len(head))
		if len(f) != len(head) {
			return nil, fmt.Errorf("sacct row %d: %d fields, want %d", n+1, len(f), len(head))
		}
		get := func(name string) string {
			if i, ok := col[strings.ToLower(name)]; ok {
				return f[i]
			}
			return ""
		}
		raw := get("JobIDRaw")
		state, by, _ := strings.Cut(get("State"), " ") // "CANCELLED by 50001"
		if parent, _, isStep := strings.Cut(raw, "."); isStep {
			i, ok := index[parent]
			if !ok {
				continue
			}
			var st Step
			st.Step.ID, st.Step.Name = raw, get("JobName")
			st.State = []string{state}
			st.ExitCode = exitCodeOf(get("ExitCode"))
			st.Time.Elapsed = atoi(get("ElapsedRaw"))
			st.Time.Total = cpuTimeOf(get("TotalCPU"))
			st.Nodes.Count = atoi(get("NNodes"))
			if rss := memBytes(get("MaxRSS")); rss > 0 {
				st.TRES.Requested.Max = TRESList{{Type: "mem", Count: rss}}
			}
			jobs[i].Steps = append(jobs[i].Steps, st)
			continue
		}
		jid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sacct row %d: job id %q", n+1, raw)
		}
		var j AcctJob
		j.JobID, j.User, j.Account, j.Partition, j.Name = jid, get("User"), get("Account"), get("Partition"), get("JobName")
		j.State.Current, j.State.Reason = []string{state}, get("Reason")
		if by = strings.TrimSpace(strings.TrimPrefix(by, "by")); by != "" {
			// sacct prints the canceller's uid; --json its name. The owner
			// cancelling their own job is the common case and reads the same.
			if by == get("UID") {
				j.KillRequestUser = j.User
			} else {
				j.KillRequestUser = "uid " + by
			}
		}
		j.ExitCode = exitCodeOf(get("ExitCode"))
		j.Nodes, j.AllocationNodes = get("NodeList"), atoi(get("NNodes"))
		if j.Nodes == "None assigned" {
			j.AllocationNodes = 0 // never started: --json says 0 nodes
		}
		j.Time.Submission, j.Time.Start, j.Time.End = atoi(get("Submit")), atoi(get("Start")), atoi(get("End"))
		j.Time.Elapsed = atoi(get("ElapsedRaw"))
		j.Time.Total = cpuTimeOf(get("TotalCPU"))
		j.Time.Limit = limitOf(get("TimelimitRaw"))
		j.TRES.Allocated, j.TRES.Requested = tresOf(get("AllocTRES")), tresOf(get("ReqTRES"))
		j.RestartCnt = atoi(get("Restarts"))
		j.WorkDir, j.SubmitLine = get("WorkDir"), get("SubmitLine")
		p := PatternJob{JobID: jid, User: j.User, Name: j.Name, Nodes: j.Nodes, WorkDir: j.WorkDir}
		p.ArrayJobID, p.ArrayTaskID = arrayOf(get("JobID"), jid)
		j.StdoutExpanded = ExpandPattern(get("StdOut"), p)
		j.StderrExpanded = ExpandPattern(get("StdErr"), p)
		index[raw] = len(jobs)
		jobs = append(jobs, j)
	}
	if jobs == nil {
		jobs = []AcctJob{}
	}
	return jobs, nil
}

// arrayOf reads sacct's JobID column: "1527_0" is task 0 of array 1527.
func arrayOf(id string, raw int64) (int64, Num) {
	master, task, ok := strings.Cut(id, "_")
	if !ok {
		return raw, Num{}
	}
	t, err := strconv.ParseInt(task, 10, 64)
	if err != nil {
		return atoi(master), Num{} // a pending range "6_[2-3]"
	}
	return atoi(master), Num{Set: true, Number: float64(t)}
}

// ParseBatchScript reads `sacct -n -B -j <id>`: a "Batch Script for <id>" line,
// a rule, then the script, or NONE when the cluster does not store scripts.
func ParseBatchScript(b []byte) string {
	s := string(b)
	if !strings.HasPrefix(s, "Batch Script for ") {
		return ""
	}
	_, s, _ = strings.Cut(s, "\n")
	if strings.HasPrefix(s, "---") {
		_, s, _ = strings.Cut(s, "\n")
	}
	if strings.TrimSpace(s) == "NONE" {
		return ""
	}
	return strings.TrimSuffix(s, "\n")
}

// ---- output path patterns ---------------------------------------------------------

// PatternJob is what a filename pattern can refer to.
type PatternJob struct {
	JobID       int64
	ArrayJobID  int64 // 0 = not an array job
	ArrayTaskID Num
	User        string
	Name        string
	Nodes       string // node list; %N is the first node
	WorkDir     string
}

var rePattern = regexp.MustCompile(`\\\\|\\%|%%|%(\d{0,2})([AabjJNnstux])`)

// ExpandPattern turns a job's --output/--error pattern into the path Slurm
// wrote (the stdout_expanded --json reports): %j job id, %A array master,
// %a task, %u user, %x job name, %N first node, %n/%t 0, %s "batch",
// %% a percent sign, zero padding (%4j), and a relative path under the job's
// working directory. "" and "(null)" stay "".
func ExpandPattern(p string, j PatternJob) string {
	if p == "" || p == "(null)" {
		return ""
	}
	if strings.Contains(p, `\\`) { // "\\" turns expansion off
		p = strings.ReplaceAll(p, `\\`, "")
	} else {
		master := j.ArrayJobID
		if master == 0 {
			master = j.JobID
		}
		task := "4294967294" // Slurm's NO_VAL when a non-array job uses %a
		if j.ArrayTaskID.Set {
			task = strconv.FormatInt(j.ArrayTaskID.Int(), 10)
		}
		p = rePattern.ReplaceAllStringFunc(p, func(m string) string {
			if m == `\%` || m == "%%" {
				return "%"
			}
			sm := rePattern.FindStringSubmatch(m)
			width, _ := strconv.Atoi(sm[1])
			num := func(v string) string {
				for len(v) < width {
					v = "0" + v
				}
				return v
			}
			switch sm[2] {
			case "j":
				return num(strconv.FormatInt(j.JobID, 10))
			case "A":
				return num(strconv.FormatInt(master, 10))
			case "a":
				return num(task)
			case "b":
				if j.ArrayTaskID.Set {
					return num(strconv.FormatInt(j.ArrayTaskID.Int()%10, 10))
				}
				return num("0")
			case "J":
				return num(strconv.FormatInt(j.JobID, 10)) + ".batch"
			case "n", "t":
				return num("0")
			case "s":
				return "batch"
			case "u":
				return j.User
			case "x":
				return j.Name
			case "N":
				first, _, _ := strings.Cut(j.Nodes, ",")
				if k := strings.IndexByte(first, '['); k >= 0 {
					rng := first[k+1:]
					rng = strings.TrimSuffix(rng, "]")
					lo, _, _ := strings.Cut(rng, "-")
					lo, _, _ = strings.Cut(lo, ",")
					first = first[:k] + lo
				}
				return first
			}
			return m
		})
	}
	if !strings.HasPrefix(p, "/") && j.WorkDir != "" {
		p = strings.TrimSuffix(j.WorkDir, "/") + "/" + p
	}
	return p
}

// ---- scontrol -o ----------------------------------------------------------------------

// Fields reads one `scontrol -o` record: space-separated Key=Value pairs, where a
// value may itself contain spaces (OS=Linux 4.18 #1 SMP ..., Reason=Not
// responding [slurm@1793000000]). A word that does not start a Key= belongs to
// the value before it.
func Fields(line string) map[string]string {
	out := map[string]string{}
	last := ""
	for _, w := range strings.Split(line, " ") {
		if w == "" {
			if last != "" {
				out[last] += " "
			}
			continue
		}
		if k, v, ok := strings.Cut(w, "="); ok && reKey.MatchString(k) {
			out[k], last = v, k
			continue
		}
		if last != "" {
			out[last] += " " + w
		}
	}
	for k, v := range out {
		out[k] = strings.TrimSpace(v)
	}
	return out
}

var reKey = regexp.MustCompile(`^[A-Z][A-Za-z0-9_:]*$`)

var reReasonBy = regexp.MustCompile(`^(.*?)\s*\[([^@\]]*)@([^\]]*)\]$`)

// ParseNodesText reads `scontrol -o show nodes` printed with SLURM_TIME_FORMAT=%s.
func ParseNodesText(b []byte) ([]Node, error) {
	var nodes []Node
	for i, line := range lines(b) {
		f := Fields(line)
		name := f["NodeName"]
		if name == "" {
			return nil, fmt.Errorf("scontrol node record %d has no NodeName", i+1)
		}
		n := Node{Name: name, State: nodeStates(f["State"]), CPUs: atoi(f["CPUTot"]), AllocCPUs: atoi(f["CPUAlloc"]),
			RealMemory: atoi(f["RealMemory"]), AllocMemory: atoi(f["AllocMem"]), Gres: null(f["Gres"]),
			LastBusy: numOf(f["LastBusyTime"]), BootTime: numOf(f["BootTime"]), InstanceType: null(f["InstanceType"])}
		if v, err := strconv.ParseFloat(f["CPULoad"], 64); err == nil {
			n.CPULoad = int64(v*100 + 0.5)
		}
		if p := null(f["Partitions"]); p != "" {
			n.Partitions = strings.Split(p, ",")
		}
		if r := null(f["Reason"]); r != "" {
			n.Reason = r
			if m := reReasonBy.FindStringSubmatch(r); m != nil {
				n.Reason, n.ReasonSetBy, n.ReasonAt = m[1], m[2], numOf(m[3])
			}
		}
		n.version = f["Version"]
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// NodesVersion is the Slurm release the nodes report ("" when none booted).
func NodesVersion(nodes []Node) string {
	for _, n := range nodes {
		if n.version != "" {
			return n.version
		}
	}
	return ""
}

// nodeStates splits "IDLE+CLOUD+POWERED_DOWN" into flags as --json lists them;
// a trailing "*" on the base state is NOT_RESPONDING.
func nodeStates(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "+") {
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "*") {
			out = append(out, strings.TrimSuffix(p, "*"), "NOT_RESPONDING")
			continue
		}
		out = append(out, strings.TrimRight(p, "~#%$@!^-"))
	}
	return out
}

// Partition is one partition from `scontrol -o show partition`: who may use it
// and its limits. AllowAccounts/AllowGroups "ALL" (or unset) mean everyone.
type Partition struct {
	Name          string
	AllowAccounts []string // nil = every account
	DenyAccounts  []string
	AllowGroups   []string // nil = every group
	AllowQos      []string // nil = every QOS
	Default       bool
	Hidden        bool
	State         string
	MaxTime       Num // minutes; Infinite for UNLIMITED
	OverSubscribe string
	TotalNodes    int64
}

// Restricted reports a partition only some accounts or groups may use.
func (p Partition) Restricted() bool {
	return p.AllowAccounts != nil || p.DenyAccounts != nil || p.AllowGroups != nil
}

// ParsePartitionsText reads `scontrol -o show partition`.
func ParsePartitionsText(b []byte) ([]Partition, error) {
	var out []Partition
	for i, line := range lines(b) {
		f := Fields(line)
		name := f["PartitionName"]
		if name == "" {
			return nil, fmt.Errorf("scontrol partition record %d has no PartitionName", i+1)
		}
		out = append(out, Partition{Name: name, AllowAccounts: list(f["AllowAccounts"]), DenyAccounts: list(f["DenyAccounts"]),
			AllowGroups: list(f["AllowGroups"]), AllowQos: list(f["AllowQos"]), Default: f["Default"] == "YES",
			Hidden: f["Hidden"] == "YES", State: f["State"], MaxTime: limitOf(f["MaxTime"]),
			OverSubscribe: f["OverSubscribe"], TotalNodes: atoi(f["TotalNodes"])})
	}
	return out, nil
}

// list reads "a,b" ("ALL", "" and "(null)" = nil: no restriction).
func list(s string) []string {
	s = null(s)
	if s == "" || s == "ALL" {
		return nil
	}
	return strings.Split(s, ",")
}

// ---- sacctmgr -----------------------------------------------------------------------

// Assoc is one of a person's Slurm associations.
type Assoc struct {
	Account   string
	Partition string // "" = every partition
}

// Accounts is what accounts a person holds.
type Accounts struct {
	Default string
	Assocs  []Assoc
}

// ParseAssoc reads `sacctmgr -n -P show user <u> withassoc
// format=DefaultAccount,Account,Partition`.
func ParseAssoc(b []byte) (Accounts, error) {
	var a Accounts
	for i, line := range lines(b) {
		f := strings.Split(line, "|")
		if len(f) < 3 {
			return a, fmt.Errorf("sacctmgr row %d: %d fields, want 3", i+1, len(f))
		}
		if a.Default == "" {
			a.Default = f[0]
		}
		if f[1] != "" {
			a.Assocs = append(a.Assocs, Assoc{Account: f[1], Partition: f[2]})
		}
	}
	return a, nil
}

// ---- shared helpers -------------------------------------------------------------------

func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// null maps Slurm's empty markers to "".
func null(s string) string {
	switch s = strings.TrimSpace(s); s {
	case "(null)", "N/A", "None", "none":
		return ""
	}
	return s
}

// numOf reads a plain integer; "N/A", "None", "Unknown" and "" are unset.
func numOf(s string) Num {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return Num{}
	}
	return Num{Set: true, Number: float64(v)}
}

// limitOf reads a time limit in minutes: a bare number (TimelimitRaw) or
// [D-]HH:MM:SS / MM:SS / MM (squeue, scontrol). UNLIMITED and the like are
// infinite, as --json reports them.
func limitOf(s string) Num {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || s == "NOT_SET" || s == "INVALID" {
		return Num{}
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return Num{Set: true, Number: float64(v)}
	}
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, s = atoi(d), rest
		if days == 0 && d != "0" {
			return Num{Infinite: true}
		}
	}
	parts := strings.Split(s, ":")
	var secs int64
	switch len(parts) {
	case 3:
		secs = atoi(parts[0])*3600 + atoi(parts[1])*60 + atoi(parts[2])
	case 2:
		if days > 0 { // D-HH:MM
			secs = atoi(parts[0])*3600 + atoi(parts[1])*60
		} else { // MM:SS
			secs = atoi(parts[0])*60 + atoi(parts[1])
		}
	case 1:
		if days > 0 {
			secs = atoi(parts[0]) * 3600
		} else {
			return Num{Infinite: true} // UNLIMITED, Partition_Limit
		}
	}
	return Num{Set: true, Number: float64(days*1440 + secs/60)}
}
