#!/usr/bin/env bash
# Mutation check: disable each safety guard in turn and confirm a test fails.
# Restores every file from a backup copy in a trap. Run: scripts/mutation_check.sh
set -u
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.8} PYTHONDONTWRITEBYTECODE=1
BK=$(mktemp -d)
FILES="internal/policy/audit.go internal/core/jobs.go internal/backend/command.go internal/core/service.go internal/policy/redact.go internal/core/a1.go internal/core/results.go internal/slurm/types.go internal/core/cluster.go internal/rules/rules.go internal/core/files.go internal/core/helpers.go internal/backend/files.go internal/core/p2.go internal/core/shared.go internal/config/config.go internal/core/catalog.go internal/core/dbcheck.go internal/slurm/sacctrows.go internal/core/allocation.go internal/site/site.go sites/ursa-major/site.yaml internal/slurm/text.go internal/core/access.go internal/core/slurmread.go internal/backend/ssh.go internal/discover/discover.go internal/discover/record.go internal/discover/hostkeys.go cmd/clusterwand/discover.go internal/mcpserver/server.go"
for f in $FILES; do mkdir -p "$BK/$(dirname "$f")"; cp "$f" "$BK/$f"; done
restore() { for f in $FILES; do cp "$BK/$f" "$f"; done; }
trap restore EXIT

mutate() { # name file python-replace(old,new)
  local name=$1 file=$2 old=$3 new=$4
  # ONLY=<prefix> runs just the guards whose name starts with it (e.g. ONLY=v093)
  case "$name" in "${ONLY:-}"*) ;; *) return;; esac
  case " $FILES " in *" $file "*) ;; *) echo "ERROR $name: $file is not in FILES (would not be restored)"; exit 2;; esac
  if python3 - "$file" "$old" "$new" <<'EOF'
import sys
p, old, new = sys.argv[1:]
t = open(p).read()
assert t.count(old) == 1, f"pattern not unique in {p}: {old!r}"
open(p, "w").write(t.replace(old, new))
EOF
  then :; else echo "BROKEN    $name (pattern did not apply; nothing was tested)"; FAIL=1; restore; return; fi
  if go test -count=1 ./... >/dev/null 2>&1; then echo "SURVIVED  $name"; FAIL=1; else echo "killed    $name"; fi
  restore
}
mutate2() { # name file old1 new1 old2 new2: two mutations in one file at once
  local name=$1 file=$2
  case "$name" in "${ONLY:-}"*) ;; *) return;; esac
  if python3 -c '
import sys
p, o1, n1, o2, n2 = sys.argv[1:]
t = open(p).read()
for o, n in ((o1, n1), (o2, n2)):
    assert t.count(o) == 1, "pattern not unique in %s: %r" % (p, o)
    t = t.replace(o, n)
