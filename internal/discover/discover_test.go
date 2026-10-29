package discover

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UCR-Research-Computing/clusterwand/internal/site"
)

func recorded(t *testing.T) Probe {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "discover", "ursa-25.11")
	p, err := FromFiles(func(n string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, n))
		return string(b), err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Discover on the recorded Ursa Major probe finds the same roles and paths
// as the hand-written profile (sites/ursa-major) wherever it infers them.
func TestBuildMatchesHandProfile(t *testing.T) {
	d, err := Build("ursa-test", recorded(t), "2026-10-29", "test")
	if err != nil {
		t.Fatal(err)
	}
	hand, err := site.Embedded("ursa-major")
	if err != nil {
		t.Fatal(err)
	}
	diffs, err := Compare(d, hand, nil)
	if err != nil {
		t.Fatal(err)
	}
	must := map[string]bool{"paths.home": true, "paths.scratch": true, "cloud_nodes": true, "partitions.probe": true,
		"partitions.general": true, "partitions.gpu": true, "partitions.highmem": true, "slurm.json": true}
	for _, c := range diffs {
		if must[c.Field] && !c.Same {
			t.Errorf("%s: draft %q, hand %q", c.Field, c.Draft, c.Reviewed)
		}
		delete(must, c.Field)
	}
	if len(must) > 0 {
		t.Errorf("not compared: %v", must)
	}
	if d.Slurm != "25.11.4" || len(d.Partitions) != 13 || len(d.Modules) < 400 {
		t.Errorf("draft: slurm %s, %d partitions, %d modules", d.Slurm, len(d.Partitions), len(d.Modules))
	}
	for _, p := range d.Partitions {
		if p.Name == "amd112" && (!p.Restricted || p.CPUsPerNode != 56 || p.MemGBPerNode != 434 || p.TimeLimit != "30-00:00:00") {
			t.Errorf("amd112: %+v", p)
		}
		if p.Name == "gpul4" && p.GPUsPerNode != 1 {
			t.Errorf("gpul4: %+v", p)
		}
		if p.Name == "standard" && (p.Restricted || !p.Default) {
			t.Errorf("standard: %+v", p)
		}
	}
}

// The draft is refused until approved; once approved it parses as a profile.
func TestDraftNeedsApproval(t *testing.T) {
	d, err := Build("ursa-test", recorded(t), "2026-10-29", "test")
	if err != nil {
		t.Fatal(err)
	}
	d.HostKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}
	y := d.ProfileYAML()
	if _, err := site.Parse(y); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("draft profile accepted: %v", err)
	}
	ok := strings.Replace(string(y), "approved: false", "approved: true", 1)
	sp, err := site.Parse([]byte(ok))
	if err != nil {
		t.Fatalf("approved draft refused: %v\n%s", err, ok)
	}
	if sp.Partitions.Probe != "check" || sp.CatalogFile != "catalog.json" || len(sp.HostKeys) != 1 || sp.Discovered == nil || sp.Model() != site.ChargeNone {
		t.Errorf("approved draft: %+v", sp)
	}
	if !strings.Contains(string(d.CatalogJSON()), `"approved": false`) {
		t.Error("draft catalog is not marked approved: false")
	}
}

func TestBuildRefusesGarbage(t *testing.T) {
	p := recorded(t)
	p.Version = "bash: sinfo: command not found"
	if _, err := Build("x", p, "", ""); err == nil {
		t.Error("non-Slurm version accepted")
	}
	p = recorded(t)
	p.Partitions = ""
	if _, err := Build("x", p, "", ""); err == nil {
		t.Error("no partitions accepted")
	}
	p = recorded(t)
	p.Nodes = "slurm 25.11.4\nGarbage=1\n"
	if _, err := Build("x", p, "", ""); err == nil {
		t.Error("node line without NodeName accepted")
	}
}

