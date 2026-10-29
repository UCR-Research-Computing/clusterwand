# sites/ursa-major

Ursa Major (UCR Research Computing, on Google Cloud) is ClusterWand's first site and the
cluster ursa-bifrost was built for. Everything here is specific to that site and is not
part of the `clusterwand` binary.

ClusterWand treats Ursa Major like any other cluster: a normal researcher SSH login and
nothing installed or changed on the cluster. On Ursa the network path to the login node is
an IAP tunnel set up by the user's own `~/.ssh/config` (`ProxyCommand`), which ClusterWand
neither knows about nor depends on; see [docs/SSH.md](../../docs/SSH.md).

What changed in F0 (v0.1.0):

- ursa-bifrost's hosted parts are not in ClusterWand (SSH only): the Cloud Run deploy
  (`deploy/`, `DEPLOY.md`), the ursa-agent dashboard (`agent/`), the hosted `serve`
  command and its Google sign-in, the IAP/OS Login backend and Cloud Storage staging. They
  live on, unchanged, in ursa-bifrost.
- `scripts/onboard_lab.py` was removed: it edits the live ursa-bifrost users file and Cloud
  Run service, which ClusterWand must never touch.

F1 (v0.2.0): [`site.yaml`](site.yaml) holds every Ursa constant that used to be compiled
into `internal/`: partition roles and fallbacks, AVX2/e2 advice, paths (`/home`, `/scratch`,
`/apps/docs/catalog.json`, `/data/shared` wording), the `check` probe partition, the
$/node-hour charge model, cloud-node wording and stock-out reasons. Select it with
`site: ursa-major` in `~/.config/clusterwand/config.yaml`. `TestGoldenUrsaMajor` checks that
every tool answers byte-identically to ursa-bifrost v0.10.1 on the recorded fixtures with
this profile loaded. Partition limits (MaxTime, SuspendTime) come from Slurm and the
catalog at run time; the values checked live on 2026-10-28 are in the file's header.

F3 (v0.4.0): `clusterwand discover --site ursa-discovered --host ursa-login --compare
ursa-major` drafts a profile from the login node alone. On the recorded probe
(`testdata/discover/ursa-25.11`) it finds every role it tries to infer exactly as this
hand-written profile has them: home, scratch, cloud nodes, probe `check`, general
`standard`, GPU `gpul4`, high-memory `highmem`. What only a person can add here: the
`$/node-hour` charge model and the published catalog, `fast`, the stockout fallbacks, CPU
notes and AVX2-only partitions (the nodes advertise no CPU features), stockout reasons,
help desk name and the 65 wording keys. Details: docs/SPEC.md section 27.
