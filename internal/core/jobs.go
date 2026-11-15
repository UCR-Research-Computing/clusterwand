package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/policy"
	"github.com/UCR-Research-Computing/clusterwand/internal/rules"
	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

// ---- jobs list -----------------------------------------------------------------

// JobSummary is one row of a job list.
type JobSummary struct {
	JobID     string  `json:"job_id"`
	Name      string  `json:"name"`
	User      string  `json:"user"`
	Partition string  `json:"partition"`
	State     string  `json:"state"`
	Reason    string  `json:"reason,omitempty"`
	Nodes     string  `json:"nodes,omitempty"`
	NodeCount int64   `json:"node_count"`
	CPUs      int64   `json:"cpus"`
	Submitted string  `json:"submitted,omitempty"`
	Started   string  `json:"started,omitempty"`
	Ended     string  `json:"ended,omitempty"`
	ElapsedS  int64   `json:"elapsed_s"`
	ExitCode  string  `json:"exit_code,omitempty"`
	CostUSD   float64 `json:"est_cost_usd,omitempty"`
	// Restarts is Slurm's requeue count (node failures, preemption): a watcher
	// sees it in the list without a job_show per job.
	Restarts int64 `json:"restarts,omitempty"`
	// RawJobID is Slurm's underlying numeric job id when JobID is an array task ("1650_2").
	RawJobID string `json:"raw_job_id,omitempty"`
	// ArraySummary rolls up sibling array task states ("4 tasks: 3 COMPLETED, 1 FAILED").
	ArraySummary string `json:"array_summary,omitempty"`
}

// JobsListInput selects jobs.
// DefaultJobRows is how many jobs a list returns when the caller gives no limit.
const DefaultJobRows = 50

type JobsListInput struct {
	User  string // "" = caller
	All   bool   // every user (R2)
	State string // optional filter, e.g. FAILED or RUNNING
	Since string // default now-7days
	Limit int
	// JobIDs keeps only these jobs (at most MaxJobIDs). Only the caller's own
	// jobs are ever listed, so another user's id is simply not in the answer.
	JobIDs []string
}

// MaxJobIDs bounds jobs_list job_ids (one watcher round for every active run).
const MaxJobIDs = 100