func TestUserDir(t *testing.T) {
	for in, want := range map[string]string{"/home/bob": "/home/{user}", "/rhome/bob": "/rhome/{user}", "/bob/bob": "", "/home/alice": "", "relative/bob": ""} {
		if got := userDir(in, "bob"); got != want {
			t.Errorf("userDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnonymize(t *testing.T) {
	p := Probe{User: "jdoe_x", Host: "real-host",
		Partitions: "PartitionName=a AllowAccounts=staff,smithlab AllowGroups=ALL DenyAccounts=jdoe_x\n",
		Nodes:      "NodeName=n1 NodeAddr=10.36.1.2 Reason=bad [root@1793000000] Owner=jdoe_x Partitions=a\n",
		Env:        "home\t/home/jdoe_x\n", Config: "ClusterName = c\nSlurmctldHost = ctl(10.1.2.3)\n"}
	a := Anonymize(p)
	all := strings.Join([]string{a.Partitions, a.Nodes, a.Env, a.Config, a.Host}, "\n")
	for _, bad := range []string{"jdoe_x", "smithlab", "staff", "10.36.1.2", "root@", "real-host", "SlurmctldHost", "10.1.2.3"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q survived anonymizing:\n%s", bad, all)
		}
	}
	if !strings.Contains(a.Partitions, "AllowGroups=ALL") || !strings.Contains(a.Env, "/home/"+FakeUser) || a.User != FakeUser {
		t.Errorf("anonymized too much: %+v", a)
	}
}

func TestHostKeys(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	var asked []string
	run := func(name string, args ...string) ([]byte, error) {
		asked = append(asked, name+" "+strings.Join(args, " "))
		switch name {
		case "ssh":
			return []byte("hostname 1.2.3.4\nport 2222\nhostkeyalias node-alias\nuserknownhostsfile /k1 /k2\nglobalknownhostsfile /dev/null\n"), nil
		case "ssh-keygen":
			if args[1] != "node-alias" {
				return nil, errors.New("wrong name")
			}
			if args[3] == "/k1" {
				return []byte("# Host node-alias found: line 3\nnode-alias " + key + "\n@revoked node-alias ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
			}
			return []byte("node-alias " + key + "\nnode-alias ssh-rsa notbase64!\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	keys, err := HostKeys(run, "ursa")
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("keys %v, err %v (asked %v)", keys, err, asked)
	}
	for _, bad := range []string{"-oProxyCommand=x", "a b", ""} {
		if _, err := HostKeys(run, bad); err == nil {
			t.Errorf("host %q accepted", bad)
		}
	}
	none := func(name string, args ...string) ([]byte, error) {
		if name == "ssh" {
			return []byte("hostname h\nuserknownhostsfile /k\n"), nil
		}
		return nil, errors.New("exit 1")
	}
	if _, err := HostKeys(none, "h"); err == nil {
		t.Error("no trusted key: accepted")
	}
}

// Roles are only given to partitions everyone may use.
func TestRolesSkipLockedPartitions(t *testing.T) {
	p := Probe{User: "bob", Version: "slurm 23.11.1",
		Partitions: "PartitionName=labq AllowAccounts=lab1 MaxTime=00:30:00 TotalNodes=9 State=UP\n" +
			"PartitionName=open MaxTime=00:45:00 TotalNodes=1 State=UP Default=YES\n" +
			"PartitionName=labgpu AllowGroups=g1 MaxTime=1-00:00:00 TotalNodes=9 State=UP\n" +
			"PartitionName=gpu MaxTime=1-00:00:00 TotalNodes=1 State=UP\n",
		Nodes: "NodeName=a CPUTot=4 RealMemory=8000 Gres=gpu:a100:2 State=IDLE Partitions=labgpu,gpu\nNodeName=b CPUTot=4 RealMemory=8000 State=IDLE Partitions=labq,open\n"}
	d, err := Build("t", p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Probe != "open" || d.GPU != "gpu" || d.General != "open" {
		t.Errorf("probe %q gpu %q general %q", d.Probe, d.GPU, d.General)
	}
	if g, _ := d.part("gpu"); g.GPUsPerNode != 2 || g.GPUType == nil || *g.GPUType != "a100" {
		t.Errorf("gpu partition: %+v", g)
	}
}
