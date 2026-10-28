# sites/ursa-major

Ursa Major (UCR Research Computing, on Google Cloud) is ClusterWand's first site and the
cluster ursa-bifrost was built for. Everything here is specific to that site and is not
part of the `clusterwand` binary.

| Path | What | Status |
|---|---|---|
| `deploy/` | Cloud Run deploy for a hosted `clusterwand serve` (Google sign-in, per-user IAP); renamed from ursa-bifrost's `deploy/` | example; never run from ClusterWand |
| `deploy/DEPLOY.md` | ursa-bifrost's deploy guide, kept for reference | historical |
| `agent/` | ursa-agent, the read-only web dashboard (Python), tested in CI (`agent` job) | carried over unchanged; live copy deploys from ursa-bifrost |

What moved and why (F0, v0.1.0):

- `deploy/` and `docs/DEPLOY.md` moved here from the repo root: they are Ursa Major's Cloud
  Run project, secrets and buckets, not something every site needs.
- `agent/` moved here; its code still points at the live ursa-bifrost server by default,
  because that is the service it serves. Its deploy script was removed: the live dashboard
  is deployed from ursa-bifrost only.
- `scripts/onboard_lab.py` was removed: it edits the live ursa-bifrost users file and Cloud
  Run service, which ClusterWand must never touch.

Coming in F1 (v0.2.0): `site.yaml` here holds every Ursa constant (partitions and roles,
paths, the `check` partition, the $/node-hour model, the catalog pointer) that is still
compiled into `internal/` today.