// JobsList merges the live queue with recent accounting (newest first).
func (s *Service) JobsList(ctx context.Context, in JobsListInput) ([]JobSummary, error) {
	me, err := s.User(ctx)
	if err != nil {
		return nil, err
	}
	who := firstNonEmpty(in.User, me)
	if in.Since == "" {
		in.Since = "now-7days"
	}
	if len(in.JobIDs) > MaxJobIDs {
		return nil, fmt.Errorf("job_ids: at most %d ids per call, got %d", MaxJobIDs, len(in.JobIDs))
	}
	wantExact := map[string]bool{}
	wantParent := map[string]bool{}
	querySet := map[string]bool{}
	for _, id := range in.JobIDs {
		if err := backend.ValidJobID(id); err != nil {
			return nil, err
		}
		wantExact[id] = true
		base := strings.SplitN(id, "_", 2)[0]
		if !strings.Contains(id, "_") {
			wantParent[base] = true
		}
		querySet[base] = true
	}
	limit := in.Limit
	if limit <= 0 {
		// a default page, not the cap: 200 rows of a busy week was ~70 KB per call
		limit = min(DefaultJobRows, s.Cfg.Limits.ListRows)
		if len(querySet) > 0 {
			limit = min(max(DefaultJobRows, len(in.JobIDs)), s.Cfg.Limits.ListRows)
		}
	}
	if limit > s.Cfg.Limits.ListRows {
		limit = s.Cfg.Limits.ListRows
	}
	var qsel queueSel
	var ac backend.Command
	var ids []string
	byID := len(querySet) > 0 && !in.All
	if byID {
		// a program watching its own jobs (the Lab's watcher): ask Slurm for just
		// these jobs, uncached, so a job that ended shows as ended on the next call
		// instead of after the queue/accounting cache (up to 80 s) runs out. The rows
		// are filtered to the caller's own user below.
		ids = make([]string, 0, len(querySet))
		for id := range querySet {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		qsel = queueSel{jobs: ids}
	} else if in.All {
		qsel = queueSel{all: true}
		ac, err = backend.SacctSummaryAll(in.Since, "")
	} else {
		qsel = queueSel{user: who}
		ac, err = backend.SacctSummaryUser(who, in.Since, "")
	}
	if err != nil {
		return nil, err
	}
	var q slurm.QueueResponse
	if q.Jobs, err = s.queue(ctx, qsel); err != nil {
		return nil, err
	}
	var a slurm.AcctResponse
	if byID {
		if a.Jobs, err = s.acctJobs(ctx, ids, false); err != nil {
			return nil, err
		}
	} else if a.Jobs, err = s.acctRows(ctx, ac); err != nil {
		return nil, err
	}
	cat, _ := s.Catalog(ctx) // prices are optional
	seenDisp := map[string]bool{}
	seenRaw := map[string]bool{}
	var out []JobSummary
	for _, j := range q.Jobs {
		if byID && j.UserName != who {
			continue // only the caller's own jobs, as with squeue -u
		}
		js := s.queueSummary(j, cat)
		seenDisp[js.JobID] = true
		seenRaw[fmt.Sprint(j.JobID)] = true
		out = append(out, js)
	}
	for _, j := range a.Jobs {
		disp := j.DisplayID()
		raw := fmt.Sprint(j.JobID)
		if seenDisp[disp] || seenRaw[raw] {
			continue
		}
		if byID && j.User != who {
			continue
		}
		seenDisp[disp] = true
		seenRaw[raw] = true
		out = append(out, s.acctSummary(j, cat))
	}
	if len(wantExact) > 0 {
		var f []JobSummary
		for _, j := range out {
			parent, _, isArr := strings.Cut(j.JobID, "_")
			raw := firstNonEmpty(j.RawJobID, j.JobID)
			if wantExact[j.JobID] || wantExact[raw] || (isArr && wantParent[parent]) || (!isArr && querySet[j.JobID]) {
				f = append(f, j)
			}
		}
		out = f
	}
	attachArrayRollups(out)
	if in.State != "" {
		want := strings.ToUpper(in.State)
		var f []JobSummary
		for _, j := range out {
			if j.State == want {
				f = append(f, j)
			}
		}
		out = f
	}
	sort.SliceStable(out, func(i, j int) bool {
		ni, nj := jobNum(out[i].JobID), jobNum(out[j].JobID)
		if ni != nj {
			return ni > nj
		}
		return jobTaskNum(out[i].JobID) < jobTaskNum(out[j].JobID)
	})
	if len(out) > limit {
		out = out[:limit]
		markTruncated(ctx)
	}
	if out == nil {
		out = []JobSummary{}
	}
	return out, nil
}

func jobNum(id string) int64 {
	var n int64
	fmt.Sscanf(id, "%d", &n)
	return n
}

func jobTaskNum(id string) int64 {
	if _, task, ok := strings.Cut(id, "_"); ok {
		if n, err := strconv.ParseInt(task, 10, 64); err == nil {
			return n
		}
	}
	return -1
}

func attachArrayRollups(js []JobSummary) {
	byParent := map[string][]int{}
	for i, j := range js {
		if parent, _, ok := strings.Cut(j.JobID, "_"); ok {
			byParent[parent] = append(byParent[parent], i)
		}
	}
	for _, idxs := range byParent {
		if len(idxs) < 2 {
			continue
		}
		counts := map[string]int{}
		for _, idx := range idxs {
			counts[js[idx].State]++
		}
		summary := formatArraySummary(len(idxs), counts)
		for _, idx := range idxs {
			js[idx].ArraySummary = summary
		}
	}
}

func formatArraySummary(total int, counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return fmt.Sprintf("%d tasks: %s", total, strings.Join(parts, ", "))
}

func isFailureState(st string) bool {
	switch st {
	case "FAILED", "TIMEOUT", "OUT_OF_MEMORY", "NODE_FAIL", "BOOT_FAIL", "PREEMPTED":
		return true
	}
	return false
}

func isTerminalState(st string) bool {
	switch st {
	case "PENDING", "RUNNING", "COMPLETING", "CONFIGURING", "RESIZING", "SUSPENDED", "REQUEUED", "STAGE_OUT", "":
		return false
	}
	return true
}

func (s *Service) queueSummary(j slurm.QueueJob, cat *Catalog) JobSummary {
	disp := j.DisplayID()
	raw := fmt.Sprint(j.JobID)
	js := JobSummary{
		JobID: disp, Name: policy.CleanLabel(j.Name, 80), User: j.UserName,
		Partition: j.Partition, State: j.State(), Reason: j.StateReason, Nodes: j.Nodes,
		NodeCount: j.NodeCount.Int(), CPUs: j.CPUs.Int(),
		Submitted: numTS(j.SubmitTime), Started: numTS(j.StartTime),
		Restarts: j.RestartCnt.Int(),
	}
	if disp != raw {
		js.RawJobID = raw
	}
	if js.Reason == "None" {
		js.Reason = ""
	}
	if js.State == "RUNNING" && j.StartTime.Valid() {
		js.ElapsedS = s.Now().Unix() - j.StartTime.Int()
	}
	if p, ok := s.price(cat, j.Partition); ok && s.Cfg.ShowCost && s.chargedInUSD() && js.ElapsedS > 0 {
		mem := j.MemoryPerNode.Int() * js.NodeCount
		if mpc := j.MemoryPerCPU.Int(); mem <= 0 && mpc > 0 {
			mem = mpc * js.CPUs
		}
		share := allocShare(cat, j.Partition, js.NodeCount, js.CPUs, mem)
		js.CostUSD = round(p*float64(js.NodeCount)*share*float64(js.ElapsedS)/3600, 2)
	}
	return js
}

func (s *Service) acctSummary(j slurm.AcctJob, cat *Catalog) JobSummary {
	disp := j.DisplayID()
	raw := fmt.Sprint(j.JobID)
	js := JobSummary{
		JobID: disp, Name: policy.CleanLabel(j.Name, 80), User: j.User,
		Partition: j.Partition, State: j.StateName(), Nodes: j.Nodes,
		NodeCount: j.AllocationNodes, CPUs: j.TRES.Allocated.Get("cpu"),
		Submitted: ts(j.Time.Submission), Started: ts(j.Time.Start), Ended: ts(j.Time.End),
		ElapsedS: j.Time.Elapsed, ExitCode: j.ExitCode.String(), Restarts: j.RestartCnt,
	}
	if disp != raw {
		js.RawJobID = raw
	}
	// Slurm keeps the last pending reason (e.g. BeginTime) on finished jobs; it is
	// noise once a job has run, so only queued jobs show a reason.
	if r := j.State.Reason; r != "" && r != "None" && js.State == "PENDING" {
		js.Reason = r
	}
	if p, ok := s.price(cat, j.Partition); ok && s.Cfg.ShowCost && s.chargedInUSD() && js.ElapsedS > 0 {
		n := max(js.NodeCount, 1)
		share := allocShare(cat, j.Partition, n, js.CPUs, j.TRES.Allocated.Get("mem"))
		js.CostUSD = round(p*float64(n)*share*float64(js.ElapsedS)/3600, 2)
	}
	return js
}

// ---- job show ---------------------------------------------------------------------

// JobDetail merges squeue (if still queued/running) and sacct.
type JobDetail struct {
	JobSummary
	TimeLimitMin  int64              `json:"time_limit_min,omitempty"`
	Account       string             `json:"account,omitempty"`
	WorkDir       string             `json:"work_dir,omitempty"`
	Stdout        string             `json:"stdout,omitempty"`
	Stderr        string             `json:"stderr,omitempty"`
	SubmitLine    *policy.Untrusted  `json:"submit_line_untrusted,omitempty"`
	Requested     map[string]int64   `json:"requested_tres,omitempty"`
	Allocated     map[string]int64   `json:"allocated_tres,omitempty"`
	Efficiency    *Efficiency        `json:"efficiency,omitempty"`
	Steps         []StepSummary      `json:"steps,omitempty"`
	RestartCount  int64              `json:"restart_count,omitempty"`
	KilledBy      string             `json:"cancelled_by,omitempty"`
	PendingReason *PendingExplain    `json:"pending,omitempty"`
	Script        *policy.Untrusted  `json:"script_untrusted,omitempty"`
	ScriptNote    string             `json:"script_note,omitempty"`
	Raw           map[string]any     `json:"-"`
	acct          *slurm.AcctJob     `json:"-"`
	queue         *slurm.QueueJob    `json:"-"`
	extra         map[string]float64 `json:"-"`
}

// Efficiency compares what the job asked for with what it used.
type Efficiency struct {
	CPUPercent   float64 `json:"cpu_percent"`
	CPUSeconds   float64 `json:"cpu_seconds"`
	CoreSeconds  float64 `json:"core_seconds_allocated"`
	MemPeakMB    int64   `json:"mem_peak_mb"`
	MemAllocMB   int64   `json:"mem_alloc_mb"`
	MemPercent   float64 `json:"mem_percent"`
	Note         string  `json:"note,omitempty"`
	WholeNodeFee bool    `json:"whole_node_billing"`
}

// StepSummary is one job step.
type StepSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	ExitCode  string `json:"exit_code,omitempty"`
	ElapsedS  int64  `json:"elapsed_s"`
	MaxRSSMB  int64  `json:"max_rss_mb,omitempty"`
	CPUSecond int64  `json:"cpu_seconds,omitempty"`
}

