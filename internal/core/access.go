package core

// F2: lab-locked partitions. A partition may be limited to some Slurm accounts
// (AllowAccounts / DenyAccounts) or Unix groups (AllowGroups); Slurm rejects a
// job sent there by anyone else ("Invalid account or account/partition
// combination"). clusterwand reads the limits live (`scontrol -o show partition`),
// the caller's associations (`sacctmgr show user <me> withassoc`) and groups
// (`id -Gn`), and then
//   - partitions lists only partitions the caller may use;
//   - script_check (and so job_submit) refuses a partition the caller, or the
//     account the script names, may not use, and says which account would work;
//   - interactive_help and the stockout fallback suggestions skip such partitions.
// When a read fails the limit is unknown: nothing is hidden or refused on a
// guess, and Slurm still enforces its own rule at submit time.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/slurm"
)

// access is who may use which partition, from the caller's point of view.
type access struct {
	parts    map[string]slurm.Partition
	accounts *slurm.Accounts // nil when sacctmgr could not be read
	groups   map[string]bool // nil when id -Gn could not be read
}

// partitionAccess reads partition limits, the caller's associations and groups.
// nil when the partition limits cannot be read (nothing is then restricted).
func (s *Service) partitionAccess(ctx context.Context) *access {
	b, err := s.run(ctx, backend.PartitionsText())
	if err != nil {
		return nil
	}
	ps, err := slurm.ParsePartitionsText(b)
	if err != nil {
		return nil
	}
	a := &access{parts: map[string]slurm.Partition{}}
	restricted := false
	for _, p := range ps {
		a.parts[p.Name] = p
		restricted = restricted || p.Restricted()
	}
	if !restricted {
		return a // nobody is locked out: skip the per-person reads
	}
	if me, err := s.User(ctx); err == nil {
		if c, err := backend.Assoc(me); err == nil {
			if b, err := s.run(ctx, c); err == nil {
				if acc, err := slurm.ParseAssoc(b); err == nil && len(acc.Assocs) > 0 {
					a.accounts = &acc
				}
			}
		}
	}
	if b, err := s.run(ctx, backend.Groups()); err == nil {
		a.groups = map[string]bool{}
		for _, g := range strings.Fields(string(b)) {
			a.groups[g] = true
		}
	}
	return a
}

// usableAccounts are the caller's accounts that may submit to part (an
// association naming another partition does not count).
func (a *access) usableAccounts(part string) []string {
	var out []string
	seen := map[string]bool{}
	for _, as := range a.accounts.Assocs {
		if (as.Partition == "" || as.Partition == part) && !seen[as.Account] {
			seen[as.Account] = true
			out = append(out, as.Account)
		}
	}
	return out
}

// accountAllowed applies AllowAccounts and DenyAccounts to one account.
func accountAllowed(p slurm.Partition, acct string) bool {
	for _, d := range p.DenyAccounts {
		if d == acct {
			return false
		}
	}
	if p.AllowAccounts == nil {
		return true
	}
	for _, x := range p.AllowAccounts {
		if x == acct {
			return true
		}
	}
	return false
}

// check says why the caller (with account, "" = their default) may not use
// part, or "" when they may or the limits are unknown. fix names an account
// that would work, when there is one.
func (a *access) check(part, account string) (why, fix string) {
	if a == nil {
		return "", ""
	}
	p, ok := a.parts[part]
	if !ok || !p.Restricted() {
		return "", ""
	}
	if p.AllowGroups != nil && a.groups != nil {
		in := false
		for _, g := range p.AllowGroups {
			in = in || a.groups[g]
		}
		if !in {
			return fmt.Sprintf("partition %s is limited to the groups %s and you are in none of them", part, strings.Join(p.AllowGroups, ",")), ""
		}
	}
	if (p.AllowAccounts == nil && p.DenyAccounts == nil) || a.accounts == nil {
		return "", ""
	}
	usable := a.usableAccounts(part)
	var good []string
	for _, x := range usable {
		if accountAllowed(p, x) {
			good = append(good, x)
		}
	}
	acct := account
	if acct == "" {
		acct = a.accounts.Default
	}
	if account != "" && !contains(usable, account) {
		why = fmt.Sprintf("you have no Slurm association with account %s on partition %s (yours: %s)", account, part, strings.Join(usable, ","))
	} else if !accountAllowed(p, acct) {
		limit := "accounts " + strings.Join(p.AllowAccounts, ",")
		if p.AllowAccounts == nil {
			limit = "every account but " + strings.Join(p.DenyAccounts, ",")
		}
		which := "your default account " + acct
		if account != "" {
			which = "account " + account
		}
		why = fmt.Sprintf("partition %s is limited to %s, and %s is not one of them", part, limit, which)
	}
	if why != "" && len(good) > 0 {
		fix = good[0]
	}
	return why, fix
}

// allowed reports whether the caller may use part with their default account
// or any other account they hold (partitions list, suggestions).
func (a *access) allowed(part string) bool {
	if why, fix := a.check(part, ""); why != "" && fix == "" {
		return false
	}
	return true
}

// lockedNote is the script_check message for a partition the caller may not use.
func lockedNote(why, fix string) string {
	if fix != "" {
		return why + "; Slurm would reject the job. Add #SBATCH --account=" + fix + " (an account of yours that may use it)"
	}
	return why + "; Slurm would reject the job (Invalid account or account/partition combination). Pick another partition"
}

// restrictedTo describes a partition's limit for the partitions list ("" = open).
func (a *access) restrictedTo(part string) string {
	if a == nil {
		return ""
	}
	p, ok := a.parts[part]
	if !ok || !p.Restricted() {
		return ""
	}
	var parts []string
	if p.AllowAccounts != nil {
		parts = append(parts, "accounts "+strings.Join(p.AllowAccounts, ","))
	}
	if p.DenyAccounts != nil {
		parts = append(parts, "not accounts "+strings.Join(p.DenyAccounts, ","))
	}
	if p.AllowGroups != nil {
		parts = append(parts, "groups "+strings.Join(p.AllowGroups, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
