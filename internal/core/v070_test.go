package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/policy"
)

func TestJobWaitCompletesAndExtractsFailureTails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"catalog.json", "sinfo.json", "sharing.txt", "nodes.json", "partitions.json"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	squeueRunning := `{"meta":{"slurm":{"release":"25.11.4"}},"jobs":[
		{"job_id":501,"user_name":"alice_ucr_edu","partition":"standard","job_state":["RUNNING"],"node_count":{"set":true,"number":1},"cpus":{"set":true,"number":4},"start_time":{"set":true,"number":1790875300},"name":"sim-a"},
		{"job_id":502,"user_name":"alice_ucr_edu","partition":"standard","job_state":["PENDING"],"state_reason":"Resources","node_count":{"set":true,"number":1},"cpus":{"set":true,"number":4},"name":"sim-b"}
	]}`
	sacctDone := `{"meta":{"slurm":{"release":"25.11.4"}},"jobs":[
		{"job_id":501,"user":"alice_ucr_edu","partition":"standard","name":"sim-a","state":{"current":["COMPLETED"],"reason":"None"},"exit_code":{"return_code":{"set":true,"number":0}},"allocation_nodes":1,"nodes":"n0","time":{"elapsed":42,"start":1790875300,"end":1790875342},"tres":{"allocated":[{"type":"cpu","count":4}]},"working_directory":"/home/alice_ucr_edu/run501","stdout":"/home/alice_ucr_edu/run501/out.log","stderr":"/home/alice_ucr_edu/run501/out.log"},
		{"job_id":502,"user":"alice_ucr_edu","partition":"standard","name":"sim-b","state":{"current":["FAILED"],"reason":"NonZeroExitCode"},"exit_code":{"return_code":{"set":true,"number":1}},"allocation_nodes":1,"nodes":"n1","time":{"elapsed":12,"start":1790875350,"end":1790875362},"tres":{"allocated":[{"type":"cpu","count":4}]},"working_directory":"/home/alice_ucr_edu/run502","stdout":"/home/alice_ucr_edu/run502/out.log","stderr":"/home/alice_ucr_edu/run502/err.log"}
	]}`
	_ = os.WriteFile(filepath.Join(dir, "squeue.json"), []byte(squeueRunning), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sacct_jobs.json"), []byte(`{"jobs":[]}`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "logs", "out502.log"), []byte("step 1\nAPI_TOKEN=supersecretvalue999\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "logs", "err502.log"), []byte("ModuleNotFoundError: No module named 'jax'\n"), 0o644)

	s, _ := newTestService(t)
	fx := &backend.Fixture{
		Dir:  dir,
		User: "alice_ucr_edu",
		Logs: map[string]string{
			"/home/alice_ucr_edu/run502/out.log": "out502.log",
			"/home/alice_ucr_edu/run502/err.log": "err502.log",
		},
	}
	s.Backend = fx
	now := time.Unix(1790875310, 0)
	s.Now = func() time.Time { return now }
	s.Sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		// On the first sleep, transition both jobs from squeue to finished sacct!
		_ = os.WriteFile(filepath.Join(dir, "squeue.json"), []byte(`{"jobs":[]}`), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "sacct_jobs.json"), []byte(sacctDone), 0o644)
		return nil
	}

	progressCalls := 0
	env, err := Call(context.Background(), s, "test", "job_wait", "R1", nil, false, func(ctx context.Context) (*JobWaitResult, error) {
		return s.JobWait(ctx, JobWaitInput{
			JobIDs:   []string{"501", "502"},
			TimeoutS: 60,
			PollS:    5,
			OnProgress: func(done, total int, _ string) {
				progressCalls++
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	res := env.Data
	if !res.Complete || res.TimedOut || res.Polls != 2 || res.Finished != 2 || res.Active != 0 || progressCalls != 2 {
		t.Fatalf("unexpected wait result: %+v (progress=%d)", res, progressCalls)
	}
	if len(res.FailureTails) != 1 || res.FailureTails[0].JobID != "502" {
		t.Fatalf("missing failure tail for job 502: %+v", res.FailureTails)
	}
	ft := res.FailureTails[0]
	if ft.StdoutTail == nil || strings.Contains(ft.StdoutTail.Text, "supersecretvalue999") {
		t.Fatalf("stdout tail missing or unredacted: %+v", ft.StdoutTail)
	}
	if ft.StderrTail == nil || !strings.Contains(ft.StderrTail.Text, "No module named 'jax'") {
		t.Fatalf("stderr tail missing: %+v", ft.StderrTail)
	}
	if env.UntrustedNote == "" {
		t.Error("expected untrusted_note on job_wait envelope when failure_tails are present")
	}
}

func TestJobWaitBoundsTimeoutAndReservesClientDeadline(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"catalog.json", "sinfo.json", "sharing.txt", "nodes.json", "partitions.json"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	squeueRunning := `{"meta":{"slurm":{"release":"25.11.4"}},"jobs":[
		{"job_id":601,"user_name":"alice_ucr_edu","partition":"standard","job_state":["RUNNING"],"node_count":{"set":true,"number":1},"cpus":{"set":true,"number":4},"start_time":{"set":true,"number":1790875300},"name":"long"}
	]}`
	_ = os.WriteFile(filepath.Join(dir, "squeue.json"), []byte(squeueRunning), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sacct_jobs.json"), []byte(`{"jobs":[]}`), 0o644)

	s, _ := newTestService(t)
	s.Backend = &backend.Fixture{Dir: dir, User: "alice_ucr_edu"}
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.Sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}

	// Default timeout clamps to 120 s (below Hermes 180 s client timeout).
	res, err := s.JobWait(context.Background(), JobWaitInput{JobIDs: []string{"601"}, PollS: 30})
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete || !res.TimedOut || res.WaitedS != DefaultWaitTimeoutS || res.Active != 1 || res.Hint == "" {
		t.Fatalf("expected clean 120s timeout result, got: %+v", res)
	}

	// Tight caller context deadline (25 s): reserves 10 s and returns at 15 s instead of dying.
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(25*time.Second))
	defer cancel()
	res2, err := s.JobWait(ctx, JobWaitInput{JobIDs: []string{"601"}, TimeoutS: 300, PollS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.TimedOut || res2.WaitedS > 15 {
		t.Fatalf("expected 10s deadline reserve (<=15s waited), got: %+v", res2)
	}
}

func TestJobArraysTextAndJSONParity(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"catalog.json", "sinfo.json", "sharing.txt", "nodes.json", "partitions.json"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	squeueArr := `{"meta":{"slurm":{"release":"25.11.4"}},"jobs":[
		{"job_id":1652,"array_job_id":{"set":true,"number":1650},"array_task_id":{"set":true,"number":2},"user_name":"alice_ucr_edu","partition":"standard","job_state":["RUNNING"],"node_count":{"set":true,"number":1},"cpus":{"set":true,"number":4},"start_time":{"set":true,"number":1790875390},"name":"sweep"}
	]}`
	sacctArr := `{"meta":{"slurm":{"release":"25.11.4"}},"jobs":[
		{"job_id":1650,"array":{"job_id":1650,"task_id":{"set":true,"number":0}},"user":"alice_ucr_edu","partition":"standard","name":"sweep","state":{"current":["COMPLETED"],"reason":"None"},"exit_code":{"return_code":{"set":true,"number":0}},"allocation_nodes":1,"nodes":"n0","time":{"elapsed":20,"start":1790875300,"end":1790875320},"tres":{"allocated":[{"type":"cpu","count":4}]},"working_directory":"/home/alice_ucr_edu/sweep","stdout":"/home/alice_ucr_edu/sweep/out_%A_%a.log","stderr":"/home/alice_ucr_edu/sweep/out_%A_%a.log"},
		{"job_id":1651,"array":{"job_id":1650,"task_id":{"set":true,"number":1}},"user":"alice_ucr_edu","partition":"standard","name":"sweep","state":{"current":["FAILED"],"reason":"NonZeroExitCode"},"exit_code":{"return_code":{"set":true,"number":1}},"allocation_nodes":1,"nodes":"n1","time":{"elapsed":15,"start":1790875310,"end":1790875325},"tres":{"allocated":[{"type":"cpu","count":4}]},"working_directory":"/home/alice_ucr_edu/sweep","stdout":"/home/alice_ucr_edu/sweep/out_1650_1.log","stderr":"/home/alice_ucr_edu/sweep/out_1650_1.log","stdout_expanded":"/home/alice_ucr_edu/sweep/out_1650_1.log","stderr_expanded":"/home/alice_ucr_edu/sweep/out_1650_1.log"},
		{"job_id":1652,"array":{"job_id":1650,"task_id":{"set":true,"number":2}},"user":"alice_ucr_edu","partition":"standard","name":"sweep","state":{"current":["RUNNING"],"reason":"None"},"exit_code":{"return_code":{"set":true,"number":0}},"allocation_nodes":1,"nodes":"n2","time":{"elapsed":10,"start":1790875390,"end":0},"tres":{"allocated":[{"type":"cpu","count":4}]},"working_directory":"/home/alice_ucr_edu/sweep","stdout":"/home/alice_ucr_edu/sweep/out_1650_2.log","stderr":"/home/alice_ucr_edu/sweep/out_1650_2.log"}
	]}`
	_ = os.WriteFile(filepath.Join(dir, "squeue.json"), []byte(squeueArr), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sacct_jobs.json"), []byte(sacctArr), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "logs", "task1.log"), []byte("ZeroDivisionError: float division by zero\n"), 0o644)

	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := newTestService(t)
			s.Site.Slurm.JSON = jsonMode
			s.Backend = &backend.Fixture{
				Dir:  dir,
				User: "alice_ucr_edu",
				Logs: map[string]string{"/home/alice_ucr_edu/sweep/out_1650_1.log": "task1.log"},
			}
			ctx := context.Background()

			js, err := s.JobsList(ctx, JobsListInput{JobIDs: []string{"1650"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(js) != 3 {
				t.Fatalf("want 3 array tasks, got %d: %+v", len(js), js)
			}
			wantRollup := "3 tasks: 1 COMPLETED, 1 FAILED, 1 RUNNING"
			if js[0].JobID != "1650_0" || js[1].JobID != "1650_1" || js[2].JobID != "1650_2" || js[0].ArraySummary != wantRollup {
				t.Fatalf("unexpected array tasks/rollup: %+v", js)
			}

			// Single task selection returns ONLY that task.
			only2, err := s.JobsList(ctx, JobsListInput{JobIDs: []string{"1650_2"}})
			if err != nil || len(only2) != 1 || only2[0].JobID != "1650_2" || only2[0].RawJobID != "1652" {
				t.Fatalf("single task lookup: %+v (%v)", only2, err)
			}
			d2, err := s.JobShow(ctx, JobShowInput{JobID: "1650_2"})
			if err != nil || d2.JobID != "1650_2" || d2.State != "RUNNING" {
				t.Fatalf("JobShow(1650_2): %+v (%v)", d2, err)
			}

			// Explaining parent "1650" selects failed task 1650_1 and diagnoses ZeroDivisionError!
			ex, err := s.JobExplain(ctx, "1650", false, 80)
			if err != nil {
				t.Fatal(err)
			}
			if ex.Job.JobID != "1650_1" || ex.Job.ArraySummary != wantRollup || len(ex.Findings) == 0 || ex.Findings[0].Rule != "numerical" {
				t.Fatalf("JobExplain(1650) did not surface failed task 1650_1: %+v", ex)
			}
		})
	}
}

func makeTarGzBytes(t *testing.T, hdrs []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		body := bodies[h.Name]
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && len(body) > 0 {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestLocalSSHInputStagingAndArchiveUnpack(t *testing.T) {
	s, fx := a1Service(t)
	fx.Files = map[string]map[string]string{}
	ctx := context.Background()

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sim.yaml")
	_ = os.WriteFile(cfgPath, []byte("dt: 0.01\n"), 0o644)

	bundle := makeTarGzBytes(t, []tar.Header{
		{Name: "mesh/grid.dat", Mode: 0o644, Typeflag: tar.TypeReg},
	}, map[string]string{"mesh/grid.dat": "1 2 3\n"})
	bundlePath := filepath.Join(tmp, "bundle.tar.gz")
	_ = os.WriteFile(bundlePath, bundle, 0o644)

	script := "#!/bin/bash\n#SBATCH -p computehigh\n#SBATCH -N 1\n#SBATCH -t 10\nmodule load python-sci\npython3 inputs/run.py\n"
	callsBefore := len(fx.Calls)
	plan, err := s.PrepareSubmit(ctx, SubmitInput{
		Script: cfgScript(script),
		Inputs: []InputSpec{
			{LocalPath: cfgPath, Path: "config/sim.yaml"},
			{Path: "run.py", Content: "print('ok')\n"},
			{Path: "weights.bin", ContentBase64: base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4})},
			{LocalPath: bundlePath},
		},
		UnpackArchives: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// PrepareSubmit must NEVER run write commands on the cluster.
	for _, c := range fx.Calls[callsBefore:] {
		if strings.Contains(c, "mkdir -p") || strings.Contains(c, "tar -xzf") {
			t.Fatalf("PrepareSubmit wrote to cluster before confirm: %s", c)
		}
	}
	if len(plan.Inputs) != 4 || !plan.UnpackArchives || plan.InputsSHA256 == "" {
		t.Fatalf("unexpected staged plan: %+v", plan)
	}
	// PlanHash must bind staged inputs: the same script with different inputs produces a different PlanHash.
	planOther, err := s.PrepareSubmit(ctx, SubmitInput{
		Script: cfgScript(script),
		Inputs: []InputSpec{{Path: "run.py", Content: "print('different')\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlanHash == planOther.PlanHash {
		t.Fatalf("PlanHash did not bind staged inputs: %s == %s", plan.PlanHash, planOther.PlanHash)
	}

	// Mutating local file after PrepareSubmit must NOT alter the snapshot staged at ConfirmSubmit.
	_ = os.WriteFile(cfgPath, []byte("dt: 9999\n"), 0o644)

	conf, err := s.ConfirmSubmit(ctx, plan.Token)
	if err != nil {
		t.Fatal(err)
	}
	if conf.StagedInputs != 4 || !strings.Contains(conf.Next, "job_wait") {
		t.Fatalf("unexpected Confirmed: %+v", conf)
	}
	remoteFolder := "/home/alice_ucr_edu/" + strings.TrimPrefix(conf.RemoteDir, "~/")
	gotFiles := fx.Files[remoteFolder]
	if gotFiles["inputs/config/sim.yaml"] != "dt: 0.01\n" || gotFiles["inputs/mesh/grid.dat"] != "1 2 3\n" || gotFiles["inputs/run.py"] != "print('ok')\n" {
		t.Fatalf("staged files mismatch: %+v", gotFiles)
	}

	// Staged snapshot for the confirmed plan is cleaned up; unconfirmed planOther stays until used/expired.
	ents, _ := os.ReadDir(filepath.Join(filepath.Dir(s.statePath()), "staged"))
	if len(ents) != 1 {
		t.Fatalf("expected only planOther's snapshot to remain, got: %v", ents)
	}

	// Security denials: symlink, credential name, .. traversal, hostile archive entries.
	sym := filepath.Join(tmp, "link.txt")
	_ = os.Symlink(cfgPath, sym)
	secretLocal := filepath.Join(tmp, "api_secret.txt")
	_ = os.WriteFile(secretLocal, []byte("x"), 0o644)
	evilTar := makeTarGzBytes(t, []tar.Header{
		{Name: "../../.ssh/authorized_keys", Mode: 0o644, Typeflag: tar.TypeReg},
	}, map[string]string{"../../.ssh/authorized_keys": "ssh-ed25519 AAAA"})
	evilLinkTar := makeTarGzBytes(t, []tar.Header{
		{Name: "escape", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}, nil)

	for _, bad := range [][]InputSpec{
		{{LocalPath: sym}},
		{{LocalPath: secretLocal, Path: "clean.txt"}},
		{{Path: "../escape.txt", Content: "x"}},
		{{Path: ".env", Content: "SECRET=1"}},
		{{Path: "link.tar.gz", ContentBase64: base64.StdEncoding.EncodeToString(evilLinkTar)}},
	} {
		if _, err := s.PrepareSubmit(ctx, SubmitInput{Script: script, Inputs: bad, UnpackArchives: true}); err == nil || !errors.Is(err, policy.ErrDenied) {
			t.Errorf("accepted unsafe staged input %+v (err=%v)", bad, err)
		}
	}
	if _, err := s.PrepareSubmit(ctx, SubmitInput{
		Script:         script,
		Inputs:         []InputSpec{{Path: "sub/evil.tar.gz", ContentBase64: base64.StdEncoding.EncodeToString(evilTar)}},
		UnpackArchives: true,
	}); err == nil || !errors.Is(err, policy.ErrDenied) || !strings.Contains(err.Error(), "archive sub/evil.tar.gz contains unsafe path") {
		t.Fatalf("expected archive traversal guard to reject evil.tar.gz, got: %v", err)
	}
}

func cfgScript(s string) string { return s }

func TestFilesDownloadAndSecurityGuards(t *testing.T) {
	s, fx := newTestService(t)
	s.Cfg.ResultsDir = t.TempDir()
	homeFixture(fx)
	fx.Paths["/home/alice_ucr_edu/project/plot.png"] = "\x89PNG\r\n\x1a\nbinaryplot"
	fx.Paths["/apps/docs/README.md"] = "# Ursa Docs\n"
	ctx := context.Background()

	dl, err := s.FilesDownload(ctx, FilesDownloadInput{Path: "project/plot.png"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dl.Saved) != 1 || dl.Saved[0] != "plot.png" || dl.TotalBytes != int64(len(fx.Paths["/home/alice_ucr_edu/project/plot.png"])) {
		t.Fatalf("FilesDownload: %+v", dl)
	}
	onDisk, err := os.ReadFile(filepath.Join(dl.DownloadedTo, dl.Saved[0]))
	if err != nil || string(onDisk) != fx.Paths["/home/alice_ucr_edu/project/plot.png"] {
		t.Fatalf("downloaded bytes mismatch: %v", err)
	}
	// Downloading again never overwrites (creates plot.png.1).
	dl2, err := s.FilesDownload(ctx, FilesDownloadInput{Path: "./project/plot.png"})
	if err != nil || dl2.Saved[0] != "plot.png.1" {
		t.Fatalf("second download should write plot.png.1, got %+v (%v)", dl2, err)
	}

	// Denials: public roots (/apps/docs), secrets, symlinks out of home, folders.
	for _, bad := range []string{
		"/apps/docs/README.md",
		"~/.ssh/id_ed25519",
		"~/project/aws.pem",
		"~/project/escape",
		"~/project/sub",
		"../bob_ucr_edu/notes.txt",
	} {
		if _, err := s.FilesDownload(ctx, FilesDownloadInput{Path: bad}); err == nil {
			t.Errorf("FilesDownload accepted %q", bad)
		}
	}
}

func TestRelativePathsAndPublicRootsContainment(t *testing.T) {
	s, fx := newTestService(t)
	homeFixture(fx)
	fx.Paths["/apps/docs/TPU_PORTING.md"] = "TPU guide\n"
	fx.Paths["/apps/containers/pytorch.sif"] = "SIF"
	fx.Links["/apps/docs/evil_home"] = "/home/bob_ucr_edu/notes.txt"
	fx.Links["/apps/docs/evil_etc"] = "/etc/passwd"
	ctx := context.Background()

	// "." and "./project" resolve to caller's home.
	lDot, err := s.FilesList(ctx, FilesListInput{Path: "."})
	if err != nil || lDot.Path != "/home/alice_ucr_edu" {
		t.Fatalf("FilesList('.'): %+v (%v)", lDot, err)
	}
	lSub, err := s.FilesList(ctx, FilesListInput{Path: "./project"})
	if err != nil || lSub.Path != "/home/alice_ucr_edu/project" {
		t.Fatalf("FilesList('./project'): %+v (%v)", lSub, err)
	}

	// Site-configured public_roots (/apps/docs, /apps/containers) are readable...
	doc, err := s.FilesRead(ctx, FilesReadInput{Path: "/apps/docs/TPU_PORTING.md"})
	if err != nil || !strings.Contains(doc.Text.Text, "TPU guide") {
		t.Fatalf("FilesRead(/apps/docs/TPU_PORTING.md): %+v (%v)", doc, err)
	}
	cnt, err := s.FilesList(ctx, FilesListInput{Path: "/apps/containers"})
	if err != nil || cnt.Total != 1 {
		t.Fatalf("FilesList(/apps/containers): %+v (%v)", cnt, err)
	}
	// ...but symlinks escaping public_roots into /home or /etc are strictly denied!
	for _, bad := range []string{"/apps/docs/evil_home", "/apps/docs/evil_etc", "/apps/other/file.txt", "..", "./../bob_ucr_edu"} {
		if _, err := s.FilesRead(ctx, FilesReadInput{Path: bad}); !errors.Is(err, policy.ErrDenied) {
			t.Errorf("expected ErrDenied for %q, got %v", bad, err)
		}
	}
}

func TestScriptCheckV070Upgrades(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()

	// 1. --mem-per-cpu * CPUs exceeding node memory is an error.
	mpcScript := "#!/bin/bash\n#SBATCH -p standard\n#SBATCH -c 16\n#SBATCH --mem-per-cpu=16G\n#SBATCH -t 10\necho hi\n"
	sc, err := s.ScriptCheck(ctx, mpcScript)
	if err != nil || sc.OK || !hasIssue(sc.Issues, "error", "--mem-per-cpu") {
		t.Fatalf("expected --mem-per-cpu overflow error: %+v (%v)", sc, err)
	}

	// 2. Comma-separated partition list checks EVERY member's limits (gpul4 has GPUs, standard has 0 GPUs).
	listScript := "#!/bin/bash\n#SBATCH -p gpul4,standard\n#SBATCH --gres=gpu:1\n#SBATCH -c 4\n#SBATCH -t 30\necho hi\n"
	sc2, err := s.ScriptCheck(ctx, listScript)
	if err != nil || sc2.OK || !hasIssue(sc2.Issues, "error", "GPUs requested on standard") {
		t.Fatalf("expected per-member GPU error on gpul4,standard: %+v (%v)", sc2, err)
	}

	// 3. Partition override parameter on ScriptCheckWithPartition + python-tpu module scoping.
	scOverride, err := s.ScriptCheckWithPartition(ctx, "#!/bin/bash\n#SBATCH -p gpul4\n#SBATCH --gres=gpu:1\n#SBATCH -t 30\necho hi\n", "standard")
	if err != nil || scOverride.OK || !hasIssue(scOverride.Issues, "error", "GPUs requested on standard") {
		t.Fatalf("ScriptCheckWithPartition override failed: %+v (%v)", scOverride, err)
	}
	tpuScript := "#!/bin/bash\n#SBATCH -p standard\n#SBATCH -c 4\n#SBATCH -t 30\nmodule load python-tpu\npython3 train.py\n"
	scBadPart, err := s.ScriptCheck(ctx, tpuScript)
	if err != nil || scBadPart.OK || !hasIssue(scBadPart.Issues, "error", "only installed on partition(s)") {
		t.Fatalf("python-tpu on standard should error: %+v (%v)", scBadPart, err)
	}

	// 4. Bare apptainer without module load apptainer warns; cuda + apptainer informs.
	cntScript := "#!/bin/bash\n#SBATCH -p gpul4\n#SBATCH --gres=gpu:1\n#SBATCH -t 30\nmodule load cuda\napptainer exec --nv /apps/containers/pytorch.sif python3 x.py\n"
	scCnt, err := s.ScriptCheck(ctx, cntScript)
	if err != nil || !hasIssue(scCnt.Issues, "warning", "module load apptainer") || !hasIssue(scCnt.Issues, "info", "containers bring their own CUDA") {
		t.Fatalf("container checks missing: %+v (%v)", scCnt, err)
	}
}

func TestClusterStatusFilterAndActiveNoLogAndGPUFallback(t *testing.T) {
	s, fx := newTestService(t)
	ctx := context.Background()

	cs, err := s.ClusterStatusFiltered(ctx, "gpul4")
	if err != nil || len(cs.Partitions) != 1 || cs.Partitions[0].Name != "gpul4" {
		t.Fatalf("ClusterStatusFiltered(gpul4): %+v (%v)", cs, err)
	}
	if _, err := s.ClusterStatusFiltered(ctx, "nonexistent"); err == nil {
		t.Error("expected error for unknown partition filter")
	}

	// RUNNING job in squeue (260) whose log file does not exist yet returns empty LogTail with Next hint.
	fx.Logs = map[string]string{}
	lt, err := s.JobLog(ctx, LogInput{JobID: "260"})
	if err != nil || lt.Tail == nil || lt.Tail.Text != "" || !strings.Contains(lt.Next, "has not written") {
		t.Fatalf("JobLog on active job without log file yet: %+v (%v)", lt, err)
	}
	ex, err := s.JobExplain(ctx, "260", false, 80)
	if err != nil || !strings.Contains(ex.LogError, "log file not created yet") {
		t.Fatalf("JobExplain on active job without log file yet: %+v (%v)", ex, err)
	}

	// GPU partition stockout must NOT suggest CPU-only standard/spot.
	dir := t.TempDir()
	for _, name := range []string{"catalog.json", "sinfo.json", "sharing.txt", "sacct_jobs.json", "squeue.json", "partitions.json"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	nodes := `{"meta":{"slurm":{"release":"25.11.4"}},"nodes":[
		{"name":"g-0","state":["DOWN","CLOUD"],"partitions":["gpul4"],"reason":"RESOURCE_POOL_EXHAUSTED","cpus":16},
		{"name":"s-0","state":["IDLE","CLOUD"],"partitions":["standard"],"cpus":16}
	]}`
	_ = os.WriteFile(filepath.Join(dir, "nodes.json"), []byte(nodes), 0o644)
	s2, _ := newTestService(t)
	s2.Backend = &backend.Fixture{Dir: dir, User: "alice_ucr_edu"}
	why := s2.partitionTrouble(ctx, "gpul4")
	if strings.Contains(why, "standard") {
		t.Fatalf("GPU stockout must not suggest CPU-only standard: %q", why)
	}
}

func hasIssue(issues []ScriptIssue, sev, sub string) bool {
	for _, is := range issues {
		if is.Severity == sev && strings.Contains(is.Message, sub) {
			return true
		}
	}
	return false
}
