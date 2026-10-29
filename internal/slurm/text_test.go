package slurm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readText(t *testing.T, version, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "slurm-text", version, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Real recordings: Slurm 25.11.4 (Ursa Major, read-only, anonymized with
// scripts/make_text_fixtures.py) and Slurm 21.08.6 (a container, 2026-10-28).
func TestTextRecordingsParse(t *testing.T) {
	for _, v := range []string{"21.08", "25.11"} {
		q, err := ParseSqueueText(readText(t, v, "squeue.txt"))
		if err != nil || len(q) == 0 {
			t.Fatalf("%s squeue: %v (%d jobs)", v, err, len(q))
		}
		a, err := ParseSacctText(readText(t, v, "sacct.txt"))
		if err != nil || len(a) == 0 {
			t.Fatalf("%s sacct: %v (%d jobs)", v, err, len(a))
		}
		steps := 0
		for _, j := range a {
			steps += len(j.Steps)
			if j.User == "" || j.StateName() == "" || j.Time.Submission == 0 {
				t.Errorf("%s job %d incomplete: %+v", v, j.JobID, j)
			}
		}
		if steps == 0 {
			t.Errorf("%s: no step rows attached", v)
		}
		_, body, _ := strings.Cut(string(readText(t, v, "nodes.txt")), "\n") // after "slurm <release>"
		ns, err := ParseNodesText([]byte(body))
		if err != nil || len(ns) == 0 {
			t.Fatalf("%s nodes: %v", v, err)
		}
		for _, n := range ns {
			if n.Name == "" || len(n.State) == 0 || len(n.Partitions) == 0 || n.CPUs == 0 {
				t.Errorf("%s node incomplete: %+v", v, n)
			}
		}
		ps, err := ParsePartitionsText(readText(t, v, "partitions.txt"))
		if err != nil || len(ps) == 0 {
			t.Fatalf("%s partitions: %v", v, err)
		}
		acc, err := ParseAssoc(readText(t, v, "assoc.txt"))
		if err != nil || acc.Default == "" || len(acc.Assocs) == 0 {
			t.Fatalf("%s assoc: %v %+v", v, err, acc)
		}
	}
}

func TestSqueueText2108(t *testing.T) {
	q, _ := ParseSqueueText(readText(t, "21.08", "squeue.txt"))
	by := map[int64]QueueJob{}
	for _, j := range q {
		by[j.JobID] = j
	}
	// array task 1 of job 13 runs as job 15
	j := by[15]
	if j.ArrayJobID.Int() != 13 || !j.ArrayTaskID.Set || j.ArrayTaskID.Int() != 0 || j.Account != "general" {
		t.Errorf("array task: %+v", j)
	}
	if by[12].TimeLimit.Int() != 10 || by[14].TimeLimit.Int() != 1 || by[14].State() != "PENDING" || by[14].StateReason != "Resources" {
		t.Errorf("limits/state: %+v %+v", by[12], by[14])
	}
	if by[14].Nodes != "" || by[12].Nodes != "c3" || by[12].StdoutExpanded != "/home/alice/slurm-12.out" {
		t.Errorf("nodes/paths: %+v", by[12])
	}
	if by[12].MemoryPerNode.Int() != 500 || by[12].CPUs.Int() != 1 || by[12].StartTime.Int() != 1793244203 {
		t.Errorf("mem/cpus/start: %+v", by[12])
	}
	if by[12].ArrayJobID.Int() != 0 {
		t.Errorf("not an array job: array_job_id %d", by[12].ArrayJobID.Int())
	}
}

func TestSacctText2108(t *testing.T) {
	a, err := ParseSacctText(readText(t, "21.08", "sacct.txt"))
	if err != nil {
		t.Fatal(err)
	}
	by := map[int64]AcctJob{}
	for _, j := range a {
		by[j.JobID] = j
	}
	// a "|" in the name survives (the delimiter is \x1f)
	if by[1].Name != "we|ird name" || !strings.Contains(by[1].SubmitLine, "-J we|ird name") {
		t.Errorf("pipe in name: %q / %q", by[1].Name, by[1].SubmitLine)
	}
	// "CANCELLED by <uid>" with the owner's uid reads as the owner
	if by[2].StateName() != "CANCELLED" || by[2].KillRequestUser != "alice" {
		t.Errorf("cancel by: %q %q", by[2].StateName(), by[2].KillRequestUser)
	}
	if by[1].Time.Limit.Int() != 7200 || by[1].TRES.Requested.Get("mem") != 500 || by[1].Account != "general" {
		t.Errorf("limit/req/account: %+v", by[1])
	}
	// Slurm 21.08 has no StdOut field: the path stays unknown, never guessed
	if by[1].StdoutExpanded != "" || by[1].RestartCnt != 0 {
		t.Errorf("legacy stdout: %q", by[1].StdoutExpanded)
	}
	if by[1].MaxRSSBytes() != 5611520 || by[1].Steps[0].Step.ID != "1.batch" {
		t.Errorf("step peak: %d", by[1].MaxRSSBytes())
	}
}

func TestSacctText2511(t *testing.T) {
	a, _ := ParseSacctText(readText(t, "25.11", "sacct.txt"))
	var arr *AcctJob
	for i := range a {
		if strings.Contains(a[i].StdoutExpanded, "warming-sweep_") {
			arr = &a[i]
			break
		}
	}
	if arr == nil {
		t.Fatal("no array job with a %A_%a pattern in the recording")
	}
	if strings.Contains(arr.StdoutExpanded, "%") || !strings.HasPrefix(arr.StdoutExpanded, "/home/alice_ucr_edu/") {
		t.Errorf("pattern not expanded: %q", arr.StdoutExpanded)
	}
}

func TestPartitionsText(t *testing.T) {
	ps, _ := ParsePartitionsText(readText(t, "25.11", "partitions.txt"))
	by := map[string]Partition{}
	for _, p := range ps {
		by[p.Name] = p
	}
	if p := by["amd112"]; strings.Join(p.AllowAccounts, ",") != "staff,lab-a" || p.AllowGroups != nil || p.MaxTime.Int() != 30*1440 || !p.Restricted() {
		t.Errorf("amd112: %+v", p)
	}
	if p := by["standard"]; p.AllowAccounts != nil || p.Restricted() || p.MaxTime.Int() != 30*1440 || !p.Default {
		t.Errorf("standard: %+v", p)
	}
	if p := by["check"]; p.MaxTime.Int() != 15 {
		t.Errorf("check MaxTime: %+v", p.MaxTime)
	}
	ps, _ = ParsePartitionsText(readText(t, "21.08", "partitions.txt"))
	if len(ps) != 2 || ps[1].Default != true || ps[0].MaxTime.Int() != 5*1440 {
		t.Errorf("21.08 partitions: %+v", ps)
	}
}

func TestNodesTextStatesAndReason(t *testing.T) {
	line := "NodeName=n1 CPUAlloc=2 CPUTot=8 CPULoad=1.50 Gres=gpu:1 RealMemory=1000 AllocMem=10 State=DOWN*+DRAIN Partitions=a,b BootTime=None LastBusyTime=1790000000 Reason=Not responding now [slurm@1790000100]\n" +
		"NodeName=n2 CPUTot=4 State=IDLE+CLOUD+POWERED_DOWN Partitions=c BootTime=1790000000 LastBusyTime=Unknown Reason=(null)\n"
	ns, err := ParseNodesText([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	n := ns[0]
	if strings.Join(n.State, "+") != "DOWN+NOT_RESPONDING+DRAIN" || !n.Broken() || n.CPULoad != 150 {
		t.Errorf("state: %+v", n)
	}
	if n.Reason != "Not responding now" || n.ReasonSetBy != "slurm" || n.ReasonAt.Int() != 1790000100 || n.Gres != "gpu:1" {
		t.Errorf("reason: %q %q %d", n.Reason, n.ReasonSetBy, n.ReasonAt.Int())
	}
	if strings.Join(n.Partitions, ",") != "a,b" || n.LastBusy.Int() != 1790000000 || n.BootTime.Valid() {
		t.Errorf("parts/times: %+v", n)
	}
	if ns[1].PoweredUp() || ns[1].LastBusy.Valid() || ns[1].Reason != "" {
		t.Errorf("n2: %+v", ns[1])
	}
}

func TestExpandPattern(t *testing.T) {
	j := PatternJob{JobID: 1528, ArrayJobID: 1527, ArrayTaskID: Num{Set: true, Number: 0}, User: "alice", Name: "sweep",
		Nodes: "node-[3-5],x", WorkDir: "/home/alice/run"}
	for in, want := range map[string]string{
		"out_%A_%a.log":        "/home/alice/run/out_1527_0.log",
		"/s/%x-%j.%N.%u":       "/s/sweep-1528.node-3.alice",
		"/s/%5j%%":             "/s/01528%",
		"slurm-%j.out":         "/home/alice/run/slurm-1528.out",
		`/s/\\%j`:              "/s/%j",
		"":                     "",
		"(null)":               "",
		"/abs/%q-unknown-%j.x": "/abs/%q-unknown-1528.x",
	} {
		if got := ExpandPattern(in, j); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	// a plain job: %A is the job id
	if got := ExpandPattern("/s/%A.log", PatternJob{JobID: 7}); got != "/s/7.log" {
		t.Errorf("plain %%A: %q", got)
	}
}

func TestLimitOf(t *testing.T) {
	for in, want := range map[string]int64{"10:00": 10, "1:00": 1, "2:00:00": 120, "1-00:00:00": 1440,
		"30-00:00:00": 43200, "5": 5, "7200": 7200, "1-12": 2160, "1-02:30": 1590} {
		if got := limitOf(in); !got.Valid() || got.Int() != want {
			t.Errorf("%q -> %+v, want %d", in, got, want)
		}
	}
	for _, in := range []string{"UNLIMITED", "Partition_Limit"} {
		if got := limitOf(in); !got.Infinite {
			t.Errorf("%q not infinite: %+v", in, got)
		}
	}
	if got := limitOf("N/A"); got.Set || got.Infinite {
		t.Errorf("N/A: %+v", got)
	}
}

func TestBatchScript(t *testing.T) {
	if s := ParseBatchScript(readText(t, "21.08", "script.txt")); s != "" {
		t.Errorf("NONE should be empty: %q", s)
	}
	b := "Batch Script for 5\n" + strings.Repeat("-", 80) + "\n#!/bin/bash\necho hi\n"
	if s := ParseBatchScript([]byte(b)); s != "#!/bin/bash\necho hi" {
		t.Errorf("script: %q", s)
	}
}

func TestTextParsersRefuseBadRows(t *testing.T) {
	if _, err := ParseSqueueText([]byte("1\x1f2\x1f3\n")); err == nil {
		t.Error("short squeue row accepted")
	}
	if _, err := ParseSqueueText([]byte("x" + strings.Repeat("\x1f", len(SqueueFields)-1) + "\n")); err == nil {
		t.Error("bad job id accepted")
	}
	if _, err := ParseSacctText([]byte("JobIDRaw\x1fJobName\x1fState\n1\x1fa\x1fRUNNING\n")); err == nil {
		t.Error("JobName not last accepted")
	}
	if _, err := ParseSacctText([]byte("JobIDRaw\x1fState\x1fJobName\n1\x1fRUNNING\n")); err == nil {
		t.Error("short sacct row accepted")
	}
	if _, err := ParseNodesText([]byte("State=IDLE\n")); err == nil {
		t.Error("node without name accepted")
	}
	if _, err := ParseAssoc([]byte("only|two\n")); err == nil {
		t.Error("short assoc row accepted")
	}
}
