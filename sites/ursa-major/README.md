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

Coming in F1 (v0.2.0): `site.yaml` here holds every Ursa constant (partitions and roles,
paths, the `check` partition, the $/node-hour model, the catalog pointer) that is still
compiled into `internal/` today.
