package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

// F2 parity: the text path gives the operations what --json gave them, field by
// field, for every recorded job and node. The fixture prints the text answers
// from the same recordings (checked against real Slurm 25.11 and 21.08 output).
func TestTextMatchesJSONQueue(t *testing.T) {
	for _, dir := range []string{"../../testdata", "../../testdata/sites/bigcluster"} {
		fx := &backend.Fixture{Dir: dir, User: "alice_ucr_edu"}
		ctx := context.Background()
		b, err := fx.Run(ctx, backend.SqueueAll())
		if err != nil {
			t.Fatal(err)
		}
		var want slurm.QueueResponse
		if err := slurm.Decode(b, &want); err != nil {
			t.Fatal(err)
		}
		b, err = fx.Run(ctx, backend.SqueueAllText())
		if err != nil {
			t.Fatal(err)
		}
		got, err := slurm.ParseSqueueText(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want.Jobs) || len(got) == 0 {
			t.Fatalf("%s: %d text jobs, %d json", dir, len(got), len(want.Jobs))
		}
		for i, w := range want.Jobs {
			g := got[i]
			type view struct {
				ID                                             int64
				Array, Task, Nodes, CPUs, Limit, Sub, Start    int64
				End, Restarts, Mem                             int64
				Name, User, Part, State, Reason, NodeList, Out string
				WorkDir                                        string
				TaskSet                                        bool
			}
			v := func(j slurm.QueueJob) view {
				mem := j.MemoryPerNode.Int() * j.NodeCount.Int()
				if mem <= 0 {
					mem = j.MemoryPerCPU.Int() * j.CPUs.Int()
				}
				return view{j.JobID, j.ArrayJobID.Int(), j.ArrayTaskID.Int(), j.NodeCount.Int(), j.CPUs.Int(), j.TimeLimit.Int(),
					j.SubmitTime.Int(), j.StartTime.Int(), j.EndTime.Int(), j.RestartCnt.Int(), mem,
					j.Name, j.UserName, j.Partition, j.State(), j.StateReason, j.Nodes, j.StdoutExpanded, j.WorkDir, j.ArrayTaskID.Set}
			}
			gv, wv := v(g), v(w)
			// --json gives the memory asked for; tres-alloc what was granted
			// (they agree once a job runs, which is when cost is computed)
			if w.State() != "RUNNING" {
				gv.Mem, wv.Mem = 0, 0
			}
			if !reflect.DeepEqual(gv, wv) {
				gj, _ := json.Marshal(gv)
				wj, _ := json.Marshal(wv)
				t.Errorf("%s job %d\n got  %s\n want %s", dir, w.JobID, gj, wj)
			}
		}
	}
}

func TestTextMatchesJSONAcct(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		fx := &backend.Fixture{Dir: "../../testdata", User: "alice_ucr_edu", LegacySacct: legacy}
		ctx := context.Background()
		ids := []string{"37", "41", "45", "93", "195", "203", "212", "229", "236", "253", "260", "261"}
		jc, _ := backend.SacctJobs(ids)
		b, err := fx.Run(ctx, jc)
		if err != nil {
			t.Fatal(err)
		}
		var want slurm.AcctResponse
		if err := slurm.Decode(b, &want); err != nil {
			t.Fatal(err)
		}
		tc, _ := backend.SacctJobsText(ids)
		b, err = fx.Run(ctx, tc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := slurm.ParseSacctText(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want.Jobs) {
			t.Fatalf("%d text jobs, %d json", len(got), len(want.Jobs))
		}
		for i, w := range want.Jobs {
			g := got[i]
			type step struct {
				ID, Name, State, Exit string
				Elapsed, Peak         int64
			}
			type view struct {
				ID, Nodes, Elapsed, Start, End, Submit, Restarts, Limit int64
				Name, User, Account, Part, State, Reason, Exit          string
				NodeList, WorkDir, SubmitLine, Out, Err, Killed         string
				Alloc, Req                                              map[string]int64
				Peak                                                    int64
				Steps                                                   []step
			}
			v := func(j slurm.AcctJob) view {
				x := view{j.JobID, j.AllocationNodes, j.Time.Elapsed, j.Time.Start, j.Time.End, j.Time.Submission, j.RestartCnt, j.Time.Limit.Int(),
					j.Name, j.User, j.Account, j.Partition, j.StateName(), j.State.Reason, j.ExitCode.String(),
					j.Nodes, j.WorkDir, j.SubmitLine, j.StdoutExpanded, j.StderrExpanded, j.KillRequestUser,
					tresMap(j.TRES.Allocated), tresMap(j.TRES.Requested), j.MaxRSSBytes(), nil}
				for _, s := range j.Steps {
					x.Steps = append(x.Steps, step{s.Step.ID, s.Step.Name, strings.Join(s.State, ","), s.ExitCode.String(), s.Time.Elapsed, s.MaxRSSBytes()})
				}
				return x
			}
			gv, wv := v(g), v(w)
			if legacy { // Slurm before 23.02 prints no Restarts, StdOut, StdErr
				wv.Restarts, wv.Out, wv.Err = 0, "", ""
			}
			if !reflect.DeepEqual(gv, wv) {
				gj, _ := json.Marshal(gv)
				wj, _ := json.Marshal(wv)
				t.Errorf("legacy=%v job %d\n got  %s\n want %s", legacy, w.JobID, gj, wj)
			}
		}
	}
}