// PendingExplain decodes why a job waits.
type PendingExplain struct {
	Reason  string `json:"reason"`
	Meaning string `json:"meaning"`
	Advice  string `json:"advice"`
}

// JobShowInput selects one job.
type JobShowInput struct {
	JobID         string
	AnyUser       bool // R2: allow other users' jobs
	IncludeScript bool // R2 only for other users
}

// JobShow returns the merged record of one job.
func (s *Service) JobShow(ctx context.Context, in JobShowInput) (*JobDetail, error) {
	if err := backend.ValidJobID(in.JobID); err != nil {
		return nil, err
	}
	me, err := s.User(ctx)
	if err != nil {
		return nil, err
	}
	var d JobDetail
	qs, _ := s.queue(ctx, queueSel{job: in.JobID})
	for i := range qs {
		if qs[i].DisplayID() == in.JobID || fmt.Sprint(qs[i].JobID) == in.JobID {
			j := qs[i]
			d.queue = &j
			break
		}
	}
	if d.queue == nil && !strings.Contains(in.JobID, "_") && len(qs) > 0 {
		for i := range qs {
			if fmt.Sprint(qs[i].ArrayJobID.Int()) == in.JobID {
				j := qs[i]
				d.queue = &j
				break
			}
		}
	}
	var a slurm.AcctResponse
	if a.Jobs, err = s.acctJobs(ctx, []string{in.JobID}, true); err != nil {
		return nil, err
	}
	// Exact task/job match first ("1650_2" or raw "1652"); when called with an
	// array parent id ("1650"), prefer any failed task so job_explain("1650")
	// surfaces the failure instead of a succeeded task 0.
	for i := range a.Jobs {
		if a.Jobs[i].DisplayID() == in.JobID || fmt.Sprint(a.Jobs[i].JobID) == in.JobID {
			d.acct = &a.Jobs[i]
			break
		}
	}
	if d.acct == nil && strings.Contains(in.JobID, "_") {
		base := strings.SplitN(in.JobID, "_", 2)[0]
		for i := range a.Jobs {
			if a.Jobs[i].Array.JobID == 0 && fmt.Sprint(a.Jobs[i].JobID) == base {
				d.acct = &a.Jobs[i]
				break
			}
		}
	}
	var arrayStates map[string]int
	arrayCount := 0
	if !strings.Contains(in.JobID, "_") {
		arrayStates = map[string]int{}
		seenTask := map[string]bool{}
		for _, qj := range qs {
			if fmt.Sprint(qj.ArrayJobID.Int()) == in.JobID {
				seenTask[qj.DisplayID()] = true
				arrayStates[qj.State()]++
				arrayCount++
			}
		}
		for i := range a.Jobs {
			if fmt.Sprint(a.Jobs[i].Array.JobID) == in.JobID {
				disp := a.Jobs[i].DisplayID()
				if !seenTask[disp] {
					seenTask[disp] = true
					arrayStates[a.Jobs[i].StateName()]++
					arrayCount++
				}
				if d.acct == nil || isFailureState(a.Jobs[i].StateName()) {
					if d.acct == nil || !isFailureState(d.acct.StateName()) {
						d.acct = &a.Jobs[i]
					}
				}
			}
		}
	}
	if d.acct != nil && d.queue != nil && d.acct.DisplayID() != d.queue.DisplayID() {
		// Parent array lookup found a failed finished task alongside a still-queued sibling:
		// keep the two consistent by matching queue to the chosen task.
		d.queue = nil
		for i := range qs {
			if qs[i].DisplayID() == d.acct.DisplayID() {
				j := qs[i]
				d.queue = &j
				break
			}
		}
	}
	if d.acct == nil && d.queue == nil {
		return nil, fmt.Errorf("job %s not found (accounting keeps finished jobs; check the id)", in.JobID)
	}
	owner := ""
	if d.acct != nil {
		owner = d.acct.User
	} else {
		owner = d.queue.UserName
	}
	if owner != me && !in.AnyUser {
		return nil, fmt.Errorf("%w: job %s belongs to another user; staff tier R2 can read it with the *_any tools", policy.ErrDenied, in.JobID)
	}
	cat, _ := s.Catalog(ctx)
	if d.acct != nil {
		d.JobSummary = s.acctSummary(*d.acct, cat)
		j := d.acct
		d.Account = j.Account
		d.WorkDir = j.WorkDir
		d.Stdout = j.StdoutExpanded
		d.Stderr = j.StderrExpanded
		d.TimeLimitMin = j.Time.Limit.Int()
		d.Requested = tresMap(j.TRES.Requested)
		d.Allocated = tresMap(j.TRES.Allocated)
		d.RestartCount = j.RestartCnt
		d.KilledBy = j.KillRequestUser
		if j.SubmitLine != "" {
			d.SubmitLine = policy.Wrap(j.SubmitLine, 2000)
			markUntrusted(ctx)
		}
		for _, st := range j.Steps {
			d.Steps = append(d.Steps, StepSummary{
				ID: st.Step.ID, Name: st.Step.Name, State: strings.Join(st.State, ","),
				ExitCode: st.ExitCode.String(), ElapsedS: st.Time.Elapsed,
				MaxRSSMB: st.MaxRSSBytes() / (1024 * 1024), CPUSecond: int64(st.Time.Total.Float()),
			})
		}
		if len(d.Steps) > 20 {
			d.Steps = d.Steps[:20]
			markTruncated(ctx)
		}
		d.Efficiency = efficiency(j)
		if in.IncludeScript {
			// the owner check above has passed: the text path reads the stored
			// script separately (--json carries it in the record)
			if j.Script == "" && !s.useJSON() {
				j.Script = s.batchScript(ctx, fmt.Sprint(j.JobID))
			}
			if j.Script != "" {
				d.Script = policy.Wrap(j.Script, s.Cfg.Limits.ScriptBytes)
				markUntrusted(ctx)
			} else {
				// SPEC 7.1: when AccountingStoreFlags has no job_script, an empty
				// script is the cluster's setting, not a lookup miss
				d.ScriptNote = s.Site.Text("script_not_stored", "Slurm did not store this job's batch script (the cluster's AccountingStoreFlags may not include job_script). Read the script from the job's working directory instead.")
			}
		}
	}
	if q := d.queue; q != nil { // live state wins
		live := s.queueSummary(*q, cat)
		d.State, d.Reason, d.Nodes, d.ElapsedS = live.State, live.Reason, live.Nodes, live.ElapsedS
		if live.CostUSD > 0 {
			d.CostUSD = live.CostUSD
		}
		if d.acct == nil {
			d.JobSummary = live
			d.Stdout, d.Stderr, d.WorkDir = q.StdoutExpanded, q.StderrExpanded, q.WorkDir
			d.TimeLimitMin = q.TimeLimit.Int()
		}
		if n := q.RestartCnt.Int(); n > d.RestartCount { // live count wins over accounting
			d.RestartCount = n
		}
		if d.State == "PENDING" {
			m, adv := rules.PendingReason(s.Site, q.StateReason)
			if q.StateReason == "Resources" {
				if extra := s.partitionTrouble(ctx, d.Partition); extra != "" {
					adv = extra + "; " + adv
				}
			}
			d.PendingReason = &PendingExplain{Reason: q.StateReason, Meaning: m, Advice: adv}
		}
	}
	if arrayCount >= 2 {
		d.ArraySummary = formatArraySummary(arrayCount, arrayStates)
	}
	return &d, nil
}

