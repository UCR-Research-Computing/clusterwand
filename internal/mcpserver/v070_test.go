package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestV070MCPToolsAndAnnotations(t *testing.T) {
	cs := connect(t, "R1", "A1")
	tools := toolNames(t, cs)

	for _, want := range []string{"job_wait", "files_download"} {
		if _, ok := tools[want]; !ok {
			t.Fatalf("missing R1 tool %s", want)
		}
	}
	// job_submit_confirm must advertise destructiveHint=true (spends compute allocation).
	sc := tools["job_submit_confirm"]
	if sc == nil || sc.Annotations == nil || sc.Annotations.DestructiveHint == nil || !*sc.Annotations.DestructiveHint {
		t.Fatalf("job_submit_confirm must carry destructiveHint=true: %+v", sc)
	}

	// Call job_wait on a finished fixture job (236) and confirm failure_tails come back.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "job_wait",
		Arguments: map[string]any{"job_ids": []string{"236"}, "timeout_s": 10, "poll_s": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"complete":true`) || !strings.Contains(string(b), `"failure_tails"`) {
		t.Fatalf("unexpected job_wait structured content: %s", string(b))
	}

	// Call cluster_status with partition filter and script_check with partition override.
	stRes, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "cluster_status",
		Arguments: map[string]any{"partition": "gpul4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := json.Marshal(stRes.StructuredContent)
	if !strings.Contains(string(sb), `"name":"gpul4"`) || strings.Contains(string(sb), `"name":"computehigh"`) {
		t.Fatalf("cluster_status partition filter failed: %s", string(sb))
	}
}
