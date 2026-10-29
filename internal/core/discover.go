package core

import (
	"context"
	"fmt"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/discover"
)

// DiscoverProbe runs discover's read-only probes on the login node (F3):
// scheduler queries, the caller's environment and the module list, nothing
// on a compute node and nothing written. Run it through Call (tool
// "discover", tier R1) so it is audited like every other read.
func (s *Service) DiscoverProbe(ctx context.Context) (*discover.Probe, error) {
	u, err := s.User(ctx)
	if err != nil {
		return nil, err
	}
	p := &discover.Probe{Host: s.Cfg.SSH.Host, User: u}
	steps := []struct {
		name string
		c    backend.Command
		dst  *string
	}{
		{"sinfo --version", backend.SlurmVersion(), &p.Version},
		{"scontrol -o show partition", backend.PartitionsText(), &p.Partitions},
		{"scontrol -o show nodes", backend.NodesText(), &p.Nodes},
		{"scontrol show config", backend.ShowConfig(), &p.Config},
		{"environment", backend.DiscoverEnv(), &p.Env},
		{"module -t avail", backend.ModuleAvail(), &p.Modules},
	}
	for _, st := range steps {
		b, err := s.run(ctx, st.c)
		if err != nil {
			return nil, fmt.Errorf("discover: %s: %w", st.name, err)
		}
		*st.dst = string(b)
	}
	p.Config = discover.FilterConfig(p.Config)
	return p, nil
}
