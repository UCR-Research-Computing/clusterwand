package backend

// F2: the fixture answers the plain-text Slurm reads by printing its recorded
// --json answers the way squeue -O, sacct -P, scontrol -o and sacct -B print the
// same jobs (formats checked against Slurm 25.11.4 on Ursa Major and Slurm 21.08.6
// in a container, 2026-10-28). One recording feeds both paths, so the JSON/text
// parity tests compare like with like.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// textNum is Slurm's {"set","infinite","number"} or a bare number.
type textNum struct {
	Set      bool
	Infinite bool
	Number   float64
}

func (n *textNum) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		return nil
	}
	if s[0] == '{' {
		var w struct {
			Set      bool    `json:"set"`
			Infinite bool    `json:"infinite"`
			Number   float64 `json:"number"`
		}
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		*n = textNum{w.Set, w.Infinite, w.Number}
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = textNum{Set: true, Number: f}
	return nil
}

func (n textNum) epoch() string {
	if !n.Set || n.Infinite || n.Number <= 0 {
		return "N/A"
	}
	return fmt.Sprint(int64(n.Number))
}

// duration prints minutes as squeue/scontrol do: [D-]HH:MM:SS, UNLIMITED.
func (n textNum) duration() string {
	if n.Infinite || !n.Set {
		return "UNLIMITED"
	}
	m := int64(n.Number)
	d, h, mm := m/1440, (m%1440)/60, m%60
	if d > 0 {
		return fmt.Sprintf("%d-%02d:%02d:00", d, h, mm)
	}
	if h > 0 {
		return fmt.Sprintf("%d:%02d:00", h, mm)
	}
	return fmt.Sprintf("%d:00", mm)
}

func orNull(s string) string {
	if s == "" {
		return "(null)"
	}
	return s
}