func TestTextMatchesJSONNodes(t *testing.T) {
	for _, dir := range []string{"../../testdata", "../../testdata/sites/bigcluster"} {
		s, _ := newTestService(t)
		s.Backend = &backend.Fixture{Dir: dir, User: "alice_ucr_edu"}
		ctx := context.Background()
		got, rel, err := s.nodes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.Site.Slurm.JSON = true
		s.cache = map[string]cacheEntry{}
		want, wrel, err := s.nodes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rel != wrel || rel == "" || len(got) != len(want) {
			t.Fatalf("%s: release %q/%q, %d/%d nodes", dir, rel, wrel, len(got), len(want))
		}
		for i := range want {
			w, g := want[i], got[i]
			w.Gres = strings.TrimPrefix(w.Gres, "(null)")
			// a time of 0 is "none" on both paths (--json: set 0, text: unset);
			// every reader checks Int() > 0
			for _, n := range []*slurm.Node{&w, &g} {
				for _, x := range []*slurm.Num{&n.LastBusy, &n.BootTime, &n.ReasonAt} {
					if x.Int() == 0 {
						*x = slurm.Num{}
					}
				}
			}
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(w)
			if string(gj) != string(wj) {
				t.Errorf("%s node %s\n got  %s\n want %s", dir, w.Name, gj, wj)
			}
		}
	}
}

// ---- lab-locked partitions ------------------------------------------------------

const labPartitions = "PartitionName=computehigh AllowGroups=ALL AllowAccounts=ALL AllowQos=ALL Default=NO MaxTime=UNLIMITED State=UP\n" +
	"PartitionName=highmem AllowGroups=ALL AllowAccounts=staff,lab-a AllowQos=ALL Default=NO MaxTime=UNLIMITED State=UP\n" +
	"PartitionName=gpul4 AllowGroups=gpuusers AllowAccounts=ALL AllowQos=ALL Default=NO MaxTime=UNLIMITED State=UP\n" +
	"PartitionName=nvmescratch AllowGroups=ALL DenyAccounts=guests AllowQos=ALL Default=NO MaxTime=UNLIMITED State=UP\n"

func labService(t *testing.T, accounts, groups string) (*Service, *backend.Fixture) {
	s, fx := newTestService(t)
	fx.Partitions, fx.Accounts, fx.Groups = labPartitions, accounts, groups
	return s, fx
}

func partNames(ps []PartitionInfo) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.Name)
	}
	return strings.Join(n, ",")
}

func TestPartitionsHidesLabLocked(t *testing.T) {
	ctx := context.Background()
	// an ordinary user: default account "general", no gpu group
	s, _ := labService(t, "general|general|\n", "alice_ucr_edu")
	ps, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := partNames(ps); strings.Contains(got, "highmem") || strings.Contains(got, "gpul4") || !strings.Contains(got, "computehigh") || !strings.Contains(got, "nvmescratch") {
		t.Errorf("ordinary user sees %s", got)
	}
	// a lab member (a second account lab-a) and in the gpu group sees both, marked
	s, _ = labService(t, "general|general|\ngeneral|lab-a|\n", "alice_ucr_edu gpuusers")
	ps, _ = s.Partitions(ctx)
	got := partNames(ps)
	if !strings.Contains(got, "highmem") || !strings.Contains(got, "gpul4") {
		t.Errorf("lab member sees %s", got)
	}
	for _, p := range ps {
		if p.Name == "highmem" && p.RestrictedTo != "accounts staff,lab-a" {
			t.Errorf("restricted_to %q", p.RestrictedTo)
		}
	}
	// a denied account
	s, _ = labService(t, "guests|guests|\n", "alice_ucr_edu")
	ps, _ = s.Partitions(ctx)
	if strings.Contains(partNames(ps), "nvmescratch") {
		t.Errorf("denied account sees nvmescratch: %s", partNames(ps))
	}
	// an association limited to another partition does not open highmem
	s, _ = labService(t, "general|general|\ngeneral|lab-a|computehigh\n", "x")
	ps, _ = s.Partitions(ctx)
	if strings.Contains(partNames(ps), "highmem") {
		t.Errorf("partition-scoped association opened highmem: %s", partNames(ps))
	}
}

