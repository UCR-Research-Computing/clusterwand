package backend

import (
	"strings"
	"testing"
)

func TestNormalizeSince(t *testing.T) {
	cases := map[string]string{
		"now-7days":  "now-7days",
		"now-1day":   "now-1days",
		"now-1hour":  "now-1hours",
		"now-30min":  "now-30minutes",
		"24h":        "now-24hours",
		"12hr":       "now-12hours",
		"7d":         "now-7days",
		"30m":        "now-30minutes",
		"2w":         "now-14days",
		"2026-11-01": "2026-11-01",
	}
	for in, want := range cases {
		if got := NormalizeSince(in); got != want {
			t.Errorf("NormalizeSince(%q) = %q, want %q", in, got, want)
		}
		if err := ValidSince(in); err != nil {
			t.Errorf("ValidSince(%q) failed: %v", in, err)
		}
	}
	c, err := SacctUser("alice", "24h", "30m")
	if err != nil {
		t.Fatal(err)
	}
	line := c.String()
	if !strings.Contains(line, "-S now-24hours") || !strings.Contains(line, "-E now-30minutes") {
		t.Fatalf("SacctUser did not normalize shorthands: %s", line)
	}
	for _, bad := range []string{"0d", "9999w", "yesterday", "24h; id"} {
		if err := ValidSince(bad); err == nil {
			t.Errorf("ValidSince(%q) accepted invalid input", bad)
		}
	}
}

func TestCleanSSHEnvStripsPythonVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"PYTHONPATH=/opt/hermes/py314/site-packages",
		"PYTHONHOME=/opt/hermes/py314",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
	}
	got := cleanSSHEnv(in)
	for _, kv := range got {
		if strings.HasPrefix(kv, "PYTHONPATH=") || strings.HasPrefix(kv, "PYTHONHOME=") {
			t.Fatalf("leaked python env var in %v", got)
		}
	}
	if len(got) != 2 || got[0] != "PATH=/usr/bin:/bin" || got[1] != "SSH_AUTH_SOCK=/tmp/agent.sock" {
		t.Fatalf("unexpected cleaned env: %v", got)
	}
}

func TestStageInputsAndSubmitStagedValidate(t *testing.T) {
	goodDir := "clusterwand-jobs/20261115-120000-sim"
	opts := SubmitOpts{Partition: "standard", Nodes: 1, TimeMin: 30, JobName: "sim", Comment: "clusterwand:0123456789ab"}
	si, err := StageInputs(goodDir, []byte("tarball"))
	if err != nil || !si.Write() {
		t.Fatalf("StageInputs: %v write=%v", err, si.Write())
	}
	ss, err := SubmitStagedBatch(goodDir, opts, []byte("#!/bin/bash\n"))
	if err != nil || !ss.Write() {
		t.Fatalf("SubmitStagedBatch: %v write=%v", err, ss.Write())
	}
	for _, bad := range []string{"../escape", "/abs/path", "clusterwand-jobs/../../x", "clusterwand-jobs/bad;id"} {
		if _, err := StageInputs(bad, []byte("x")); err == nil {
			t.Errorf("StageInputs accepted %q", bad)
		}
		if _, err := SubmitStagedBatch(bad, opts, []byte("#!/bin/bash\n")); err == nil {
			t.Errorf("SubmitStagedBatch accepted %q", bad)
		}
	}
	if _, err := StageInputs(goodDir, nil); err == nil {
		t.Error("StageInputs accepted empty archive")
	}
}