// squeueTextFromJSON prints squeue.json as `squeue -h -O <fields>`.
func squeueTextFromJSON(b []byte) ([]byte, error) {
	var doc struct {
		Jobs []struct {
			JobID      int64    `json:"job_id"`
			ArrayJob   textNum  `json:"array_job_id"`
			ArrayTask  textNum  `json:"array_task_id"`
			User       string   `json:"user_name"`
			Account    string   `json:"account"`
			Partition  string   `json:"partition"`
			State      []string `json:"job_state"`
			NodeCount  textNum  `json:"node_count"`
			CPUs       textNum  `json:"cpus"`
			TresAlloc  string   `json:"tres_alloc_str"`
			TimeLimit  textNum  `json:"time_limit"`
			Submit     textNum  `json:"submit_time"`
			Start      textNum  `json:"start_time"`
			End        textNum  `json:"end_time"`
			Restarts   textNum  `json:"restart_cnt"`
			Reason     string   `json:"state_reason"`
			Nodes      string   `json:"nodes"`
			WorkDir    string   `json:"current_working_directory"`
			StdOut     string   `json:"standard_output"`
			StdErr     string   `json:"standard_error"`
			Name       string   `json:"name"`
			StdOutDone string   `json:"stdout_expanded"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, j := range doc.Jobs {
		master := fmt.Sprint(j.JobID)
		if j.ArrayJob.Number > 0 {
			master = fmt.Sprint(int64(j.ArrayJob.Number))
		}
		task := "N/A"
		if j.ArrayTask.Set {
			task = fmt.Sprint(int64(j.ArrayTask.Number))
		}
		state := ""
		if len(j.State) > 0 {
			state = j.State[0]
		}
		f := []string{fmt.Sprint(j.JobID), master, task, j.User, j.Account, j.Partition, state,
			fmt.Sprint(int64(j.NodeCount.Number)), fmt.Sprint(int64(j.CPUs.Number)), j.TresAlloc,
			j.TimeLimit.duration(), j.Submit.epoch(), j.Start.epoch(), j.End.epoch(), fmt.Sprint(int64(j.Restarts.Number)),
			j.Reason, j.Nodes, j.WorkDir, orNull(j.StdOut), orNull(j.StdErr), j.Name}
		out.WriteString(strings.Join(f, "\x1f") + "\n")
	}
	return []byte(out.String()), nil
}

// sacctTextFromJSON prints sacct_jobs.json as `sacct -P --delimiter=\x1f -o <fields>`
// with its header. legacy drops the fields Slurm before 23.02 lacks.
func sacctTextFromJSON(b []byte, legacy bool) ([]byte, error) {
	type exit struct {
		ReturnCode textNum `json:"return_code"`
		Signal     struct {
			ID textNum `json:"id"`
		} `json:"signal"`
	}
	type tres struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Count int64  `json:"count"`
	}
	type cpu struct {
		Seconds      int64 `json:"seconds"`
		Microseconds int64 `json:"microseconds"`
	}
	var doc struct {
		Jobs []struct {
			JobID int64 `json:"job_id"`
			Array struct {
				JobID  int64   `json:"job_id"`
				TaskID textNum `json:"task_id"`
			} `json:"array"`
			Name      string `json:"name"`
			User      string `json:"user"`
			Account   string `json:"account"`
			Partition string `json:"partition"`
			State     struct {
				Current []string `json:"current"`
				Reason  string   `json:"reason"`
			} `json:"state"`
			ExitCode        exit   `json:"exit_code"`
			Nodes           string `json:"nodes"`
			AllocationNodes int64  `json:"allocation_nodes"`
			KillRequestUser string `json:"kill_request_user"`
			RestartCnt      int64  `json:"restart_cnt"`
			Time            struct {
				Elapsed    int64   `json:"elapsed"`
				Start      int64   `json:"start"`
				End        int64   `json:"end"`
				Submission int64   `json:"submission"`
				Limit      textNum `json:"limit"`
				Total      cpu     `json:"total"`
			} `json:"time"`
			TRES struct {
				Allocated []tres `json:"allocated"`
				Requested []tres `json:"requested"`
			} `json:"tres"`
			WorkDir    string `json:"working_directory"`
			StdOut     string `json:"stdout"`
			StdErr     string `json:"stderr"`
			SubmitLine string `json:"submit_line"`
			Steps      []struct {
				Step struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"step"`
				State    []string `json:"state"`
				ExitCode exit     `json:"exit_code"`
				Nodes    struct {
					Count int64 `json:"count"`
				} `json:"nodes"`
				Time struct {
					Elapsed int64   `json:"elapsed"`
					Start   textNum `json:"start"`
					End     textNum `json:"end"`
					Total   cpu     `json:"total"`
				} `json:"time"`
				TRES struct {
					Requested struct {
						Max []tres `json:"max"`
					} `json:"requested"`
				} `json:"tres"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	const uid = "50001" // the anonymized fixture uid
	ec := func(e exit) string {
		if e.Signal.ID.Set {
			return fmt.Sprintf("0:%d", int64(e.Signal.ID.Number))
		}
		return fmt.Sprintf("%d:0", int64(e.ReturnCode.Number))
	}
	ts := func(v int64) string {
		if v <= 0 || v >= 4294967294 {
			return "None"
		}
		return fmt.Sprint(v)
	}
	cp := func(c cpu) string {
		s := float64(c.Seconds) + float64(c.Microseconds)/1e6
		d := int64(s) / 86400
		s -= float64(d * 86400)
		h, m := int64(s)/3600, (int64(s)%3600)/60
		sec := s - float64(h*3600+m*60)
		switch {
		case d > 0:
			return fmt.Sprintf("%d-%02d:%02d:%02d", d, h, m, int64(sec))
		case h > 0:
			return fmt.Sprintf("%02d:%02d:%02d", h, m, int64(sec))
		}
		return fmt.Sprintf("%02d:%06.3f", m, sec)
	}
	tr := func(l []tres) string {
		var out []string
		for _, t := range l {
			if t.Type == "energy" {
				continue
			}
			k := t.Type
			if t.Name != "" {
				k += "/" + t.Name
			}
			v := fmt.Sprint(t.Count)
			if t.Type == "mem" {
				v += "M"
			}
			out = append(out, k+"="+v)
		}
		return strings.Join(out, ",")
	}
	fields := strings.Split(sacctTextFields, ",")
	if legacy {
		fields = strings.Split(sacctLegacyFields, ",")
	}
	var out strings.Builder
	row := func(v map[string]string) {
		var f []string
		for _, k := range fields {
			f = append(f, v[k])
		}
		out.WriteString(strings.Join(f, "\x1f") + "\n")
	}
	out.WriteString(strings.Join(fields, "\x1f") + "\n")
	for _, j := range doc.Jobs {
		state := strings.Join(j.State.Current, ",")
		if j.KillRequestUser != "" && state == "CANCELLED" {
			by := uid
			if j.KillRequestUser != j.User {
				by = "0"
			}
			state += " by " + by
		}
		nodes, nn := j.Nodes, j.AllocationNodes
		if nn == 0 {
			nodes, nn = "None assigned", 1
		}
		id := fmt.Sprint(j.JobID)
		if j.Array.JobID != 0 && j.Array.TaskID.Set {
			id = fmt.Sprintf("%d_%d", j.Array.JobID, int64(j.Array.TaskID.Number))
		}
		limit := "UNLIMITED"
		if j.Time.Limit.Set && !j.Time.Limit.Infinite {
			limit = fmt.Sprint(int64(j.Time.Limit.Number))
		}
		row(map[string]string{"JobID": id, "JobIDRaw": fmt.Sprint(j.JobID), "UID": uid, "User": j.User, "Account": j.Account,
			"Partition": j.Partition, "State": state, "Reason": j.State.Reason, "ExitCode": ec(j.ExitCode), "NodeList": nodes,
			"NNodes": fmt.Sprint(nn), "Submit": ts(j.Time.Submission), "Start": ts(j.Time.Start), "End": ts(j.Time.End),
			"ElapsedRaw": fmt.Sprint(j.Time.Elapsed), "TotalCPU": cp(j.Time.Total), "TimelimitRaw": limit,
			"AllocTRES": tr(j.TRES.Allocated), "ReqTRES": tr(j.TRES.Requested), "Restarts": fmt.Sprint(j.RestartCnt),
			"WorkDir": j.WorkDir, "StdOut": j.StdOut, "StdErr": j.StdErr, "SubmitLine": j.SubmitLine, "JobName": j.Name})
		for _, s := range j.Steps {
			rss := ""
			for _, t := range s.TRES.Requested.Max {
				if t.Type == "mem" {
					rss = fmt.Sprint(t.Count)
				}
			}
			row(map[string]string{"JobID": s.Step.ID, "JobIDRaw": s.Step.ID, "Account": j.Account,
				"State": strings.Join(s.State, ","), "ExitCode": ec(s.ExitCode), "NodeList": j.Nodes,
				"NNodes": fmt.Sprint(s.Nodes.Count), "Submit": ts(int64(s.Time.Start.Number)), "Start": ts(int64(s.Time.Start.Number)),
				"End": ts(int64(s.Time.End.Number)), "ElapsedRaw": fmt.Sprint(s.Time.Elapsed), "TotalCPU": cp(s.Time.Total),
				"MaxRSS": rss, "JobName": s.Step.Name})
		}
	}
	return []byte(out.String()), nil
}

// scriptFromJSON prints `sacct -n -B -j <id>` for one recorded job.
func scriptFromJSON(b []byte, id string) ([]byte, error) {
	var doc struct {
		Jobs []struct {
			JobID  int64  `json:"job_id"`
			Script string `json:"script"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	want := strings.SplitN(id, "_", 2)[0]
	for _, j := range doc.Jobs {
		if fmt.Sprint(j.JobID) == want {
			s := j.Script
			if s == "" {
				s = "NONE\n"
			}
			return []byte("Batch Script for " + id + "\n" + strings.Repeat("-", 80) + "\n" + s + "\n"), nil
		}
	}
	return nil, nil
}

// nodesTextFromJSON prints nodes.json as `scontrol --version; scontrol -o show nodes`.
func nodesTextFromJSON(b []byte) ([]byte, error) {
	var doc struct {
		Nodes []struct {
			Name        string   `json:"name"`
			State       []string `json:"state"`
			CPULoad     int64    `json:"cpu_load"`
			CPUs        int64    `json:"cpus"`
			AllocCPUs   int64    `json:"alloc_cpus"`
			RealMemory  int64    `json:"real_memory"`
			AllocMemory int64    `json:"alloc_memory"`
			Partitions  []string `json:"partitions"`
			Reason      string   `json:"reason"`
			ReasonBy    string   `json:"reason_set_by_user"`
			ReasonAt    textNum  `json:"reason_changed_at"`
			Gres        string   `json:"gres"`
			LastBusy    textNum  `json:"last_busy"`
			BootTime    textNum  `json:"boot_time"`
			Version     string   `json:"version"`
		} `json:"nodes"`
		Meta struct {
			Slurm struct {
				Release string `json:"release"`
			} `json:"slurm"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var out strings.Builder
	if doc.Meta.Slurm.Release != "" {
		out.WriteString("slurm " + doc.Meta.Slurm.Release + "\n")
	}
	for _, n := range doc.Nodes {
		busy := "Unknown"
		if n.LastBusy.Set && n.LastBusy.Number > 0 {
			busy = n.LastBusy.epoch()
		}
		boot := "None"
		if n.BootTime.Set && n.BootTime.Number > 0 {
			boot = n.BootTime.epoch()
		}
		fmt.Fprintf(&out, "NodeName=%s CPUAlloc=%d CPUTot=%d CPULoad=%.2f Gres=%s RealMemory=%d AllocMem=%d State=%s Version=%s OS=Linux 4.18.0 #1 SMP Partitions=%s BootTime=%s LastBusyTime=%s",
			n.Name, n.AllocCPUs, n.CPUs, float64(n.CPULoad)/100, orNull(n.Gres), n.RealMemory, n.AllocMemory,
			strings.Join(n.State, "+"), n.Version, strings.Join(n.Partitions, ","), boot, busy)
		if n.Reason != "" {
			fmt.Fprintf(&out, " Reason=%s [%s@%s]", n.Reason, n.ReasonBy, n.ReasonAt.epoch())
		}
		out.WriteString("\n")
	}
	return []byte(out.String()), nil
}

// partitionsTextFromJSON prints partitions.json as `scontrol -o show partition`.
func partitionsTextFromJSON(b []byte) ([]byte, error) {
	var doc struct {
		Partitions []struct {
			Name     string `json:"name"`
			Accounts struct {
				Allowed string `json:"allowed"`
				Deny    string `json:"deny"`
			} `json:"accounts"`
			Groups struct {
				Allowed string `json:"allowed"`
			} `json:"groups"`
			Maximums struct {
				Time textNum `json:"time"`
			} `json:"maximums"`
			Nodes struct {
				Total int64 `json:"total"`
			} `json:"nodes"`
			Partition struct {
				State []string `json:"state"`
			} `json:"partition"`
		} `json:"partitions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	all := func(s string) string {
		if s == "" {
			return "ALL"
		}
		return s
	}
	var out strings.Builder
	for _, p := range doc.Partitions {
		deny := ""
		if p.Accounts.Deny != "" {
			deny = " DenyAccounts=" + p.Accounts.Deny
		}
		allowAcc := " AllowAccounts=" + all(p.Accounts.Allowed)
		if deny != "" {
			allowAcc = "" // scontrol prints one or the other
		}
		state := "UP"
		if len(p.Partition.State) > 0 {
			state = p.Partition.State[0]
		}
		fmt.Fprintf(&out, "PartitionName=%s AllowGroups=%s%s%s AllowQos=ALL Default=NO MaxTime=%s State=%s TotalNodes=%d\n",
			p.Name, all(p.Groups.Allowed), allowAcc, deny, p.Maximums.Time.duration(), state, p.Nodes.Total)
	}
	return []byte(out.String()), nil
}

// textAnswer answers a text template, or ok=false when a is not one.
func (f *Fixture) textAnswer(a []string) ([]byte, bool, error) {
	if len(a) < 3 || a[0] != "bash" {
		return nil, false, nil
	}
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(f.Dir, name)) }
	var rest []string
	if len(a) > 4 {
		rest = a[4:]
	}
	switch a[2] {
	case squeueTextTemplate:
		b, err := read("squeue.json")
		if err != nil {
			return nil, true, err
		}
		if id := flag(rest, "-j"); id != "" && !f.IgnoreJobFilter {
			if b, err = filterJobs(b, id, "job_id"); err != nil {
				return nil, true, err
			}
		}
		out, err := squeueTextFromJSON(b)
		return out, true, err
	case sacctTextTemplate:
		b, err := read("sacct_jobs.json")
		if err != nil {
			return nil, true, err
		}
		if id := flag(rest, "-j"); id != "" && !f.IgnoreJobFilter {
			if b, err = filterJobs(b, id, "job_id"); err != nil {
				return nil, true, err
			}
		}
		out, err := sacctTextFromJSON(b, f.LegacySacct)
		return out, true, err
	case nodesTextTemplate:
		b, err := read("nodes.json")
		if err != nil {
			return nil, true, err
		}
		out, err := nodesTextFromJSON(b)
		return out, true, err
	}
	return nil, false, nil
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