func TestPartitionsUnknownLimitsHideNothing(t *testing.T) {
	ctx := context.Background()
	s, fx := labService(t, "general|general|\n", "x")
	fx.Fail = map[string]string{"scontrol": "error: Unable to contact slurm controller"}
	ps, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 6 {
		t.Errorf("limits unreadable: %d partitions listed, want all 6", len(ps))
	}
	// sacctmgr unreadable: accounts unknown, nothing refused on a guess
	s, fx = labService(t, "", "x")
	fx.Fail = map[string]string{"sacctmgr": "sacctmgr: error: Problem talking to the database"}
	ps, _ = s.Partitions(ctx)
	if !strings.Contains(partNames(ps), "highmem") {
		t.Errorf("accounts unreadable but highmem hidden: %s", partNames(ps))
	}
}

func scriptOn(part, extra string) string {
	return "#!/bin/bash\n#SBATCH -p " + part + "\n#SBATCH -N 1\n#SBATCH -t 1:00:00\n" + extra + "echo hi\n"
}

func lockedIssue(sc *ScriptCheck) string {
	for _, is := range sc.Issues {
		if is.Severity == "error" && strings.Contains(is.Message, "Slurm would reject") {
			return is.Message
		}
	}
	return ""
}

func TestScriptCheckLabLocked(t *testing.T) {
	ctx := context.Background()
	s, _ := labService(t, "general|general|\n", "alice_ucr_edu")
	sc, err := s.ScriptCheck(ctx, scriptOn("highmem", ""))
	if err != nil {
		t.Fatal(err)
	}
	if m := lockedIssue(sc); !strings.Contains(m, "limited to accounts staff,lab-a") || !strings.Contains(m, "default account general") || sc.OK {
		t.Errorf("ordinary user on highmem: ok=%v %q", sc.OK, m)
	}
	if sc, _ := s.ScriptCheck(ctx, scriptOn("gpul4", "#SBATCH --gres=gpu:1\n")); !strings.Contains(lockedIssue(sc), "groups gpuusers") {
		t.Errorf("group lock: %+v", sc.Issues)
	}
	if sc, _ := s.ScriptCheck(ctx, scriptOn("computehigh", "")); lockedIssue(sc) != "" {
		t.Errorf("open partition refused: %+v", sc.Issues)
	}
	// a partition list: a locked member is reported
	if sc, _ := s.ScriptCheck(ctx, scriptOn("computehigh,highmem", "")); !strings.Contains(lockedIssue(sc), "highmem") {
		t.Errorf("list member: %+v", sc.Issues)
	}
	// a lab member whose default account is not allowed is told which to use
	s, _ = labService(t, "general|general|\ngeneral|lab-a|\n", "x")
	sc, _ = s.ScriptCheck(ctx, scriptOn("highmem", ""))
	if m := lockedIssue(sc); !strings.Contains(m, "--account=lab-a") {
		t.Errorf("fix not suggested: %q", m)
	}
	// ... and with that account the script passes
	if sc, _ := s.ScriptCheck(ctx, scriptOn("highmem", "#SBATCH --account=lab-a\n")); lockedIssue(sc) != "" {
		t.Errorf("allowed account refused: %+v", sc.Issues)
	}
	// an account the person does not hold is refused, whatever the partition says
	if sc, _ := s.ScriptCheck(ctx, scriptOn("highmem", "#SBATCH -A staff\n")); !strings.Contains(lockedIssue(sc), "no Slurm association with account staff") {
		t.Errorf("foreign account: %+v", sc.Issues)
	}
}

func TestJobSubmitRefusesLabLocked(t *testing.T) {
	ctx := context.Background()
	s, fx := labService(t, "general|general|\n", "x")
	_, err := s.PrepareSubmit(ctx, SubmitInput{Script: scriptOn("highmem", "#SBATCH -c 4\n")})
	if err == nil || !strings.Contains(err.Error(), "limited to accounts") {
		t.Fatalf("prepare on a locked partition: %v", err)
	}
	// an override onto a locked partition is refused too
	_, err = s.PrepareSubmit(ctx, SubmitInput{Script: scriptOn("computehigh", ""), Partition: "highmem"})
	if err == nil || !strings.Contains(err.Error(), "limited to accounts") {
		t.Fatalf("override onto a locked partition: %v", err)
	}
	for _, c := range fx.Calls {
		if strings.HasPrefix(c, "sbatch") || strings.Contains(c, "exec sbatch") {
			t.Fatalf("a locked plan reached the scheduler: %s", c)
		}
	}
}

