#!/usr/bin/env python3
"""Anonymize a plain-text Slurm recording into testdata/slurm-text/<version>/ (F2).

Input: a folder with squeue.txt, sacct.txt, nodes.txt, partitions.txt, assoc.txt
recorded read-only on a login node (formats as in internal/backend/command.go).
The real user, uid and lab account names are replaced, internal IPs masked and
node files trimmed. Usage: make_text_fixtures.py <raw dir> <version>
"""

import re
import sys
from pathlib import Path

RAW = Path(sys.argv[1])
OUT = Path(__file__).resolve().parent.parent / "testdata" / "slurm-text" / sys.argv[2]
FAKE, FAKE_UID = "alice_ucr_edu", "50001"


def identity() -> tuple[str, str]:
    row = (RAW / "sacct.txt").read_text().splitlines()[1].split("\x1f")
    head = (RAW / "sacct.txt").read_text().splitlines()[0].split("\x1f")
    d = dict(zip(head, row))
    return d["User"], d["UID"]


USER, UID = identity()
# lab accounts other than the caller's own become lab-a, lab-b ...
labs: dict[str, str] = {}


def scrub(text: str) -> str:
    text = text.replace(USER, FAKE).replace(UID, FAKE_UID)
    text = re.sub(r"\b10\.\d+\.\d+\.\d+\b", "10.0.0.1", text)

    def acct(m: re.Match) -> str:
        names = []
        for a in m.group(2).split(","):
            if a in ("ALL", "staff", "root"):
                names.append(a)
            else:
                labs.setdefault(a, "lab-" + "abcdefgh"[len(labs)])
                names.append(labs[a])
        return m.group(1) + ",".join(names)

    return re.sub(r"(AllowAccounts=|DenyAccounts=)(\S+)", acct, text)


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    for name in ("squeue.txt", "sacct.txt", "partitions.txt", "assoc.txt"):
        (OUT / name).write_text(scrub((RAW / name).read_text()))
    # one node per partition is enough to cover every format
    seen, keep = set(), []
    for line in (RAW / "nodes.txt").read_text().splitlines():
        m = re.search(r"Partitions=(\S+)", line)
        p = m.group(1) if m else ""
        if p not in seen:
            seen.add(p)
            keep.append(line)
    (OUT / "nodes.txt").write_text(scrub("\n".join(keep) + "\n"))
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