func tresMap(l slurm.TRESList) map[string]int64 {
	m := map[string]int64{}
	for _, t := range l {
		switch t.Type {
		case "cpu", "mem", "node", "gres":
			m[t.Key()] = t.Count
		}
	}
	return m
}

func efficiency(j *slurm.AcctJob) *Efficiency {
	cpus := j.TRES.Allocated.Get("cpu")
	if cpus == 0 || j.Time.Elapsed == 0 {
		return nil
	}
	e := &Efficiency{
		CPUSeconds:   round(j.Time.Total.Float(), 1),
		CoreSeconds:  float64(cpus * j.Time.Elapsed),
		MemPeakMB:    j.MaxRSSBytes() / (1024 * 1024),
		MemAllocMB:   j.TRES.Allocated.Get("mem"),
		WholeNodeFee: true,
	}
	e.CPUPercent = round(100*e.CPUSeconds/e.CoreSeconds, 1)
	if e.MemAllocMB > 0 {
		e.MemPercent = round(100*float64(e.MemPeakMB)/float64(e.MemAllocMB), 1)
	}
	if e.MemPeakMB == 0 {
		e.Note = "peak memory not recorded for this job"
	}
	return e
}

// ---- explain --------------------------------------------------------------------

// Explanation is job_explain's answer.
type Explanation struct {
	Job      JobSummary        `json:"job"`
	Findings []rules.Finding   `json:"findings"`
	LogPath  string            `json:"log_path,omitempty"`
	LogTail  *policy.Untrusted `json:"log_tail_untrusted,omitempty"`
	LogError string            `json:"log_error,omitempty"`
	Note     string            `json:"note"`
}

