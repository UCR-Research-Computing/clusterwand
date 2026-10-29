//go:build slurmlive

package core

// Live parser check against a real Slurm (the containerised cluster in
// .github/workflows/slurm.yml, or any login node you are on). Every text read
// clusterwand makes is run as the exact remote line the SSH backend would send,
// through `bash -c`, and its output goes through the same parsers.
//
//	SLURM_LIVE_USER=alice go test -tags slurmlive ./internal/core -run Live -v
//
// Read-only: squeue, sacct, scontrol show, sacctmgr show, id.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

func liveRun(t *testing.T, c backend.Command) []byte {
	t.Helper()
	out, err := exec.Command("bash", "-c", backend.RemoteLine(c)).Output()
	if err != nil {
		t.Fatalf("%s: %v", c.String(), err)
	}
	return out
}

func TestLiveTextReads(t *testing.T) {
	user := os.Getenv("SLURM_LIVE_USER")
	if user == "" {
		t.Skip("SLURM_LIVE_USER not set")
	}
	q, err := slurm.ParseSqueueText(liveRun(t, backend.SqueueAllText()))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("squeue: %d jobs", len(q))
	uc, _ := backend.SqueueUserText(user)
	if _, err := slurm.ParseSqueueText(liveRun(t, uc)); err != nil {
		t.Fatal(err)
	}
	sc, _ := backend.SacctSummaryUser(user, "now-1days", "")
	rows, err := slurm.ParseSacctRows(liveRun(t, sc))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sacct summary: %d jobs", len(rows))
	if len(rows) > 0 {
		jc, _ := backend.SacctJobText(strconv.FormatInt(rows[0].JobID, 10))
		a, err := slurm.ParseSacctText(liveRun(t, jc))
		if err != nil || len(a) != 1 {
			t.Fatalf("sacct job: %v (%d)", err, len(a))
		}
		t.Logf("job %d: %s, %d steps, stdout %q", a[0].JobID, a[0].StateName(), len(a[0].Steps), a[0].StdoutExpanded)
	}
	b := liveRun(t, backend.NodesText())
	_, body, _ := strings.Cut(string(b), "\n")
	ns, err := slurm.ParseNodesText([]byte(body))
	if err != nil || len(ns) == 0 {
		t.Fatalf("nodes: %v", err)
	}
	ps, err := slurm.ParsePartitionsText(liveRun(t, backend.PartitionsText()))
	if err != nil || len(ps) == 0 {
		t.Fatalf("partitions: %v", err)
	}
	ac, _ := backend.Assoc(user)
	acc, err := slurm.ParseAssoc(liveRun(t, ac))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s; %d nodes, %d partitions, default account %q, %d associations", strings.SplitN(string(b), "\n", 2)[0], len(ns), len(ps), acc.Default, len(acc.Assocs))
	if want := os.Getenv("SLURM_LIVE_LOCKED"); want != "" {
		for _, p := range ps {
			if p.Name == want && !p.Restricted() {
				t.Errorf("partition %s should be lab-locked: %+v", want, p)
			}
		}
	}
}
