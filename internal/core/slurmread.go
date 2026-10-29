package core

// F2: one place that reads the scheduler. Plain-text formats (squeue -O,
// sacct -P, scontrol -o) are the primary path and read the same on Slurm 20.11
// to 25.11; --json is used only when the site profile sets slurm.json. Both
// paths fill the same slurm structs, so the operations above never know which
// one ran.

import (
	"context"
	"strings"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

// useJSON reports whether this site reads Slurm with --json.
func (s *Service) useJSON() bool { return s.Site.Slurm.JSON }

// queueSel picks queue rows: every job, one user's, one job, or a list of jobs.
type queueSel struct {
	all  bool
	user string
	job  string
	jobs []string
}

// queueCmd is the command for a queue selection on this site's path.
func (s *Service) queueCmd(sel queueSel) (backend.Command, error) {
	j := s.useJSON()
	switch {
	case len(sel.jobs) > 0:
		if j {
			return backend.SqueueJobs(sel.jobs)
		}
		return backend.SqueueJobsText(sel.jobs)
	case sel.job != "":
		if j {
			return backend.SqueueJob(sel.job)
		}
		return backend.SqueueJobText(sel.job)
	case sel.user != "":
		if j {
			return backend.SqueueUser(sel.user)
		}
		return backend.SqueueUserText(sel.user)
	}
	if j {
		return backend.SqueueAll(), nil
	}
	return backend.SqueueAllText(), nil
}

// queue reads queue rows.
func (s *Service) queue(ctx context.Context, sel queueSel) ([]slurm.QueueJob, error) {
	c, err := s.queueCmd(sel)
	if err != nil {
		return nil, err
	}
	b, err := s.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if s.useJSON() {
		var q slurm.QueueResponse
		if err := slurm.Decode(b, &q); err != nil {
			return nil, err
		}
		return q.Jobs, nil
	}
	return slurm.ParseSqueueText(b)
}

// acctJobs reads full accounting records (steps, paths, submit line) of one
// job or a list of jobs. List-type reads use acctRows (the summary) instead.
func (s *Service) acctJobs(ctx context.Context, ids []string, cached bool) ([]slurm.AcctJob, error) {
	var c backend.Command
	var err error
	switch {
	case s.useJSON() && cached && len(ids) == 1:
		c, err = backend.SacctJob(ids[0])
	case s.useJSON():
		c, err = backend.SacctJobs(ids)
	case cached && len(ids) == 1:
		c, err = backend.SacctJobText(ids[0])
	default:
		c, err = backend.SacctJobsText(ids)
	}
	if err != nil {
		return nil, err
	}
	b, err := s.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if s.useJSON() {
		var a slurm.AcctResponse
		if err := slurm.Decode(b, &a); err != nil {
			return nil, err
		}
		return a.Jobs, nil
	}
	return slurm.ParseSacctText(b)
}

// batchScript is a job's stored script on the text path (--json carries it in
// the record). "" when the cluster stores none or the read fails.
func (s *Service) batchScript(ctx context.Context, id string) string {
	c, err := backend.SacctScript(id)
	if err != nil {
		return ""
	}
	b, err := s.run(ctx, c)
	if err != nil {
		return ""
	}
	return slurm.ParseBatchScript(b)
}

// nodes reads every node and the Slurm release.
func (s *Service) nodes(ctx context.Context) ([]slurm.Node, string, error) {
	if s.useJSON() {
		b, err := s.run(ctx, backend.Nodes())
		if err != nil {
			return nil, "", err
		}
		var nr slurm.NodesResponse
		if err := slurm.Decode(b, &nr); err != nil {
			return nil, "", err
		}
		return nr.Nodes, nr.Meta.Slurm.Release, nil
	}
	b, err := s.run(ctx, backend.NodesText())
	if err != nil {
		return nil, "", err
	}
	first, rest, _ := strings.Cut(string(b), "\n")
	release := ""
	if strings.HasPrefix(first, "slurm ") {
		release = strings.TrimSpace(strings.TrimPrefix(first, "slurm "))
	} else {
		rest = string(b) // no version line
	}
	ns, err := slurm.ParseNodesText([]byte(rest))
	if err != nil {
		return nil, "", err
	}
	if release == "" {
		release = slurm.NodesVersion(ns)
	}
	return ns, release, nil
}