// JobExplain runs the diagnosis rules over a job's record and log tail.
func (s *Service) JobExplain(ctx context.Context, jobID string, anyUser bool, lines int) (*Explanation, error) {
	d, err := s.JobShow(ctx, JobShowInput{JobID: jobID, AnyUser: anyUser})
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = 80
	}
	if lines > 200 { // the rules only need the end of the log
		lines = 200
	}
	f := rules.Facts{State: d.State, Reason: d.Reason, ExitCode: d.ExitCode, Partition: d.Partition,
		ElapsedSec: d.ElapsedS, LimitMin: d.TimeLimitMin, CPUsAlloc: d.CPUs, RestartCnt: d.RestartCount,
		KilledBy: d.KilledBy, Site: s.Site}
	if e := d.Efficiency; e != nil {
		f.CPUSeconds, f.MemReqMB, f.MaxRSSMB = e.CPUSeconds, e.MemAllocMB, e.MemPeakMB
	}
	f.GPUsAlloc = d.Allocated["gres/gpu"]
	if cat, err := s.Catalog(ctx); err == nil {
		if p, ok := cat.Partition(d.Partition); ok {
			f.SpotPartition = p.Spot
			// When GPUs are not in AccountingStorageTRES (as on some sites) and the
			// partition is whole-node, a job there holds the node's GPUs.
			if f.GPUsAlloc == 0 && p.GPUsPerNode > 0 {
				f.GPUsAlloc = int64(p.GPUsPerNode) * max(d.NodeCount, 1)
			}
		}
	}
	ex := &Explanation{Job: d.JobSummary, Note: "Deterministic rules over accounting data and the log tail; no model involved. Verify before telling a user."}
	if d.State != "PENDING" {
		tail, p, err := s.readLog(ctx, d, "stdout", lines)
		ex.LogPath = p
		if err != nil {
			if isActiveNoLog(d.State, err) {
				ex.LogError = fmt.Sprintf("job is %s; log file not created yet", d.State)
			} else {
				ex.LogError = err.Error()
			}
		} else {
			f.Log, f.LogReadable = tail, true
			ex.LogTail = policy.Wrap(tail, s.Cfg.Limits.UntrustedCh)
			markUntrusted(ctx)
		}
	}
	ex.Findings = rules.Explain(f)
	if ex.Findings == nil {
		ex.Findings = []rules.Finding{}
	}
	return ex, nil
}

