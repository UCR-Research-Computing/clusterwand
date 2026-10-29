package discover

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/UCR-Research-Computing/clusterwand/internal/site"
)

// Diff is one difference between a discovered draft and a reviewed profile.
type Diff struct {
	Field     string `json:"field"`
	Draft     string `json:"draft"`
	Reviewed  string `json:"reviewed"`
	Same      bool   `json:"same"`
	Note      string `json:"note,omitempty"`
	Inferable bool   `json:"inferable"` // discover tries to find it (false: by hand only)
}

// handCatalog is the part of a published catalog the comparison reads.
type handCatalog struct {
	Partitions []Partition `json:"partitions"`
	Modules    struct {
		Core         []string `json:"core"`
		MPIDependent map[string]struct {
			Modules []string `json:"modules"`
		} `json:"mpi_dependent"`
	} `json:"modules"`
}

// Compare sets a draft next to a reviewed profile and, when given, the
// catalog that profile uses (raw JSON; nil to skip).
func Compare(d *Draft, hand *site.Profile, catalog []byte) ([]Diff, error) {
	var out []Diff
	add := func(field, draft, reviewed string, inferable bool, note string) {
		out = append(out, Diff{Field: field, Draft: draft, Reviewed: reviewed, Same: draft == reviewed, Inferable: inferable, Note: note})
	}
	hp := hand.Partitions
	add("paths.home", d.Home, hand.Paths.Home, true, "")
	add("paths.scratch", d.Scratch, hand.Paths.Scratch, true, "")
	add("cloud_nodes", fmt.Sprint(d.CloudNodes), fmt.Sprint(hand.CloudNodes), true, "nodes with State CLOUD")
	add("partitions.probe", d.Probe, hp.Probe, true, "shortest open partition with a limit of 1 hour or less")
	add("partitions.general", d.General, hp.General, true, "the Default=YES partition")
	add("partitions.gpu", d.GPU, hp.GPU, true, "open partition with GPUs, most nodes")
	add("partitions.highmem", d.HighMem, hp.HighMem, true, "open partition with the most memory per node (>= 1.5x general)")
	add("partitions.avx2_only", strings.Join(d.AVX2Only, ","), strings.Join(hp.AVX2Only, ","), true, "nodes advertising avx2 but no avx512 feature")
	add("modules.python", strings.Join(d.Python, ","), strings.Join(hand.Modules.Python, ","), true, "python*/conda module names")
	add("slurm.json", "false", fmt.Sprint(hand.Slurm.JSON), true, "text is always the default")
	add("charge.model", site.ChargeNone, hand.Model(), false, "prices are not discoverable")
	add("partitions.fast", "", hp.Fast, false, "")
	add("partitions.fallback_idle", "", strings.Join(hp.FallbackIdle, ","), false, "")
	add("partitions.fallback_cold", "", strings.Join(hp.FallbackCold, ","), false, "")
	add("partitions.cpu notes", "", fmt.Sprintf("%d notes", len(hp.CPU)), false, "")
	add("stockout_reasons", "", strings.Join(hand.Stockout, ","), false, "")
	add("help", "", hand.Help, false, "")
	add("text (site wording)", "", fmt.Sprintf("%d keys", len(hand.Texts)), false, "")
	switch {
	case hand.Catalog != "":
		add("catalog", "catalog_file: catalog.json (draft)", hand.Catalog, false, "the site publishes its own catalog")
	case hand.CatalogFile != "":
		add("catalog", "catalog_file: catalog.json (draft)", "catalog_file: "+hand.CatalogFile, false, "")
	}
	if catalog == nil {
		return out, nil
	}
	var hc handCatalog
	if err := json.Unmarshal(catalog, &hc); err != nil {
		return nil, fmt.Errorf("reviewed catalog: %w", err)
	}
	byName := map[string]Partition{}
	for _, p := range hc.Partitions {
		byName[p.Name] = p
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range d.Partitions {
		names = append(names, p.Name)
		seen[p.Name] = true
	}
	for _, p := range hc.Partitions {
		if !seen[p.Name] {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		dp, inDraft := d.part(n)
		hp, inHand := byName[n]
		switch {
		case !inHand:
			add("catalog partition "+n, "present", "missing", true, "in Slurm but not in the reviewed catalog")
			continue
		case !inDraft:
			add("catalog partition "+n, "missing", "present", true, "in the reviewed catalog but not in Slurm")
			continue
		}
		f := func(field, a, b string) { add("catalog "+n+"."+field, a, b, true, "") }
		f("default", fmt.Sprint(dp.Default), fmt.Sprint(hp.Default))
		f("cpus_per_node", fmt.Sprint(dp.CPUsPerNode), fmt.Sprint(hp.CPUsPerNode))
		f("mem_gb_per_node", fmt.Sprint(dp.MemGBPerNode), fmt.Sprint(hp.MemGBPerNode))
		f("gpus_per_node", fmt.Sprint(dp.GPUsPerNode), fmt.Sprint(hp.GPUsPerNode))
		f("max_nodes", fmt.Sprint(dp.MaxNodes), fmt.Sprint(hp.MaxNodes))
		f("time_limit", normTime(dp.TimeLimit), normTime(hp.TimeLimit))
		f("spot", fmt.Sprint(dp.Spot), fmt.Sprint(hp.Spot))
		f("gpu_type", deref(dp.GPUType), deref(hp.GPUType))
	}
	hand2 := map[string]bool{}
	for _, m := range hc.Modules.Core {
		hand2[m] = true
	}
	for _, g := range hc.Modules.MPIDependent {
		for _, m := range g.Modules {
			hand2[m] = true
		}
	}
	both := 0
	for _, m := range d.Modules {
		if hand2[m] {
			both++
		}
	}
	add("catalog modules", fmt.Sprintf("%d (module -t avail)", len(d.Modules)), fmt.Sprintf("%d (core + MPI-dependent)", len(hand2)), true,
		fmt.Sprintf("%d in both", both))
	return out, nil
}

// normTime prints a time limit in minutes so "15:00" and "00:15:00" compare equal.
func normTime(s string) string {
	_, m := timeLimit(s)
	if m < 0 {
		return "infinite"
	}
	return fmt.Sprintf("%d min", m)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
