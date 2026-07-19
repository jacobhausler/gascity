# Patch manifest — patched local `gc` candidate `ra.1`

Per moiraine's manifest ruling (2026-07-19 14:1xZ, on `ra-ol9b7`): the candidate carries
a committed manifest enumerating every local patch in the build, **plus an explicit
DEFERRED list**, so that exclusion is always a statement and never a silence. siuan's
adoption gate cross-checks this file against the branches registered on `ra-ol9b7`; an
unlisted registered branch FAILS verification.

Assembled by perrin, 2026-07-19. This file is local to our patched builds and must
**never** be included in an upstream PR.

---

## Candidate identity

| Field | Value |
|---|---|
| Candidate | `ra.1` |
| Branch | `candidate/ra.1` |
| Upstream base | `4169ba4a2` (`fix(doctor): bound each check with a per-check timeout (#3448)`) |
| Base is upstream `main` tip | yes, at assembly time |
| Local patches applied | 1 (below) |
| Version stamp | `1.3.5+ra.1` — deliberately NOT "1.3.5" |

The version stamp exists because the binary this candidate replaces reports a bare
`1.3.5` while being a dirty local build (`ra-ufk9t`, and the `brew upgrade` invariant in
CLAUDE.md). `+ra.1` cannot be mistaken for stock, and the embedded `vcs.revision` anchors
it to a real commit.

---

## INCLUDED — local patches in this build

### 1. `4c04d4d00` — `fix/bd-backup-freshness-dolt-state`

- **In candidate as:** `63485d626` (cherry-picked onto the tip; identical tree)
- **Bead:** `ra-m5y6t` (closed, fixed+verified) · upstream filing `ra-7ir1i`
- **Files:** `internal/doctor/checks_bd_backup_freshness.go` (+ `_test.go`)
- **What it changes:** `gc doctor`'s `bd-backup-freshness` check read
  `.beads/backup/backup_state.json` unconditionally. On a scope that has migrated to a
  Dolt backup destination, that file is frozen and a successful `bd backup sync` never
  writes it — so the check emitted a warning no operator action could clear, and its
  frozen Dolt commit pointed at an abandoned store (a restore trap). The check now reads
  `.beads/dolt-backup-state.json` when `.beads/dolt-backup.json` registers a destination,
  falls back to the legacy file only for never-migrated scopes, warns on a registered
  destination that has never synced (previously passed silently), and names the store in
  every finding.
- **Verification:** full `./internal/doctor/` suite green; falsifiable floor met (reverting
  only the fix fails 4 subtests, the first reproducing the live bug); live: stock WARNs
  178h27m unclearably vs patched OK; stale state still WARNs; a real `bd backup sync`
  turns doctor GREEN. Evidence in `ra-m5y6t` notes.

---

## ⚠ DISCLOSURE TO THE ADOPTION GATE — read before judging §2 and §4

The gate's baseline (`baseline-doctor-20260719T1310Z.txt`) lists `bd-backup-freshness`
(scope `/Users/home/workspace`, 177h39m) as a **pre-existing WARN**. On this candidate that
warning **disappears**.

PROTOCOL.md §2 says a pre-existing finding that silently disappears is *investigated, not
celebrated*, since a check that stopped running looks exactly like a check that started
passing. That rule is correct and I am not asking for an exemption — I am supplying the
investigation up front:

- The disappearance is **caused by patch 1 above**, deliberately, and is the entire point
  of that patch. The check still runs and is still able to fail.
- Proof it can still fail, on this candidate: set
  `.beads/dolt-backup-state.json` `last_sync` to an old timestamp → the check WARNs
  (observed: `dolt backup: last sync was 445h57m0s ago`). Restore the file and it returns
  to OK. That is a two-command falsification any verifier can repeat.
- Expected §2 delta: **passed count +1, warning count −1**, attributable solely to this
  patch. No baseline-✓ check should regress.