// ---- logs -------------------------------------------------------------------------

// LogTail is job_log_tail's answer: a window of the log, or a search.
type LogTail struct {
	JobID      string            `json:"job_id"`
	Stream     string            `json:"stream"`
	Path       string            `json:"path"`
	Lines      int               `json:"lines"`                // lines returned
	TotalLines int               `json:"total_lines"`          // lines in the log
	FirstLine  int               `json:"first_line,omitempty"` // 1-based number of the first line returned
	LastLine   int               `json:"last_line,omitempty"`  // number of the last line returned
	Tail       *policy.Untrusted `json:"untrusted,omitempty"`  // the window
	Grep       *GrepResult       `json:"grep,omitempty"`       // matches with line numbers
	Next       string            `json:"next,omitempty"`       // how to page
}

// LogInput selects a log window or search.
type LogInput struct {
	JobID     string
	Stream    string
	Lines     int    // window size (default 100, cap limits.log_lines)
	StartLine int    // 0 = the last Lines lines; otherwise from this line
	Grep      string // extended regex: matching lines anywhere in the log
	AnyUser   bool
}

// JobLogTail returns the redacted tail of a job's stdout or stderr.
func (s *Service) JobLogTail(ctx context.Context, jobID, stream string, lines int, anyUser bool) (*LogTail, error) {
	return s.JobLog(ctx, LogInput{JobID: jobID, Stream: stream, Lines: lines, AnyUser: anyUser})
}

// JobLog returns a window of a job's log (the tail, or from a line) or the
// lines matching a pattern, with line numbers for paging (SPEC 11.5).
func (s *Service) JobLog(ctx context.Context, in LogInput) (*LogTail, error) {
	stream := in.Stream
	if stream == "" {
		stream = "stdout"
	}
	if stream != "stdout" && stream != "stderr" {
		return nil, fmt.Errorf("stream must be stdout or stderr")
	}
	lines := in.Lines
	if lines <= 0 {
		lines = 100
	}
	if lines > s.Cfg.Limits.LogLines {
		lines = s.Cfg.Limits.LogLines
		markTruncated(ctx)
	}
	if in.StartLine < 0 {
		return nil, fmt.Errorf("start_line must be 1 or more")
	}
	if in.Grep != "" {
		if err := backend.ValidPattern(in.Grep); err != nil {
			return nil, err
		}
	}
	d, err := s.JobShow(ctx, JobShowInput{JobID: in.JobID, AnyUser: in.AnyUser})
	if err != nil {
		return nil, err
	}
	p, err := s.logPath(d, stream)
	if err != nil {
		return nil, err
	}
	lt := &LogTail{JobID: in.JobID, Stream: stream, Path: p}
	markUntrusted(ctx)
	if in.Grep != "" {
		g, err := s.grepFile(ctx, p, in.Grep)
		if err != nil {
			if isActiveNoLog(d.State, err) {
				lt.Grep = &GrepResult{Pattern: in.Grep, Lines: policy.Wrap("", 0)}
				lt.Next = fmt.Sprintf("Job %s is %s and has not written %s yet; check again shortly.", in.JobID, d.State, p)
				return lt, nil
			}
			return nil, fmt.Errorf("searching %s: %w", p, err)
		}
		lt.Grep, lt.TotalLines = g, g.TotalLines
		return lt, nil
	}
	c, err := backend.LogWindow(p, in.StartLine, lines)
	if err != nil {
		return nil, err
	}
	out, err := s.run(ctx, c)
	if err != nil {
		if isActiveNoLog(d.State, err) {
			lt.Tail = policy.Wrap("", 0)
			lt.Next = fmt.Sprintf("Job %s is %s and has not written %s yet; check again shortly.", in.JobID, d.State, p)
			return lt, nil
		}
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	head, body, _ := strings.Cut(string(out), "\n")
	var total, first int
	if _, err := fmt.Sscanf(head, "%d %d", &total, &first); err != nil {
		return nil, fmt.Errorf("unexpected answer reading %s", p)
	}
	if len(body) > pageChars { // keep whole lines from the end of the window
		cut := strings.IndexByte(body[len(body)-pageChars:], '\n')
		dropped := body[:len(body)-pageChars+cut+1]
		first += strings.Count(dropped, "\n")
		body = body[len(dropped):]
		markTruncated(ctx)
	}
	n := strings.Count(body, "\n")
	if body != "" && !strings.HasSuffix(body, "\n") {
		n++
	}
	lt.TotalLines, lt.Lines, lt.Tail = total, n, policy.Wrap(body, 0)
	if n > 0 {
		lt.FirstLine, lt.LastLine = first, first+n-1
	}
	switch {
	case lt.FirstLine > 1:
		lt.Next = fmt.Sprintf("Lines %d-%d of %d. Earlier: start_line=%d. Search the whole log with grep.", lt.FirstLine, lt.LastLine, total, max(1, lt.FirstLine-lines))
	case lt.LastLine < total:
		lt.Next = fmt.Sprintf("Lines %d-%d of %d. Later: start_line=%d.", lt.FirstLine, lt.LastLine, total, lt.LastLine+1)
	}
	return lt, nil
}

// logPath picks the job's log path from its record and checks the roots.
func (s *Service) logPath(d *JobDetail, stream string) (string, error) {
	p := d.Stdout
	if stream == "stderr" {
		p = firstNonEmpty(d.Stderr, d.Stdout)
	}
	if p == "" {
		return "", fmt.Errorf("the job record has no %s path (interactive job?)", stream)
	}
	if err := s.checkLogPath(p, d.User); err != nil {
		return p, err
	}
	return p, nil
}

// readLog resolves the log path from the job record (never from the caller) and
// checks it against the allowed roots for the job's owner.
func (s *Service) readLog(ctx context.Context, d *JobDetail, stream string, lines int) (string, string, error) {
	p, err := s.logPath(d, stream)
	if err != nil {
		return "", p, err
	}
	c, err := backend.Tail(p, lines)
	if err != nil {
		return "", p, err
	}
	b, err := s.run(ctx, c)
	if err != nil {
		return "", p, fmt.Errorf("reading %s: %w", p, err)
	}
	return string(b), p, nil
}

func (s *Service) checkLogPath(p, owner string) error {
	clean := path.Clean(p)
	if clean != p || !path.IsAbs(p) {
		return fmt.Errorf("%w: log path %q is not a clean absolute path", policy.ErrDenied, p)
	}
	for _, root := range s.Cfg.LogRoots {
		r := strings.ReplaceAll(root, "{user}", owner)
		if !strings.HasSuffix(r, "/") {
			r += "/"
		}
		if strings.HasPrefix(clean, r) {
			return nil
		}
	}
	return fmt.Errorf("%w: log %s is outside the allowed roots %v for user %s", policy.ErrDenied, p, s.Cfg.LogRoots, owner)
}

func localUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}

