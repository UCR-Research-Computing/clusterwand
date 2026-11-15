package rules

import (
	"strings"
	"testing"
)

func TestSwallowedExceptionOnCompletedJob(t *testing.T) {
	log := "step 1 ok\nTraceback (most recent call last):\n  File \"train.py\", line 42\nValueError: bad loss\ndone anyway\n"
	fs := Explain(Facts{State: "COMPLETED", ExitCode: "0", Log: log, LogReadable: true})
	f := want(t, fs, "swallowed-exception")
	if f.Severity != Warning || !strings.HasPrefix(f.Evidence[1], "untrusted log line: ") {
		t.Fatalf("unexpected finding: %+v", f)
	}
	// Harmless text on a COMPLETED job without a traceback must not fire error log rules.
	harmless := "find: '/proc/1/fd': Permission denied\nall outputs written\n"
	if got := Explain(Facts{State: "COMPLETED", ExitCode: "0", Log: harmless, LogReadable: true}); len(got) != 0 {
		t.Fatalf("unexpected findings on healthy completed job: %+v", got)
	}
}

func TestCudaOOMSuppressesCPUOOM(t *testing.T) {
	log := "RuntimeError: CUDA out of memory. Tried to allocate 2.00 GiB\n"
	fs := Explain(Facts{State: "FAILED", ExitCode: "1", Log: log, LogReadable: true, Site: ursa().Site})
	want(t, fs, "cuda")
	if hasRule(fs, "oom-log") {
		t.Fatalf("CUDA OOM must not emit host RAM oom-log finding: %+v", fs)
	}
}

func TestRequeuedPendingWarning(t *testing.T) {
	fs := Explain(Facts{State: "PENDING", Reason: "BeginTime", RestartCnt: 2})
	f := want(t, fs, "requeued")
	if f.Severity != Warning || !strings.Contains(strings.Join(f.Evidence, " "), "restarted 2 time(s)") {
		t.Fatalf("requeued finding: %+v", f)
	}
}

func TestScriptErrorQuotesLastLinesWithUntrustedPrefix(t *testing.T) {
	log := "starting solver\ncustom solver code E912: mesh inverted at cell 44\naborting run\n"
	fs := Explain(Facts{State: "FAILED", ExitCode: "3", Log: log, LogReadable: true})
	f := want(t, fs, "script-error")
	joined := strings.Join(f.Evidence, "\n")
	if !strings.Contains(joined, "untrusted log line: custom solver code E912") {
		t.Fatalf("script-error did not quote untrusted log tail: %v", f.Evidence)
	}
}