**And the trap worth stating plainly, because it cuts against my own patch:**
PROTOCOL.md §4 requires a *restore-proven* backup for every affected scope, and calls it
the item most likely to block the switch. This patch turns the backup-freshness indicator
**green**. Green here means only "the live Dolt backup synced recently" — it is **not**
evidence of a restore, and it does **not** discharge §4. Do not let a green
`bd-backup-freshness` on this candidate be read as §4 satisfied; §4 is untouched by this
build and still owed. A patch that makes an instrument green while the underlying bar is
unmet is exactly the failure mode this city keeps finding, so it is flagged here rather
than left to be discovered at the gate.

---

## DEFERRED — registered or known work NOT in this build

Nothing below is in the candidate. Listed so exclusion is a statement, not a silence.

| Item | Bead | Why not included |
|---|---|---|
| Storehealth `LastMaintenance` scan bound (gascity **#4418**) | `ra-92wf7` | Upstream PR **open, not merged** — verified by ancestry: no commit matching `#4418` in `origin/main` at `4169ba4a2`. No local branch in this tree, so the fix is genuinely absent from this candidate. |
| `gc mail archive` destroys content — retain by closing | `ra-pbot4` | Upstream PR; no local branch in this tree. Not confirmed merged (see caveat below). |
| Parallelize `gc status` per-session git sweep | `ra-c3jr.2` | Upstream PR; no local branch in this tree. Not confirmed merged (see caveat below). |
| Parked PRs **#4385**, **#4388** | — | Parked per `ra-ol9b7` item 1. Verified by ancestry: neither appears in `origin/main`. No local branches. |

**Caveat on the last two rows, stated rather than smoothed over:** `ra-92wf7` records an
upstream PR number (#4418) so its exclusion is verified *by number*. `ra-pbot4` and
`ra-c3jr.2` do not expose a PR number in a field I could read, so their exclusion is
asserted from **the absence of a local branch in this tree** — which is the load-bearing
fact for what this build contains — and *not* from a confirmed upstream state. If either
was in fact merged upstream before `4169ba4a2`, it is already in this candidate **via the
base tip**, not as a local patch, and this table would understate the build. Whoever
reconciles the queue in `ra-ol9b7` should close that gap by recording PR numbers on the
beads.

### Already in the build via the upstream base (NOT local patches)

`ra-ol9b7` item 6 names the first target as a build carrying **#3785 + #3818** to satisfy
`ra-y0cj`. Both are **already merged upstream** and are ancestors of the base tip —
verified by ancestry, not by symbol presence (PROTOCOL.md §5 notes symbol presence has
already proven non-load-bearing here):

- `3fa9bb38e` — `fix(hook): fall back across stores when claim-time store empties (#3785)`
- `73b4dcad8` — `fix(hook): skip an unclaimable candidate instead of wedging the whole claim (#3818)`

Both confirmed with `git merge-base --is-ancestor <commit> origin/main`. They therefore
need no local patch. PROTOCOL.md §1's functional cross-store proof, not this ancestry
note, is what decides whether they actually work.

---

## Reproducing this build

```sh
cd /Users/home/scratch/gc-build/gascity
git checkout candidate/ra.1          # must be clean: vcs.modified=false is a blocking bar
make build VERSION=1.3.5+ra.1
```

The Makefile already points CGO at keg-only `icu4c` on macOS. Building with plain
`go build` outside `make` needs `CGO_CXXFLAGS=-I/opt/homebrew/opt/icu4c@78/include` —
`CGO_CFLAGS` alone fails with `unicode/regex.h not found`, because `go-icu-regex` compiles
C++, not C.

Provenance check required by PROTOCOL.md §5 (`vcs.modified` must be `false`):

```sh
go version -m bin/gc | grep -E 'vcs.revision|vcs.modified|mod\s'
```

**The installed Homebrew `gc` is not overwritten by any of this.** Parallel install only;
the symlink flip is `ra-kq01e`, which is gated on siuan's verification.