func approxSize(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

func isActiveNoLog(state string, err error) bool {
	if err == nil {
		return false
	}
	switch state {
	case "PENDING", "CONFIGURING", "RUNNING":
	default:
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not a regular file") || strings.Contains(msg, "No such file or directory")
}

// ---- job_wait -------------------------------------------------------------------

// Bounds for job_wait so MCP tool calls finish cleanly inside typical client
// timeouts (180 s in Hermes / Claude Desktop) rather than getting cut off.
const (
	DefaultWaitTimeoutS = 120
	MaxWaitTimeoutS     = 600
	DefaultWaitPollS    = 10
)

// JobFailureTail carries the redacted end of stdout/stderr for a job that ended
// in a failure state during job_wait, saving a follow-up round-trip.
type JobFailureTail struct {
	JobID      string            `json:"job_id"`
	State      string            `json:"state"`
	ExitCode   string            `json:"exit_code,omitempty"`
	StdoutPath string            `json:"stdout_path,omitempty"`
	StdoutTail *policy.Untrusted `json:"stdout_tail_untrusted,omitempty"`
	StderrPath string            `json:"stderr_path,omitempty"`
	StderrTail *policy.Untrusted `json:"stderr_tail_untrusted,omitempty"`
}

// JobWaitInput configures job_wait.
type JobWaitInput struct {
	JobIDs     []string
	TimeoutS   int
	PollS      int
	OnProgress func(done, total int, message string)
}

// JobWaitResult is job_wait's structured answer.
type JobWaitResult struct {
	Jobs         []JobSummary     `json:"jobs"`
	Complete     bool             `json:"complete"`
	TimedOut     bool             `json:"timed_out,omitempty"`
	Active       int              `json:"active"`
	Finished     int              `json:"finished"`
	WaitedS      int64            `json:"waited_s"`
	Polls        int              `json:"polls"`
	FailureTails []JobFailureTail `json:"failure_tails,omitempty"`
	Hint         string           `json:"hint,omitempty"`
}

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.C:
		return nil
	}
}