func TestInteractiveHelpLabLocked(t *testing.T) {
	s, _ := labService(t, "general|general|\n", "x")
	if _, err := s.InteractiveHelp(context.Background(), InteractiveInput{Partition: "highmem"}); err == nil || !strings.Contains(err.Error(), "limited to accounts") {
		t.Errorf("interactive on a locked partition: %v", err)
	}
	s, _ = labService(t, "general|general|\ngeneral|lab-a|\n", "x")
	if _, err := s.InteractiveHelp(context.Background(), InteractiveInput{Partition: "highmem"}); err == nil || !strings.Contains(err.Error(), "--account=lab-a") {
		t.Errorf("interactive fix: %v", err)
	}
}

// No restricted partition: no per-person reads (sacctmgr, id -Gn) at all.
func TestOpenClusterSkipsAccountReads(t *testing.T) {
	s, fx := newTestService(t)
	if _, err := s.Partitions(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Calls {
		if strings.HasPrefix(c, "sacctmgr") || c == "id -Gn" {
			t.Errorf("open cluster ran %s", c)
		}
	}
}

// The stockout fallback never suggests a partition the caller may not use.
func TestFallbackSkipsLabLocked(t *testing.T) {
	a := &access{parts: map[string]slurm.Partition{}}
	ps, _ := slurm.ParsePartitionsText([]byte(labPartitions))
	for _, p := range ps {
		a.parts[p.Name] = p
	}
	acc, _ := slurm.ParseAssoc([]byte("general|general|\n"))
	a.accounts = &acc
	a.groups = map[string]bool{"x": true}
	if a.allowed("highmem") || a.allowed("gpul4") || !a.allowed("computehigh") || !a.allowed("nosuch") {
		t.Errorf("allowed: highmem %v gpul4 %v computehigh %v", a.allowed("highmem"), a.allowed("gpul4"), a.allowed("computehigh"))
	}
	var nilAccess *access
	if !nilAccess.allowed("highmem") {
		t.Error("unknown limits must allow")
	}
}

// A state change drops the cached text queue and accounting reads too.
func TestInvalidateDropsTextReads(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	if _, err := s.queue(ctx, queueSel{user: "alice_ucr_edu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.acctJobs(ctx, []string{"236"}, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.nodes(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.cache) < 3 {
		t.Fatalf("expected cached reads, got %d", len(s.cache))
	}
	s.invalidateQueue()
	for k := range s.cache {
		if strings.Contains(k, "squeue") || strings.Contains(k, "sacct") || strings.Contains(k, "show nodes") {
			t.Errorf("still cached after a state change: %.80s", k)
		}
	}
}

// The stockout advice never points at a partition the caller may not use.
func TestPartitionTroubleSkipsLabLocked(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"catalog.json", "squeue.json", "sacct_jobs.json", "sharing.txt"} {
		b, err := os.ReadFile(filepath.Join("../../testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	nodes := `{"meta":{"slurm":{"release":"25.11.4"}},"nodes":[
{"name":"s-0","state":["DOWN","CLOUD"],"partitions":["standard"],"reason":"RESOURCE_POOL_EXHAUSTED","cpus":16},
{"name":"h-0","state":["IDLE","CLOUD"],"partitions":["highmem"],"cpus":32},
{"name":"c-0","state":["IDLE","CLOUD"],"partitions":["computehigh"],"cpus":22}]}`
	_ = os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(nodes), 0o644)
	s, _ := newTestService(t)
	s.Backend = &backend.Fixture{Dir: dir, User: "alice_ucr_edu", Partitions: labPartitions, Accounts: "general|general|\n", Groups: "x"}
	s.Site.Partitions.FallbackIdle = []string{"highmem", "computehigh"}
	s.Site.Partitions.FallbackCold = []string{"highmem", "computehigh"}
	why := s.partitionTrouble(context.Background(), "standard")
	if !strings.Contains(why, "consider partition computehigh") || strings.Contains(why, "highmem") {
		t.Errorf("advice: %q", why)
	}
	// a lab member may be pointed at highmem
	s.Backend = &backend.Fixture{Dir: dir, User: "alice_ucr_edu", Partitions: labPartitions, Accounts: "staff|staff|\n", Groups: "x"}
	s.cache = map[string]cacheEntry{}
	if why := s.partitionTrouble(context.Background(), "standard"); !strings.Contains(why, "consider partition highmem") {
		t.Errorf("lab member advice: %q", why)
	}
}
