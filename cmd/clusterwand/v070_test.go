package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV070CLICommands(t *testing.T) {
	cfg := testConfig(t, "R1,A1")

	// 1. status --partition
	out, errOut, code := runCLI(t, "status", "--partition", "gpul4", "--config", cfg)
	if code != 0 || !strings.Contains(out, "gpul4") || strings.Contains(out, "computehigh") {
		t.Fatalf("status --partition gpul4 failed (code=%d): %s / %s", code, out, errOut)
	}

	// 2. wait & job wait on finished job 236
	for _, args := range [][]string{
		{"wait", "236", "--timeout", "10", "--poll", "2", "--config", cfg},
		{"job", "wait", "236", "--timeout", "10", "--config", cfg},
	} {
		out, errOut, code = runCLI(t, args...)
		if code != 0 || !strings.Contains(out, "Wait complete") || !strings.Contains(out, "236") {
			t.Fatalf("%v failed (code=%d): %s / %s", args, code, out, errOut)
		}
	}

	// 3. check --partition override
	scriptFile := filepath.Join(t.TempDir(), "run.sbatch")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\n#SBATCH -p gpul4\n#SBATCH --gres=gpu:1\n#SBATCH -c 4\n#SBATCH -t 30\necho hi\n"), 0o644)
	out, _, code = runCLI(t, "check", "--partition", "standard", scriptFile, "--config", cfg)
	if code != 2 || !strings.Contains(out, "GPUs requested on standard") {
		t.Fatalf("check --partition standard should catch --gres=gpu:1 on CPU partition (code=%d): %s", code, out)
	}

	// 4. submit --input (prepare + --yes confirm)
	inFile := filepath.Join(t.TempDir(), "params.txt")
	_ = os.WriteFile(inFile, []byte("alpha=1\n"), 0o644)
	goodScript := filepath.Join(t.TempDir(), "ok.sbatch")
	_ = os.WriteFile(goodScript, []byte("#!/bin/bash\n#SBATCH -p computehigh\n#SBATCH -N 1\n#SBATCH -t 10\necho ok\n"), 0o644)
	out, errOut, code = runCLI(t, "submit", goodScript, "--input", inFile+":cfg/params.txt", "--yes", "--config", cfg)
	if code != 0 || !strings.Contains(out, "inputs     1 file(s)") || !strings.Contains(out, "Submitted job") {
		t.Fatalf("submit --input --yes failed (code=%d): %s / %s", code, out, errOut)
	}
}