// JobWait polls jobs_list(job_ids) until every watched job reaches a terminal
// state or TimeoutS elapses, returning structured progress (never a hard timeout
// error when jobs are still queued or running).
func (s *Service) JobWait(ctx context.Context, in JobWaitInput) (*JobWaitResult, error) {
	if len(in.JobIDs) == 0 {
		return nil, fmt.Errorf("job_ids is required (1 to %d job ids)", MaxJobIDs)
	}
	if len(in.JobIDs) > MaxJobIDs {
		return nil, fmt.Errorf("job_ids: at most %d ids per call, got %d", MaxJobIDs, len(in.JobIDs))
	}
	for _, id := range in.JobIDs {
		if err := backend.ValidJobID(id); err != nil {
			return nil, err
		}
	}
	timeoutS := in.TimeoutS
	if timeoutS <= 0 {
		timeoutS = DefaultWaitTimeoutS
	}
	if timeoutS > MaxWaitTimeoutS {
		timeoutS = MaxWaitTimeoutS
	}
	pollS := in.PollS
	if pollS <= 0 {
		pollS = DefaultWaitPollS
	}
	if pollS < 2 {
		pollS = 2
	}
	if pollS > 60 {
		pollS = 60
	}

	start := s.Now()
	deadline := start.Add(time.Duration(timeoutS) * time.Second)
	// Leave a 10 s reserve before any caller context deadline so MCP clients
	// receive a clean timed_out=true payload instead of a transport timeout.
	if dl, ok := ctx.Deadline(); ok {
		if safe := dl.Add(-10 * time.Second); safe.After(start) && safe.Before(deadline) {
			deadline = safe
		}
	}

	polls := 0
	var jobs []JobSummary
	for {
		polls++
		var err error
		jobs, err = s.JobsList(ctx, JobsListInput{JobIDs: in.JobIDs, Limit: s.Cfg.Limits.ListRows})
		if err != nil {
			return nil, err
		}
		if len(jobs) == 0 {
			return nil, fmt.Errorf("none of %v were found among your jobs", in.JobIDs)
		}
		active, finished := 0, 0
		for _, j := range jobs {
			if isTerminalState(j.State) {
				finished++
			} else {
				active++
			}
		}
		if in.OnProgress != nil {
			in.OnProgress(finished, len(jobs), fmt.Sprintf("%d/%d finished (%d active)", finished, len(jobs), active))
		}
		now := s.Now()
		if active == 0 {
			res := &JobWaitResult{
				Jobs:     jobs,
				Complete: true,
				Active:   0,
				Finished: finished,
				WaitedS:  max(0, int64(now.Sub(start).Seconds())),
				Polls:    polls,
			}
			res.FailureTails = s.collectFailureTails(ctx, jobs)
			return res, nil
		}
		rem := deadline.Sub(now)
		if rem <= 0 {
			res := &JobWaitResult{
				Jobs:     jobs,
				Complete: false,
				TimedOut: true,
				Active:   active,
				Finished: finished,
				WaitedS:  max(0, int64(now.Sub(start).Seconds())),
				Polls:    polls,
				Hint:     fmt.Sprintf("%d of %d job(s) still active after %ds; call job_wait again to continue waiting.", active, len(jobs), max(1, int64(now.Sub(start).Seconds()))),
			}
			res.FailureTails = s.collectFailureTails(ctx, jobs)
			return res, nil
		}
		waitDur := min(time.Duration(pollS)*time.Second, rem)
		if err := s.sleep(ctx, waitDur); err != nil {
			return &JobWaitResult{
				Jobs:     jobs,
				Complete: false,
				TimedOut: true,
				Active:   active,
				Finished: finished,
				WaitedS:  max(0, int64(s.Now().Sub(start).Seconds())),
				Polls:    polls,
				Hint:     "Wait interrupted before all jobs finished; call job_wait again to continue.",
			}, nil
		}
	}
}

func (s *Service) collectFailureTails(ctx context.Context, jobs []JobSummary) []JobFailureTail {
	var out []JobFailureTail
	for _, j := range jobs {
		if !isFailureState(j.State) {
			continue
		}
		if len(out) >= 5 {
			break
		}
		d, err := s.JobShow(ctx, JobShowInput{JobID: j.JobID})
		if err != nil {
			continue
		}
		ft := JobFailureTail{JobID: j.JobID, State: j.State, ExitCode: j.ExitCode}
		if txt, p, err := s.readLog(ctx, d, "stdout", 30); err == nil {
			ft.StdoutPath = p
			ft.StdoutTail = policy.Wrap(txt, s.Cfg.Limits.UntrustedCh)
			markUntrusted(ctx)
		}
		if d.Stderr != "" && d.Stderr != d.Stdout {
			if txt, p, err := s.readLog(ctx, d, "stderr", 30); err == nil {
				ft.StderrPath = p
				ft.StderrTail = policy.Wrap(txt, s.Cfg.Limits.UntrustedCh)
				markUntrusted(ctx)
			}
		}
		if ft.StdoutTail != nil || ft.StderrTail != nil {
			out = append(out, ft)
		}
	}
	return out
}