open(p, "w").write(t)
' "$file" "$3" "$4" "$5" "$6"
  then :; else echo "BROKEN    $name (pattern did not apply; nothing was tested)"; FAIL=1; restore; return; fi
  if go test -count=1 ./... >/dev/null 2>&1; then echo "SURVIVED  $name"; FAIL=1; else echo "killed    $name"; fi
  restore
}
FAIL=0
mutate "tier check"        internal/policy/audit.go   'if t == tier {' 'if true {'
mutate "owner check"       internal/core/jobs.go      'if owner != me && !in.AnyUser {' 'if false {'
mutate "log root check"    internal/core/jobs.go      'if strings.HasPrefix(clean, r) {' 'if true {'
mutate "job id validation" internal/backend/command.go 'if !regexp.MustCompile(`^\d{1,10}(_\d{1,7})?$`).MatchString(id) {' 'if false {'
mutate "quoting"           internal/backend/command.go 'return "'"'"'" + strings.ReplaceAll(s, "'"'"'", `'"'"'\'"'"''"'"'`) + "'"'"'"' 'return s'
mutate "redaction"         internal/policy/redact.go  's = r.re.ReplaceAllString(s, r.repl)' '_ = r'
mutate "log line cap"      internal/core/jobs.go      'lines = s.Cfg.Limits.LogLines' 'lines = lines'
mutate "rate limit"        internal/policy/audit.go   'if l.tokens < 1 {' 'if false {'
mutate "script redaction"  internal/core/jobs.go      'd.Script = policy.Wrap(j.Script, s.Cfg.Limits.ScriptBytes)' 'd.Script = \&policy.Untrusted{Note: policy.UntrustedNote, Text: j.Script}'
mutate "A1 single-use token"  internal/core/a1.go 'delete(st.Pending, key) // single use, even when it then fails' '_ = key'
mutate "A1 token expiry"      internal/core/a1.go 'if s.Now().After(p.Expires) {
		return pending{}, errors.New("confirm_token expired' 'if false {
		return pending{}, errors.New("confirm_token expired'
mutate "A1 plan hash check"   internal/core/a1.go 'if submitHash(p.Dir, p.Opts, hashOf(p.Script)) != p.Hash {' 'if submitHash(p.Dir, p.Opts, hashOf(p.Script)) == "never" {'
mutate "MPI prerequisite"     internal/core/cluster.go 'if loadedBefore(lines, mpi) {' 'if true {'
mutate "A1 node cap"          internal/core/a1.go 'if nodes > caps.MaxNodes {' 'if false {'
mutate "A1 hour cap"          internal/core/a1.go 'if float64(mins) > caps.MaxHours*60 {' 'if false {'
mutate "A1 job cost cap"      internal/core/a1.go 'if worst > jobCap {' 'if false {'
mutate "A1 day cap (prepare)" internal/core/a1.go 'if spent+worst > dayCap {' 'if false {'
mutate "A1 day cap (confirm)" internal/core/a1.go 'if spent+p.WorstUSD > dayCap {' 'if false {'
mutate "A1 time required"     internal/core/a1.go 'if !ok || mins <= 0 {' 'if false {'
mutate "A1 kind check"        internal/core/a1.go 'if p.Kind != kind {' 'if false {'
mutate "results traversal"    internal/core/results.go 'if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {' 'if false {'
mutate "results no overwrite" internal/core/results.go 'target = freeName(target)' 'target = target'
mutate "results home guard"   internal/core/results.go 'if dir == home || dir == home+"/" {' 'if false {'
mutate "A1 cross-process lock" internal/core/a1.go 'if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {' 'if false {'
mutate "redact whole assignment" internal/policy/redact.go 'for _, r := range redactRules {' 'for _, r := range append(append([]redactRule{}, redactRules[1:]...), redactRules[0]) {'
mutate "booting is not broken" internal/slurm/types.go 'return n.HasState("NOT_RESPONDING") && !n.Booting()' 'return n.HasState("NOT_RESPONDING")'
mutate "release only never-ran" internal/core/a1.go 'neverRan := d.State == "PENDING" && d.Started == ""' 'neverRan := true'
mutate "rule cmd-not-found case" internal/rules/rules.go '(?mi)(\S+): command not found\s*$' '(?m)(\S+): command not found$'
mutate "rule exit 127"           internal/rules/rules.go 'if st == "FAILED" && f.ExitCode == "127" && !hasRule(out, "command-not-found") {' 'if false {'
mutate "v07 user root check"     internal/core/files.go 'if p == r {
			return nil
		}
		if strings.HasPrefix(p, r+"/") {' 'if p == r {
			return nil
		}
		if true {'
mutate "v07 hidden component"     internal/core/files.go '				if hiddenOrSensitive(part) {' '				if false {'
mutate "v07 realpath recheck"     internal/core/files.go '	if err := checkUserPath(real, roots); err != nil {' '	if err := checkUserPath(p, roots); err != nil {'
mutate "v07 listing hides"        internal/core/files.go '		if hiddenOrSensitive(name) {
			l.Hidden++' '		if false {
			l.Hidden++'
mutate "v07 sensitive names"      internal/core/files.go 'func sensitive(name string) bool { return sensitiveName.MatchString(name) }' 'func sensitive(name string) bool { return false }'
mutate "v07 chunk redaction"      internal/core/files.go '	r.Chunk, r.Text = info, policy.Wrap(text, 0)' '	r.Chunk, r.Text = info, \&policy.Untrusted{Text: text}'
mutate "v07 grep redaction"       internal/core/files.go '		Lines: policy.Wrap(rest, 0)}, nil' '		Lines: \&policy.Untrusted{Text: rest}}, nil'
mutate "v07 binary refusal"       internal/core/files.go '	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {' '	if false {'
mutate "v07 utf8 boundary"        internal/core/files.go '	for len(b) > 0 && offset > 0 && b[0]&0xC0 == 0x80 {' '	for false {'
mutate "v07 results read in listing" internal/core/results.go '		if !hasFile(all, in.Read) {' '		if false {'
mutate "v07 results prefix check" internal/core/results.go '		if err := backend.ValidRelPath(in.Prefix); err != nil {' '		if err := error(nil); err != nil {'
mutate "v07 env version allowlist" internal/core/helpers.go '	c, err := backend.EnvCheck(s.Cfg.EnvPartition, modules, commands, versionCommands)' '	c, err := backend.EnvCheck(s.Cfg.EnvPartition, modules, commands, map[string]bool{"myprog": true, "gcc": true, "python3": true})'
mutate "v07 interactive gpus"     internal/core/helpers.go '	if in.GPUs < 0 || in.GPUs > cp.GPUsPerNode {' '	if in.GPUs < 0 {'
mutate "v07 interactive memory"   internal/core/helpers.go '	if in.Memory != "" && !reMemory.MatchString(in.Memory) {' '	if false {'
mutate "v07 storage hides creds"  internal/core/helpers.go '		if sensitive(path.Base(p)) {' '		if false {'
mutate "v07 grep pattern check"   internal/backend/files.go '	if p == "" || len(p) > 200 || strings.ContainsAny(p, "\x00\n\r") {' '	if p == "" {'
mutate "v07 env command names"    internal/backend/files.go '		if err := ValidCommandName(c); err != nil {
			return Command{}, err
		}' ''
mutate "v07 read size cap"        internal/backend/files.go '	if n < 1 || n > MaxReadBytes {' '	if n < 1 {'
mutate "v07 signature redaction"  internal/policy/redact.go '	{regexp.MustCompile(`(?i)(X-Goog-Signature=)[0-9a-f]{16,}`), "${1}[REDACTED]"},' ''
mutate "v07 trace redaction"      internal/core/service.go '		t.cmds = append(t.cmds, policy.Redact(key))' '		t.cmds = append(t.cmds, key)'
mutate "v071 unreachable not missing" internal/core/files.go '	if errors.Is(err, backend.ErrUnreachable) {
		return "", err // a transport problem, not a missing file
	}' ''
mutate "v072 module_show loads MPI"   internal/core/cluster.go 'c, err := backend.ModuleShowUnder(name, res.LoadedFirst)' 'c, err := backend.ModuleShowUnder(name, "")'
mutate "v072 module_show prereq validated" internal/backend/command.go '		if !reModule.MatchString(prereq) {' '		if false {'
mutate "v072 module_show mpi must match" internal/core/cluster.go '		if !ok {
			if len(choices) == 0 {' '		if false {
			if len(choices) == 0 {'
mutate "v072 completed job not a failure" internal/core/p2.go '	return j.State == "COMPLETED" && (j.ExitCode == "0" || j.ExitCode == "")' '	return false'
mutate "v072 failed job not a success"   internal/core/p2.go '	return j.State == "COMPLETED" && (j.ExitCode == "0" || j.ExitCode == "")' '	return j.ExitCode == "0" || j.ExitCode == ""'
mutate "v072 jobs_list config cap"       internal/core/jobs.go '	if limit > s.Cfg.Limits.ListRows {
		limit = s.Cfg.Limits.ListRows
	}
	var qsel' '	var qsel'
mutate "v080 no-core error on shared"   internal/core/cluster.go '		if shared && !cr.CoresAsked {' '		if false {'
# v080's explicit shared-cores re-check is now backed up by v0.9.16's full static
# check of the override; the guard disables both, so the pair is what is proven.
mutate2 "v080 submit re-checks override"  internal/core/a1.go '	} else if ok && s.partitionShared(ctx, part) && !coresAsked(req) && !sharedRuleReported(blocking) {' '	} else if false {' '	if in.Partition != "" && in.Partition != sc.Request["partition"] {' '	if false {'
mutate "v080 shared price share"         internal/core/a1.go '		share = coreRequest(req, cp, true).NodeShare' '		share = 1'
mutate "v080 exclusive pays the node"    internal/core/shared.go '	if r.Exclusive || !shared {' '	if !shared {'
mutate "v080 memory share counts"        internal/core/shared.go '		if m := float64(mem) / float64(nodeMemMB); m > share {' '		if m := 0.0; m > share {'
mutate "v080 memory capped at node"     internal/core/shared.go '	if nodeMemMB > 0 && mem > nodeMemMB {
		mem = nodeMemMB
	}' ''
mutate "v080 cores capped at node"       internal/core/shared.go '	if nodeCores > 0 && cores > nodeCores {
		cores = nodeCores
	}' ''
mutate "v080 EXCLUSIVE is not shared"    internal/core/shared.go '		out[strings.TrimSuffix(name, "*")] = mode != "" && mode != "EXCLUSIVE"' '		out[strings.TrimSuffix(name, "*")] = mode != ""'
mutate "v080 failed read is exclusive"   internal/core/shared.go '	if err != nil {
		return out
	}
	for _, line' '	if err != nil {
		return map[string]bool{"computehigh": true}
	}
	for _, line'
mutate "v080 every core request counts"  internal/core/shared.go '	for _, k := range []string{"cpus-per-task", "ntasks", "ntasks-per-node", "exclusive"} {' '	for _, k := range []string{"cpus-per-task"} {'
mutate "v080 script_check share price"   internal/core/cluster.go '			c := round(price*float64(nodes)*cr.NodeShare*float64(mins)/60, 2)' '			c := round(price*float64(nodes)*float64(mins)/60, 2)'
mutate "v081 no-core error wording"     internal/core/cluster.go 'or #SBATCH --exclusive for the whole node", part, cr.DefaultNote)' 'or #SBATCH --exclusive for the whole node", part, strings.TrimPrefix(cr.DefaultNote, "asks for no cores, so Slurm gives it "))'
mutate "v081 commented module load"     internal/core/cluster.go '		if strings.HasPrefix(ln, "#") {
			continue // a comment that mentions `module load x` loads nothing
		}' ''
mutate "v081 run-time checks skip comments" internal/core/cluster.go '	code := codeOnly(script)' '	code := script'
mutate "v080 usage cost share"           internal/core/cluster.go '				x.row.CostUSD += price * nodes * hrs * allocShare(cat, j.Partition, int64(nodes), int64(cores), j.TRES.Allocated.Get("mem"))' '				x.row.CostUSD += price * nodes * hrs'
mutate "v080 job list cost share"        internal/core/jobs.go '		share := allocShare(cat, j.Partition, n, js.CPUs, j.TRES.Allocated.Get("mem"))' '		share := 1.0'
mutate "v080 waste node-hours share"    internal/core/p2.go '		share := allocShare(cat, j.Partition, int64(nodes), int64(cores), j.TRES.Allocated.Get("mem"))' '		share := 1.0'
mutate "v080 alloc share capped"         internal/core/shared.go '	if share <= 0 || share > 1 {' '	if share <= 0 {'
mutate "v080 alloc memory share"         internal/core/shared.go '		share = math.Max(share, float64(memMB)/float64(nodes)/nodeMem)' '		_ = nodeMem'
mutate "v080 interactive share"          internal/core/helpers.go '		share = cr.NodeShare
' '		_ = cr
'
mutate "v080 interactive no-cpu warning" internal/core/helpers.go '		if in.CPUs == 0 {
			r.Warnings' '		if false {
			r.Warnings'
mutate "v093 job_ids filter applied" internal/core/jobs.go '			if want[strings.SplitN(j.JobID, "_", 2)[0]] {' '			if true {'
mutate "v093 job_ids validated" internal/core/jobs.go '		if err := backend.ValidJobID(id); err != nil {
			return nil, err
		}
		want[' '		if false {
			return nil, nil
		}
		want['
mutate "v093 job_ids count cap" internal/core/jobs.go '	if len(in.JobIDs) > MaxJobIDs {' '	if false {'
mutate "v093 restarts on queue rows" internal/core/jobs.go '		Restarts: j.RestartCnt.Int(),' '		Restarts: 0,'
mutate "v093 restarts on accounting rows" internal/core/jobs.go 'ExitCode: j.ExitCode.String(), Restarts: j.RestartCnt,' 'ExitCode: j.ExitCode.String(),'
mutate "v094 redirect ends module list" internal/core/cluster.go '		if strings.ContainsAny(w, "<>") {
			break' '		if false {
			break'
mutate "v096 env in a job"         internal/backend/files.go '	argv := append(EnvJobArgs(partition), "bash", "-lc", envTemplate, "clusterwand", strconv.Itoa(len(modules)))' '	argv := []string{"bash", "-lc", envTemplate, "clusterwand", strconv.Itoa(len(modules))}'
mutate "v096 env one core"         internal/backend/files.go '"-N", "1", "-n", "1", "-c", "1", "-t", "3",' '"-N", "1", "-n", "1", "-t", "3",'
mutate "v096 env gives up"         internal/backend/files.go '		"--immediate=120", "--quiet", "-J", "clusterwand-env-check"}' '		"--quiet", "-J", "clusterwand-env-check"}'
mutate "v096 env partition checked" internal/backend/files.go '	if !rePartition.MatchString(partition) {
		return Command{}, fmt.Errorf("env_check partition' '	if false {
		return Command{}, fmt.Errorf("env_check partition'
mutate "v096 env uses config"      internal/core/helpers.go '	c, err := backend.EnvCheck(s.Cfg.EnvPartition, modules, commands, versionCommands)' '	c, err := backend.EnvCheck("standard", modules, commands, versionCommands)'
mutate "v096 env reports job"      internal/core/helpers.go '			r.JobID, r.Node = policy.CleanLabel(f[1], 20), policy.CleanLabel(f[2], 80)' '			_ = f'
mutate "v096 env timeout"          internal/backend/files.go '	return Command{argv: argv, kind: KindNoCache, timeout: 200 * time.Second}, nil' '	return Command{argv: argv, kind: KindNoCache, timeout: 120 * time.Second}, nil'
mutate "v096 config partition"     internal/config/config.go '	if c.EnvPartition != "" && !rePartitionName.MatchString(c.EnvPartition) {' '	if false {'
mutate "v097 job_ids uncached"     internal/backend/command.go '	return Command{argv: []string{"squeue", "--json", "-j", list}, kind: KindNoCache, okExit: []int{1}}, nil' '	return Command{argv: []string{"squeue", "--json", "-j", list}, kind: KindQueue, okExit: []int{1}}, nil'
mutate "v097 job_ids by id"        internal/core/jobs.go '	byID := len(want) > 0 && !in.All' '	byID := false'
mutate "v097 own queue rows only"  internal/core/jobs.go '		if byID && j.UserName != who {' '		if false {'
mutate "v097 own acct rows only"   internal/core/jobs.go '		if byID && j.User != who {' '		if false {'
mutate "v097 job list validated"   internal/backend/command.go '		if err := ValidJobID(id); err != nil {
			return "", err' '		if false {
			return "", nil'
mutate "v098 idle alt order"       sites/ursa-major/site.yaml '  fallback_idle: [standard, spot, computehigh, nvmescratch, highmem]' '  fallback_idle: [computehigh, standard, spot, nvmescratch, highmem]'
mutate "v098 cold alt order"       sites/ursa-major/site.yaml '  fallback_cold: [standard, spot, computehigh]' '  fallback_cold: [computehigh, standard, spot]'
mutate "v099 time over MaxTime"     internal/core/cluster.go '		if maxMin, ok := slurmMinutes(p.TimeLimit); ok && hasTime && maxMin > 0 && mins > maxMin {' '		if maxMin, ok := slurmMinutes(p.TimeLimit); false && ok && hasTime && maxMin > 0 && mins > maxMin {'
mutate "v099 time unlimited ok"    internal/core/cluster.go '		if maxMin, ok := slurmMinutes(p.TimeLimit); ok && hasTime && maxMin > 0 && mins > maxMin {' '		if maxMin, ok := slurmMinutes(p.TimeLimit); hasTime && mins > maxMin && ok || p.TimeLimit == "infinite" {'
mutate "v099 avx partitions"       internal/core/cluster.go '		if s.Site.AVX2Only(part) {' '		if true {'
mutate "v099 avx comments"         internal/core/cluster.go '			if m := reAVX512.FindString(codeOnly(script)); m != "" {' '			if m := reAVX512.FindString(script); m != "" {'
mutate "v099 mpi hint multinode"   internal/core/cluster.go 'ok && nodes > 1 && reMPIRun.MatchString(codeOnly(script)) {' 'ok && reMPIRun.MatchString(codeOnly(script)) {'
mutate "v099 waste ch standard"    internal/core/p2.go 'j.Partition == fast && cores <= float64(cpusOf(cat, gen)) {' 'j.Partition == fast && cores <= 0 {'
mutate "v099 waste ch only"        internal/core/p2.go 'j.Partition == fast && cores <= float64(cpusOf(cat, gen)) {' 'cores <= float64(cpusOf(cat, gen)) {'
mutate "v0910 sigill not crash"    internal/rules/rules.go '			if r.id == "tool-crash" && hasRule(out, "illegal-instruction") {' '			if false {'
mutate "v0910 sigill exit quiet"   internal/rules/rules.go '"tool-crash", "illegal-instruction", "oom"' '"tool-crash", "oom"'
mutate "v0910 cpu note"            internal/site/site.go '	if n, ok := p.Partitions.CPU[part]; ok {' '	if n, ok := p.Partitions.CPU[part]; false && ok {'
mutate "v0911 db- lists all"     internal/core/catalog.go '		return append([]Dataset(nil), c.Datasets.Items...)' '		return nil'
mutate "v0911 storage db note"   internal/core/helpers.go 'len(cat.Datasets.Items) > 0 {' 'len(cat.Datasets.Items) > 9999 {'
mutate "v0912 db download warn"   internal/core/dbcheck.go '				if !ok || seen[name] {' '				if true {'
mutate "v0912 hosted only"        internal/core/dbcheck.go '				if !ok || seen[name] {' '				if seen[name] {'
mutate "v0912 array percent"      internal/core/dbcheck.go '	if m := reArraySpc.FindStringSubmatch(spec); m != nil {' '	if m := reArraySpc.FindStringSubmatch(spec); m != nil && false {'
mutate "v0912 big scan gate"      internal/core/dbcheck.go '	if n := arrayConcurrency(array); n > 8 && reBigScan.MatchString(codeOnly(script)) {' '	if n := arrayConcurrency(array); n > 8 {'
mutate "v0914 step rows not jobs"   internal/slurm/sacctrows.go '		if parent, _, isStep := strings.Cut(id, "."); isStep {' '		if parent, _, isStep := strings.Cut(id, "."); isStep && false {'
mutate "v0914 step peak memory"    internal/slurm/sacctrows.go '			jobs[i].Steps = append(jobs[i].Steps, st)' '			_ = st'
mutate "v0914 signal exit"         internal/slurm/sacctrows.go '	if n := atoi(sig); n != 0 {' '	if n := atoi(sig); n != 0 && false {'
mutate "v0914 never started nodes" internal/slurm/sacctrows.go '		if j.Nodes == "None assigned" {' '		if false {'
mutate "v0914 state before by"     internal/slurm/sacctrows.go '		state, _, _ := strings.Cut(f[3], " ") // "CANCELLED by 50001"' '		state := f[3]'
mutate "v0914 name keeps pipes"    internal/slurm/sacctrows.go '		f := strings.SplitN(line, "|", sacctFieldCount)' '		f := strings.Split(line, "|")'
mutate "v0914 cpu days"            internal/slurm/sacctrows.go '		days, s = atoi(d), rest' '		s = rest; _ = d'
mutate "v0914 mem MB"              internal/slurm/sacctrows.go '			t.Count = memBytes(v) / (1024 * 1024)' '			t.Count = memBytes(v)'
mutate "v0914 usage uses summary"  internal/core/cluster.go '		c, err = backend.SacctSummaryUser(firstNonEmpty(in.User, me), in.Since, in.Until)' '		c, err = backend.SacctUser(firstNonEmpty(in.User, me), in.Since, in.Until)'
mutate "v0914 jobs uses summary"   internal/core/jobs.go '		qsel = queueSel{user: who}
		ac, err = backend.SacctSummaryUser(who, in.Since, "")' '		qsel = queueSel{user: who}
		ac, err = backend.SacctUser(who, in.Since, "")'
mutate "v0914 user not widened"    internal/backend/command.go '	return sacctSummary([]string{"-u", user}, since, until)' '	return sacctSummary([]string{"-a"}, since, until)'
mutate "v0914 user validated"      internal/backend/command.go '	if err := ValidUser(user); err != nil {
		return Command{}, err
	}
	return sacctSummary(' '	if false {
		return Command{}, nil
	}
	return sacctSummary('
mutate "v0914 script note"         internal/core/jobs.go '				d.ScriptNote = s.Site.Text("script_not_stored"' '				_ = s.Site.Text("script_not_stored"'
mutate "v0914 du parallel"         internal/backend/files.go 'xargs -0 -r -P 8 -n 1' 'xargs -0 -r -P 1 -n 1'
mutate "v0914 du budget"           internal/backend/files.go 'end=$(( $(date +%s) + 40 ))' 'end=$(( $(date +%s) + 100 ))'
# v0.9.16 review fixes
mutate "v0916 override full check"     internal/core/a1.go '	if in.Partition != "" && in.Partition != sc.Request["partition"] {' '	if false {'
mutate "v0916 confirm submit count"    internal/core/a1.go '	if s.Cfg.Caps.MaxSubmitsPerDay > 0 && n >= s.Cfg.Caps.MaxSubmitsPerDay {' '	if false {'
mutate "v0916 time refuses junk"       internal/core/cluster.go '			if c < '"'"'0'"'"' || c > '"'"'9'"'"' {' '			if false {'
mutate "v0916 seconds round up"        internal/core/cluster.go '		return v[0] + up(v[1]), true' '		return v[0], true'
# v0.10.0 allocation framing (F4 show_cost_tiers, F5 allocations)
mutate "v010 R1 never sees per-member figures" internal/core/allocation.go 'if !hasTier(s.Cfg.Tiers, "R2") {' 'if false {'
mutate "v010 show_cost_tiers stripping"    internal/core/service.go 'stripCostFields(s, data)' '// stripCostFields(s, data)'
# F1 (v0.2.0) site profiles: approval gate, path/partition validation, charge model gating
mutate "f1 unapproved site refused"   internal/site/site.go '	if !p.Approved {' '	if false {'
mutate "f1 catalog path checked"      internal/site/site.go '!path.IsAbs(p.Catalog) || path.Clean(p.Catalog) != p.Catalog' 'false'
mutate "f1 user dir has user"         internal/site/site.go 'strings.Count(d, "{user}") == 1' 'true'
mutate "f1 log roots checked"         internal/site/site.go '		if !strings.HasSuffix(r, "/") || !validUserDir(strings.TrimSuffix(r, "/")) {' '		if false {'
mutate "f1 partition names checked"   internal/site/site.go '		if n != "" && !rePartition.MatchString(n) {' '		if false {'
mutate "f1 site name checked"         internal/site/site.go '	if !reName.MatchString(name) {
		return nil, fmt.Errorf("site name %q is not valid", name)' '	if false {
		return nil, fmt.Errorf("site name %q is not valid", name)'
mutate "f1 unknown yaml fields"       internal/site/site.go '	dec.KnownFields(true)' '	dec.KnownFields(false)'
mutate "f1 su needs rates"            internal/site/site.go '		if len(p.Charge.Rates) == 0 {' '		if false {'
mutate "f1 dollars only on usd sites" internal/core/service.go 'func (s *Service) chargedInUSD() bool { return s.Site.Model() == site.ChargeUSDPerNodeHour }' 'func (s *Service) chargedInUSD() bool { return true }'
mutate "f1 no charge no figures"      internal/core/service.go '	if !s.Cfg.ShowCost || s.Site.Model() == site.ChargeNone {' '	if !s.Cfg.ShowCost {'
mutate "f1 su site needs caps"        internal/core/a1.go '	if unit == "SU" && (jobCap <= 0 || dayCap <= 0) {' '	if false {'
mutate "f1 unpriced partition refused" internal/core/a1.go '	if !priced && unit != "" {' '	if false {'
mutate "f1 su rate per core"          internal/core/service.go '				return r * float64(p.CPUsPerNode), true' '				return r, true'
mutate "f1 env needs probe"           internal/core/helpers.go '	if s.Cfg.EnvPartition == "" {' '	if false {'
mutate "f1 roots from site"           internal/core/files.go '	roots = []string{site.Dir(s.Site.Paths.Home, user)}' '	roots = []string{site.Dir(s.Site.Paths.Home, user), "/home/" + user, "/scratch/" + user}'
mutate "f1 bad site is an error"      internal/core/service.go '	if err != nil {
		return cfg, nil, err
	}' '	if err != nil {
		sp, err = site.Generic(), nil
	}'
mutate "f1 sharing-only usd strip"    internal/core/service.go '	case *ScriptCheck:
		if v != nil {
			v.EstCostUSD = nil' '	case *ScriptCheck:
		if v != nil {
			_ = v'
mutate "f1 waste price gated"         internal/core/p2.go 'if p, ok := s.price(cat, part); ok && s.canSeeCost() {' 'if p, ok := s.price(cat, part); ok && s.Cfg.ShowCost {'
mutate "f1 catalog resource stripped" internal/core/service.go '				v.Partitions[i].USDPerNodeHr = nil' '				_ = i'
# F2: plain-text Slurm reads and lab-locked partitions
mutate "f2 squeue user validated"       internal/backend/command.go 'func SqueueUserText(user string) (Command, error) {
	if err := ValidUser(user); err != nil {' 'func SqueueUserText(user string) (Command, error) {
	if err := ValidUser(user); err != nil && false {'
mutate "f2 sacct job validated"         internal/backend/command.go 'func SacctJobText(id string) (Command, error) {
	if err := ValidJobID(id); err != nil {' 'func SacctJobText(id string) (Command, error) {
	if err := ValidJobID(id); err != nil && false {'
mutate "f2 assoc user validated"        internal/backend/command.go 'func Assoc(user string) (Command, error) {
	if err := ValidUser(user); err != nil {' 'func Assoc(user string) (Command, error) {
	if err := ValidUser(user); err != nil && false {'
mutate "f2 job_ids uncached"            internal/backend/command.go 'squeueTextTemplate, "clusterwand", "-j", list}, kind: KindNoCache' 'squeueTextTemplate, "clusterwand", "-j", list}, kind: KindQueue'
mutate "f2 legacy sacct fields"         internal/backend/command.go 'sacct --helpformat | grep -qw StdOut || f=' 'true || f='
mutate "f2 summary restarts compat"     internal/backend/command.go "| sed 's/|/||/15'" "| cat"
mutate "f2 squeue row field count"      internal/slurm/text.go '		if len(f) != n {
			return nil, fmt.Errorf("squeue row' '		if len(f) < 0 {
			return nil, fmt.Errorf("squeue row'
mutate "f2 sacct name last"             internal/slurm/text.go '	if col["jobname"] != len(head)-1 {' '	if false {'
mutate "f2 sacct steps attach"          internal/slurm/text.go '			jobs[i].Steps = append(jobs[i].Steps, st)
			continue' '			_ = st
			continue'
mutate "f2 cancel by uid"               internal/slurm/text.go '			if by == get("UID") {' '			if true {'
mutate "f2 pattern relative"            internal/slurm/text.go '	if !strings.HasPrefix(p, "/") && j.WorkDir != "" {' '	if false {'
mutate "f2 pattern array task"          internal/slurm/text.go '			case "a":
				return num(task)' '			case "a":
				return num("0")'
mutate "f2 not responding"              internal/slurm/text.go 'out = append(out, strings.TrimSuffix(p, "*"), "NOT_RESPONDING")' 'out = append(out, strings.TrimSuffix(p, "*"))'
mutate "f2 reason by split"             internal/slurm/text.go '				n.Reason, n.ReasonSetBy, n.ReasonAt = m[1], m[2], numOf(m[3])' '				_ = m'
mutate "f2 ALL is unrestricted"         internal/slurm/text.go '	if s == "" || s == "ALL" {' '	if s == "" {'
mutate "f2 limit unlimited"             internal/slurm/text.go '			return Num{Infinite: true} // UNLIMITED, Partition_Limit' '			return Num{}'
mutate "f2 text is default"             internal/core/slurmread.go 'func (s *Service) useJSON() bool { return s.Site.Slurm.JSON }' 'func (s *Service) useJSON() bool { return true }'
mutate "f2 text script read"            internal/core/jobs.go '				j.Script = s.batchScript(ctx, fmt.Sprint(j.JobID))' '				_ = j'
mutate "f2 deny accounts"               internal/core/access.go '		if d == acct {
			return false' '		if d == acct {
			return true'
mutate "f2 allow accounts"              internal/core/access.go '		if x == acct {
			return true
		}
	}
	return false' '		if x == acct {
			return true
		}
	}
	return true'
mutate "f2 allow groups"                internal/core/access.go '		if !in {
			return fmt.Sprintf' '		if false {
			return fmt.Sprintf'
mutate "f2 assoc partition scope"       internal/core/access.go '		if (as.Partition == "" || as.Partition == part) && !seen[as.Account] {' '		if !seen[as.Account] {'
mutate "f2 named account held"          internal/core/access.go '	if account != "" && !contains(usable, account) {' '	if false {'
mutate "f2 unknown limits allow"        internal/core/access.go '	if a == nil {
		return "", ""
	}
	p, ok := a.parts[part]' '	if a == nil {
		return "locked", ""
	}
	p, ok := a.parts[part]'
mutate "f2 partitions hide locked"      internal/core/cluster.go '		if !acc.allowed(p.Name) {' '		if false {'
mutate "f2 script_check locked"         internal/core/cluster.go '			if why, fix := acc.check(strings.TrimSpace(member), sc.Request["account"]); why != "" {' '			if why, fix := acc.check(strings.TrimSpace(member), sc.Request["account"]); false && why != "" && fix != "" {'
mutate "f2 interactive locked"          internal/core/helpers.go '	if why, fix := s.partitionAccess(ctx).check(part, ""); why != "" {' '	if why, fix := s.partitionAccess(ctx).check(part, ""); false && why != "" && fix != "" {'
mutate "f2 fallback skips locked"       internal/core/a1.go '		if p != part && upIdle[p] && acc.allowed(p) {' '		if p != part && upIdle[p] && acc != nil {'
mutate "f2 invalidate text queue"       internal/core/a1.go 'strings.Contains(k, "exec squeue ") ||' 'false ||'
mutate "f3 draft catalog refused"       internal/core/service.go '	if cat.Approved != nil && !*cat.Approved {' '	if false {'
mutate "f3 local catalog only unset"    internal/core/service.go '	if s.Cfg.CatalogPath == "" && s.Site.CatalogFilePath() != "" {' '	if false {'
mutate "f3 host key format"             internal/site/site.go '		if err := ValidHostKey(k); err != nil {' '		if err := ValidHostKey(k); false \&\& err != nil {'
mutate "f3 catalog_file inside folder"  internal/site/site.go 'strings.HasPrefix(c, "../") ||' 'false ||'
mutate "f3 catalog_file not embedded"   internal/site/site.go '	if err == nil && sp.CatalogFile != "" {' '	if false {'
mutate "f3 pin strict"                  internal/backend/ssh.go '"-o", "StrictHostKeyChecking=yes",' '"-o", "StrictHostKeyChecking=accept-new",'
mutate "f3 pin no global file"          internal/backend/ssh.go '"-o", "GlobalKnownHostsFile=/dev/null",' ''
mutate "f3 pin key newline"             internal/backend/ssh.go '		if strings.ContainsAny(k, "\n\r\x00") || strings.Count(k, " ") != 1 {' '		if false {'
mutate "f3 pin site name"               internal/backend/ssh.go '	if strings.ContainsAny(site, "/\x00 \n") || site == "" || site == "." || site == ".." {' '	if site == "" {'
mutate "f3 new pins"                    internal/core/service.go '		if len(sp.HostKeys) > 0 {' '		if false {'
mutate "f3 draft unapproved"            internal/discover/discover.go '	b.WriteString("approved: false\n")' '	b.WriteString("approved: true\n")'
mutate "f3 catalog unapproved"          internal/discover/discover.go 'Generator: "clusterwand discover " + d.By, Approved: false,' 'Generator: "clusterwand discover " + d.By, Approved: true,'
mutate "f3 anon user"                   internal/discover/record.go '		if p.User != "" {' '		if false {'
mutate "f3 anon ip"                     internal/discover/record.go '		s = rePrivateIP.ReplaceAllString(s, "10.0.0.1")' ''
mutate "f3 anon accounts"               internal/discover/record.go '				if a == "ALL" || a == "(null)" || a == "N/A" {' '				if true {'
mutate "f3 anon config filter"          internal/discover/record.go 'Config: scrub(FilterConfig(p.Config))' 'Config: scrub(p.Config)'
mutate "f3 hostkeys host option"        internal/discover/hostkeys.go '	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\n\x00") || host == "" {' '	if host == "" {'
mutate "f3 cli host option"             cmd/clusterwand/discover.go '	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\n\x00") {' '	if false {'
mutate "f3 hostkeys valid key"          internal/discover/hostkeys.go '			if site.ValidHostKey(k) == nil && !seen[k] {' '			if !seen[k] {'
mutate "f3 cli no overwrite"            cmd/clusterwand/discover.go '			if _, err := os.Stat(f); err == nil {' '			if false {'
mutate "f3 cli approved kept"           cmd/clusterwand/discover.go '			if sp, err := site.Parse(b); err == nil && sp.Approved {' '			if false {'
mutate "f3 cli R1 only"                 cmd/clusterwand/discover.go '	cfg.Tiers = []string{"R1"}' '	cfg.Tiers = append(cfg.Tiers, "A1")'
mutate "f3 probe restricted"            internal/discover/discover.go '	open := func(p Partition) bool { return !p.Restricted && p.State == "up" }' '	open := func(p Partition) bool { return p.State == "up" }'
mutate "f4 results hidden listing"     internal/core/results.go '		if hiddenRel(parts[2]) {' '		if false {'
mutate "f4 results hidden download"    internal/core/results.go '		if hiddenRel(name) {' '		if false {'
mutate "f4 results hidden component"   internal/core/results.go '		if hiddenOrSensitive(part) {
			return true' '		if false {
			return true'
mutate "v051 array cost counts tasks"  internal/core/a1.go '	worst := round(price*float64(nodes)*share*float64(mins)/60*float64(tasks), 2)' '	worst := round(price*float64(nodes)*share*float64(mins)/60, 2)'
mutate "v051 array uncountable"        internal/core/a1.go '		if !ok {
			return nil, fmt.Errorf("--array=%s: cannot count' '		if false {
			return nil, fmt.Errorf("--array=%s: cannot count'
mutate "v051 array short flag"         internal/core/cluster.go '"A": "account", "a": "array"}' '"A": "account"}'
mutate "v051 array step guard"         internal/core/dbcheck.go '		if b < a || step < 1 {' '		if b < a {'
mutate "v051 hetjob refused"           internal/core/a1.go '	if req["hetjob"] != "" || req["packjob"] != "" {' '	if false {'
mutate "v051 script_check array"       internal/core/cluster.go '					c = round(c*float64(n), 2)' '					_ = n'
mutate "v051 results hidden job dir"   internal/core/results.go '			if hiddenRel(rel) {
				return fmt.Errorf' '			if false {
				return fmt.Errorf'
mutate "v051 results link roots"       internal/core/results.go '		if err := s.checkLogPath(real+"/x", d.User); err != nil {' '		if false {'
mutate "v051 results link hidden"      internal/core/results.go '		if err := s.checkJobDir(real, d.User); err != nil {' '		if false {'
mutate "v051 audit not stdout"         internal/policy/audit.go 'var auditStream io.Writer = os.Stderr' 'var auditStream io.Writer = os.Stdout'
# v0.6.0 cleanup: tool descriptions never promise cost the caller cannot see
mutate "v060 status desc cost gated"     internal/mcpserver/server.go '	if s.CanSeeCost() {' '	if true {'
mutate "v060 partitions desc gated"      internal/mcpserver/server.go '	if s.CanSeeCharge() {' '	if true {'
restore
for f in $FILES; do diff -q "$BK/$f" "$f" >/dev/null || { echo "NOT RESTORED: $f"; FAIL=1; }; done
exit $FAIL
