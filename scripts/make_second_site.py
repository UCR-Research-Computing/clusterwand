#!/usr/bin/env python3
"""Build the synthetic second-site fixtures (testdata/sites/bigcluster) from the Ursa ones.

Every site-specific string in the recorded Ursa Major fixtures is rewritten to a
made-up on-premises cluster ("Big Cluster" at Example University): partition names,
node names, user, home and scratch roots, catalog paths, cloud and vendor words. The
result proves, through TestGoldenSecondSite, that no Ursa string comes from the code:
any that remains in an answer must have leaked from Go, not from the fixtures.

Run from the repo root: python3 scripts/make_second_site.py
"""

import json
import pathlib
import re
import shutil

SRC = pathlib.Path("testdata")
OUT = pathlib.Path("testdata/sites/bigcluster")

# Longest first so prefixes do not clobber longer names.
SUBS = [
    ("ucr-ursa-major-hpc-cluster", "example-project"),
    ("us-central1-a", "zone-a"),
    ("us-central1", "region-1"),
    ("alice_ucr_edu", "bob42"),
    ("ucrslurmcl-stdnodeset-", "bc-cpu-"),
    ("ucrslurmcl-spotnodeset-", "bc-pre-"),
    ("ucrslurmcl-c3nodeset-", "bc-fast-"),
    ("ucrslurmcl-gpunodeset-", "bc-gpu-"),
    ("ucrslurmcl-highmemnodeset-", "bc-bigmem-"),
    ("ucrslurmcl-nvmenodeset-", "bc-io-"),
    ("ucrslurmcl-checknodeset-", "bc-debug-"),
    ("ucrslurmcl", "bigcluster"),
    ("stdnodeset", "cpunodes"),
    ("spotnodeset", "prenodes"),
    ("c3nodeset", "fastnodes"),
    ("gpunodeset", "gpunodes"),
    ("highmemnodeset", "bigmemnodes"),
    ("nvmenodeset", "ionodes"),
    ("deep-research-lab", "projects"),
    ("ursa-major-hpc", "my-analysis"),
    ("ursa_conda_libs", "site_conda_libs"),
    ("URSA_CONTAINERS", "SITE_CONTAINERS"),
    ("[ursa] job", "[site] job"),
    ("ENV=ucr ", "ENV=site "),
    ("ursa-selftest", "site-selftest"),
    ("ursa-catalog (ucr-slurm-production repo)", "catalog builder"),
    ("ursa-catalog/1", "clusterwand-catalog/1"),
    ("ucr-stack", "site-stack"),
    ("/apps/docs/templates/job-header.sh", "/opt/site/job-header.sh"),
    ("/apps/containers/", "/opt/containers/"),
    ("/apps/", "/opt/"),
    ("/data/shared", "/refdata"),
    ("/scratch/", "/work/"),
    ("/home/", "/users/"),
    ("Google Cloud", "on premises"),
    ("Google", "the owner"),
    ("GCP", "the provider"),
    ("UCR Research Computing", "Example University HPC"),
    ("UCR Ursa Major", "Example University"),
    ("Ursa Major", "Big Cluster"),
    ("UCR", "EXU"),
    ("Sapphire Rapids", "fast cores"),
    ("NVIDIA L4 24 GB", "NVIDIA A100 80 GB"),
    ("NVIDIA L4", "NVIDIA A100"),
    ("c3-highcpu-44", "fast node"),
    ("n2-highmem-64", "big-memory node"),
    ("c2-standard-16", "io node"),
    ("c2d-standard-32", "cpu node"),
    ("e2-standard", "cpu-node"),
    ("Rocky Linux 8.10", "the node OS"),
    ("Rocky Linux 8", "the node OS"),
    ("Rocky 8", "the node OS"),
    ("maintained by Research Computing", "maintained by the HPC team"),
    ("GPU-verified on L4", "GPU-verified on A100"),
]

# Partition names (whole words only).
PARTS = {
    "computehigh": "fast",
    "gpul4": "gpu",
    "highmem": "bigmem",
    "nvmescratch": "io",
    "spot": "preempt",
    "standard": "cpu",
    "check": "debug",
}
WORD = re.compile(r"\b(" + "|".join(PARTS) + r")\b")

# Words that must never appear in the synthetic fixtures (checked at the end).
FORBIDDEN = ["ursa", "ucr", "google", "gcp", "rocky 8", "rocky linux", "research computing", " l4", "computehigh", "gpul4", "nvmescratch", "/scratch/", "/data/shared", "/apps/", "slurm-gcp", "alice"]


def rewrite(s: str) -> str:
    for a, b in SUBS:
        s = s.replace(a, b)
    return WORD.sub(lambda m: PARTS[m.group(1)], s)


def main() -> None:
    if OUT.exists():
        shutil.rmtree(OUT)
    (OUT / "logs").mkdir(parents=True)
    for p in sorted(SRC.glob("*")):
        if p.is_file():
            (OUT / p.name).write_text(rewrite(p.read_text()))
    for p in sorted((SRC / "logs").glob("*")):
        (OUT / "logs" / p.name).write_text(rewrite(p.read_text()))
    # the made-up site prices nothing: drop catalog dollar figures and mark it
    cat = json.loads((OUT / "catalog.json").read_text())
    for part in cat["partitions"]:
        part["usd_per_node_hour"] = None
    cat["cluster"] = "Big Cluster (bigcluster), Example University HPC, on premises"
    (OUT / "catalog.json").write_text(json.dumps(cat, indent=1) + "\n")
    bad = []
    for p in OUT.rglob("*"):
        if p.is_file():
            low = p.read_text().lower()
            bad += [f"{p}: {w}" for w in FORBIDDEN if w in low]
    if bad:
        raise SystemExit("forbidden strings left:\n" + "\n".join(bad))
    print("wrote", sorted(str(p.relative_to(OUT)) for p in OUT.rglob("*") if p.is_file()))


if __name__ == "__main__":
    main()
