# Patch manifest — patched local `gc` candidate `ra.2`

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
| Candidate | `ra.2` |
| Branch | `candidate/ra.2` |
| Upstream base | `4169ba4a2` (`fix(doctor): bound each check with a per-check timeout (#3448)`) |
| Base is upstream `main` tip | yes, at assembly time |
| Local patches applied | 2 (below) |
| Version stamp | `1.3.5-ra.2` — deliberately NOT "1.3.5" (see the defect below) |
| Built from | STAMPED AFTER BUILD — see "Built revision" below |
| Assembly timestamp (as-of pin) | 2026-07-19T19:2xZ |

The version stamp exists because the binary this candidate replaces reports a bare
`1.3.5` while being a dirty local build (`ra-ufk9t`, and the `brew upgrade` invariant in
CLAUDE.md). `gc version` on this candidate prints `1.3.5-ra.1`, which cannot be mistaken
for stock, and the embedded `vcs.revision` anchors it to a real commit.

### ⚠ Defect found while stamping it: `gc` silently strips SemVer build metadata

PROTOCOL.md §5 prescribes `1.3.5+ra.N`. **That form cannot work**, and the failure is
silent. `cmd/gc/cmd_version.go:72-74` (`normalizeVersion`) truncates the version at the
first `+`:

```go
if i := strings.IndexByte(v, '+'); i >= 0 {
    v = v[:i]
}
```

Measured, not inferred: built with `-X main.version=1.3.5+ra.1`, `gc version` prints
**`1.3.5`** — indistinguishable from stock. So the one scheme the gate names for marking a
build as non-stock is the one scheme `gc` erases, and it erases it without warning: the
build succeeds, and the binary simply lies about itself afterward.

This is very likely a contributing mechanism behind `ra-ophcr` (our running `gc` printing
`1.3.5` while embedding a dirty tree), though I have not proven that is how the current
binary was stamped, and do not claim it.

**Workaround used here:** `1.3.5-ra.2`, verified to survive (`gc version` →
`1.3.5-ra.2`). **Tradeoff, stated because it is a real cost:** under SemVer, `-` denotes a
*prerelease*, so `1.3.5-ra.1` sorts *below* stock `1.3.5`, which is backwards — this build
is 1.3.5 *plus* patches. It satisfies the bar that actually matters (it does not claim to
be stock) while being semantically wrong about ordering. The correct fix is upstream: make
`normalizeVersion` preserve build metadata. Filed as `ra-8zop6` (P1); not fixed in this
candidate, because widening the candidate's diff under an active gate is the wrong trade.

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

### 2. `24faf5a9e` — `fix/pool-alias-livelock-backoff`

- **In candidate as:** `231389cb9` (cherry-picked; identical tree)
- **Bead:** `ra-fwllb` (open — held on this deploy, which is the only remaining remedy)
- **Files:** `cmd/gc/session_beads.go` (+ `_test.go`)
- **What it changes:** a session that loses the managed pool alias race records a conflict
  and retries. For a canonical singleton pool identity the retry deliberately bypasses the
  stable-conflict guard (`retryDeferredSingleton`), because the alias is expected to free up
  when its holder exits. When it never does — two live sessions of a
  `max_active_sessions=1` agent — the retry has no converging condition and no throttle, so
  it rewrites three metadata fields on the session bead **every sync tick, forever**.
  Measured in this city: 15,489 iterations still climbing at ~1 write/12.5s, the dominant
  writer to the event log (~68% of recent events), `events.jsonl` past 109 MB, saturating the
  bounded events window for every other reader. Now backs off 30s→30m using the existing
  `pool_alias_conflict_at` stamp (no new state); recovery preserved, since the retry still
  fires on a widening interval.
- **Verification:** existing alias/sync suites unregressed; gofmt+vet clean. Falsifiable
  floor: neutering the backoff to pre-fix behaviour fails the suite on the real production
  numbers (`15237 attempts, last 8s ago: must NOT be due — this is the livelock`).
- **Why it matters for this candidate specifically:** `ra-fwllb`'s mitigation was authorized
  by the owner, executed, and FAILED — gc's own sync loop reverts the alias write within
  seconds, so no non-destructive mitigation exists. This patch is the only remedy that does
  not touch a preserved session.

---

## Why ra.2 keeps ra.1's base rather than the newer tip

Upstream `main` had advanced 8 commits (to `a38c91428`) at assembly time. This candidate
deliberately stays on **`4169ba4a2`**, the base siuan verified for ra.1.

Rationale, stated because the opposite choice is defensible and someone will ask: the delta
under review then is *exactly our two patches*, so ra.1's verification largely carries and
the flip is reasoning about a known base. Taking the newer tip would inject 8 unreviewed
upstream commits at the moment of maximum haste — precisely when the acceleration order makes
that hardest to notice. A later candidate should take the newer tip once ra.2 is in and the
seam is proven; that is a deliberate deferral, not an oversight.

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
### Measured §2 delta — and a correction to my own prediction

I first wrote that this patch would show as "passed +1, warning −1" against the 13:10Z
baseline. **That prediction was wrong**, and comparing tallies to the frozen baseline is
itself misleading. Recording it rather than quietly replacing it, because the reason is
the interesting part.

Raw tallies do not compare, for three independent reasons: the city has drifted since
13:10Z; the candidate is ~100 upstream commits ahead of stock and so **adds checks that
did not exist**; and the candidate's tally line carries an `advisory` field stock's does
not. Against the frozen baseline the candidate looks like `1 failed → 3 failed`, which
reads as a regression and is not one.

So I ran **both binaries back to back against the same live city** and diffed per check —
which isolates the binary from the drift:

```
stock  (6e7465ca, live gc): 81 passed, 7 warnings, 1 failed
candidate (1.3.5-ra.1)    : 83 passed, 6 warnings, 3 failed, 2 advisory
```

- **Checks that disappeared: NONE.** (§2's "a check that stopped running looks exactly
  like a check that started passing" — clean.)
- **Status transitions among checks present in both: exactly ONE —**
  `bd-backup-freshness ⚠ → ✓`. That is this patch, and it is the *only* behavioural
  change the candidate makes to any pre-existing check. **No stock-✓ check regressed.**
- **Three checks are NEW in the candidate** (they come from upstream commits between
  `6e7465ca` and `4169ba4a2`, **not** from any local patch):
  - `census-owner-liveness` → ✓
  - `hold-label-conventions:city` → ✗ *(advisory)*, 2 retired hold/blocked labels
  - `hold-label-conventions:tar-valon` → ✗ *(advisory)*, 29 retired hold/blocked labels

The two new ✗ are **pre-existing data debt newly detected, not new breakage**: they are the
retired `exec-*`/`gate-*`/hold label population already tracked as a known migration gap
(`workspace-hf64`). A newer binary noticing old debt is the check working. They are the
reason the failed count moves 1 → 3, and they are advisory severity.

Reproduce this diff:

```sh
cd ~/workspace
gc doctor > /tmp/doc-stock.txt
/Users/home/scratch/gc-build/gascity/bin/gc doctor > /tmp/doc-cand.txt
# then diff per check name, not by tally
```

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
git checkout candidate/ra.2          # must be clean: vcs.modified=false is a blocking bar
make build VERSION=1.3.5-ra.2
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
