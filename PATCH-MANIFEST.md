# Patch manifest — patched local `gc` candidates (current: `ra.5`)

Per moiraine's manifest ruling (2026-07-19 14:1xZ, on `ra-ol9b7`): the candidate carries
a committed manifest enumerating every local patch in the build, **plus an explicit
DEFERRED list**, so that exclusion is always a statement and never a silence. siuan's
adoption gate cross-checks this file against the branches registered on `ra-ol9b7`; an
unlisted registered branch FAILS verification.

This file is local to our patched builds and must **never** be included in an upstream PR.

---

## ⚠ THIS MANIFEST WENT STALE FOR TWO CANDIDATES — read before trusting any older copy

Stated first because it is the fact most likely to mislead a reader who checks out an
older tag. `PATCH-MANIFEST.md` was last written for `ra.2` (commit `1f7d945b0`,
2026-07-19) and **was not updated when `ra.3` or `ra.4` were cut**. Both of those
candidates were tagged, built and flipped onto the live `gc` carrying a manifest whose
title, identity table and INCLUDED list describe a *different, earlier* build — naming
2 local patches where `ra.4` in fact carries 9.

Consequences worth knowing, because "the file exists and looks complete" is exactly how
this hid:

- The `ra.3` tag (`candidate/ra.3/build-2eb9930cb`) and the `ra.4` tag
  (`candidate/ra.4/build-730c9b2e0`) both contain the **ra.2** manifest. The tags are
  immutable and are NOT being re-cut to fix documentation — the binary must not change
  for a doc edit. So the manifest that describes `ra.4` is *this* commit, which sits one
  commit **after** the `ra.4` tag. Cite the tag for the artifact; cite this file for what
  is in it.
- siuan's adoption gate cross-checks this file against the branches registered on
  `ra-ol9b7`. For two candidates that cross-check was reading stale content — it could
  only ever have compared against `ra.2`'s patch set.
- Same defect class as `ra-6fv78`: the cut procedure verified **provenance** (revision,
  `vcs.modified`, tag, gates) and nothing about whether the candidate's own paperwork
  described the candidate. `gc-cut.sh` did not check it, so nothing failed. Tracked as a
  gate fix on the cut script (see the bead referenced from `ra-kbk3v`).
- **An `ra.3` manifest was in fact written — it just never reached git.**
  `/Users/home/.local/gc-builds/ra.3/PATCH-MANIFEST.md` is a real 94-line `ra.3` document
  (assembled 2026-07-20 under `ra-n68rg.1`), while `git show 2eb9930cb:PATCH-MANIFEST.md`
  is the `ra.2` one. `gc-cut.sh:243` copies whatever the tree has into the install dir, so
  the deployed and committed copies are free to diverge and did. Net effect: for `ra.3`
  there are two manifests and **neither is authoritative** — the deployed one is untracked
  and reproducible from no revision, the committed one is stale. Do not read "an install
  dir contains a manifest" as "the candidate has a manifest".

---

## Candidate identity — `ra.5` (CURRENT, cut; awaiting flip)

| Field | Value |
|---|---|
| Candidate | `ra.5` |
| Immutable tag | `candidate/ra.5/build-<short>` (annotated; peels to the built revision) — stamped by `gc-cut.sh`, pushed to FORK `github.com/jacobhausler/gascity`, never to upstream |
| Built revision | stamped by `gc-cut.sh` in `BUILT-REVISION.txt` at cut; `vcs.modified=false` (child of `77b1c1289`, adding only this manifest) |
| Upstream base | `ab8bb6a08` (inherited UNCHANGED from `ra.4`/`ra.3`; **not** upstream tip — do not describe this build as latest) |
| Local patches applied | 11 functional + this manifest (`ra.4`'s 9 + the 2 close-gate commits below) |
| Version stamp | `1.3.5+ra.5` (the `+` survives since `b8bacc8e5`) |
| Installed at | `/Users/home/.local/gc-builds/ra.5/gc`; live via the `gc-flip.sh` symlink seam once flipped |
| Cut by | `gascity-docs/scripts/gc-cut.sh` |
| Predecessor | `ra.4` (`730c9b2e0`), kept on disk; rollback is a symlink flip |

### DELTA from `ra.4` — the `ra-18okp` close-gate fix

`ra.5` is `ra.4`'s EXACT tree plus these two commits and this manifest — no upstream commits
injected, no other local patch changed. The change lives entirely in the session
reconciler's close-gate path.

| Commit | Bead | What it fixes |
|---|---|---|
| `932ec5b8a` | `ra-18okp` (attempt `ra-p37yo`) | excludes a session's OWN `mol-do-work` drain step from the close-gate assigned-work probe, opt-in per call site via an `excludeOwnDrainStep bool`; the event path at `session_reconciler.go:631` (`recordDrainAckAssignedWorkEvent`) is left UNTOUCHED so the genuinely-stranded-work signal still fires |
| `77b1c1289` | `ra-5z6wo` | makes `932ec5b8a` actually fire in production. `isSessionOwnDrainStepBead` compared `gc.step_ref` against the bare literal `"drain"`, but the store writes it formula-qualified (`"mol-do-work.drain"`), so the predicate was false for every real drain step and the exclusion was a NO-OP. Now matches the final dot-segment; the `mol-do-work` formula pin right after already narrows the match. Test fixtures rebuilt from the real store form so they cannot pass trivially before-and-after |

**Why two commits for one fix, stated because a reader will ask:** `932ec5b8a` (from
`ra-p37yo`) shipped the right *design* but was a NO-OP in production for the string-match
reason above; perrin's lead acceptance REJECTED it precisely for that, and `77b1c1289`
(`ra-5z6wo`) is the corrective one-line-class fix, lead-accepted with the falsifiable floor
re-run by perrin himself (revert the fix → ARM 2 `DrainAckOwnDrainStepClosesWithoutEvent`
FAILS; restore → PASSES). Deploy artifact per moiraine's ruling is the pair culminating in
`77b1c1289`, **not** `932ec5b8a` alone.

### Post-cut deploy note — the L4 trap bites THIS fix hardest

This fix lives in the city supervisor's OWN reconcile loop, so a symlink flip changes
NOTHING until the supervisor restarts — the same L4 trap `ra.4`'s pool-alias fix hit at
16:1xZ (installed and not running for 100 minutes). The flip **and** supervisor restart are
a maintenance-window action coordinated by moiraine (both disrupt live sessions). Acceptance
(`exactly-once-per-stop-pending` behavioural, `ra-t9mpj` step 3) CANNOT be read until the
supervisor runs the new image — verified by `go version -m $(which gc)` showing a revision
`!= 730c9b2e0a99` AND `lsof -p <supervisor-pid> | awk '$4=="txt"'` showing the `ra.5` image —
and needs a deliberately-spawned live pool subject (a quiet city is a zero denominator, not
a pass).

---

## Candidate identity — `ra.4` (predecessor — superseded by `ra.5`)

| Field | Value |
|---|---|
| Candidate | `ra.4` |
| Immutable tag | `candidate/ra.4/build-730c9b2e0` (annotated; peels to the built revision) — pushed to FORK `github.com/jacobhausler/gascity`, never to upstream |
| Built revision | `730c9b2e0a99e29dc9aefc1ec58b14d8da1b36ef`, `vcs.modified=false` |
| Upstream base | `ab8bb6a08` (inherited from `ra.3`; **not** upstream tip — do not describe this build as latest) |
| Local patches applied | 9 functional + this manifest (below) |
| Version stamp | `1.3.5+ra.4` (the `+` survives since `b8bacc8e5`) |
| Installed at | `/Users/home/.local/gc-builds/ra.4/gc`; live via the `gc-flip.sh` symlink seam |
| Cut by | `gascity-docs/scripts/gc-cut.sh` — the first candidate cut by script rather than by hand |
| Predecessor | `ra.3` (`2eb9930cb`), kept on disk; rollback is a symlink flip |

### INCLUDED — the full local stack in `ra.4`, oldest first

`git log --oneline ab8bb6a08..730c9b2e0` in a tree at the tag reproduces exactly this list.

| Commit | Bead | What it fixes |
|---|---|---|
| `4344c0af2` | `ra-m5y6t` (upstream filing `ra-fte82`) | `gc doctor` bd-backup-freshness reads the active backup pipeline's state — detailed in the ra.2 section below |
| `430631874` | `ra-fwllb` | first pool-alias livelock backoff (session_beads.go write site) — detailed in the ra.2 section below |
| `1f7d945b0` | — | the ra.2 manifest commit (this file's ancestor) |
| `b8bacc8e5` | `ra-8zop6` | `normalizeVersion` preserves SemVer build metadata, so `1.3.5+ra.N` stops being erased |
| `4a6fe9753` | — | `gc sling` dry-run route preview names the wisp root, not the work bead |
| `2eb9930cb` | — | `gc sling` restamps the work bead on formula-attach (**ra.3's tip**) |
| `840537681` | `ra-60910` | clears the two lint-red findings our own patch queue authored (misspell + staticcheck S1017) |
| `b80604b96` | `ra-uu78e` | gates the **second** unguarded pool-alias-conflict write site (`build_desired_state_pool_info.go`) through the same `deferredSingletonAliasRetryDue` predicate — the half `430631874` missed |
| `8d9a6296a` | `ra-od3o5` | strips the monotonic clock reading before convoy keyset-pagination comparisons (measured 1 failure / 200 runs on an idle box) |
| `08f0c0279` | `ra-625fy` | hook discovery no longer starves rig-routed work under first-store-wins |
| `730c9b2e0` | `ra-625fy` | lint fix for the patch immediately above — three `behaviour` misspellings it introduced |

**The last row is the cut gate earning its existence on first use.** `gc-cut.sh` REFUSED
the first `ra.4` attempt: it re-ran the lint gate on base `2eb9930cb`, found those three
misspellings absent there, attributed them to us rather than to upstream noise, and
blocked — naming exactly those three. Without it they would have shipped, and would have
bounced the `ra-625fy` upstream PR on style. **Note for whoever raises that PR:** the fix
lives only in `ra.4`; the source branch `fix/hook-cross-store-priority` still carries the
misspellings at `e545cb9f4`.

### DEFERRED from `ra.4` — deliberate exclusions

| Item | Bead | Why not included |
|---|---|---|
| Cancellation / rollback fix | `ra-q0h1k` | Validated, but exists only as **text** in a closed bead's notes — never committed. Adding a late, uncommitted patch to an otherwise-ready cut is how a cut slips. Scoped to the candidate *after* `ra.4`. |

The `ra.2` DEFERRED table further down still applies except where a row above supersedes it.

### Post-cut deploy note — a symlink flip is not a deploy

`ra.4` went live on the symlink at 2026-07-20 16:1xZ and the pool-alias counter kept
climbing, because **a symlink flip does not touch a running process**. Both patched write
sites execute inside the city supervisor's reconcile loop, so the fix was installed and
not running until the supervisor was restarted (17:57:15Z). Read the running image from
the process TEXT segment (`lsof -p PID | awk '$4=="txt"'`), never from `argv` — `argv`
says `/opt/homebrew/bin/gc` either way and tells you nothing. `gc-flip.sh` now prints the
L4 holders at every flip (commit `5b66d15`).

---

# HISTORY — candidate `ra.2` (superseded; retained for its analysis)

Everything below was written for `ra.2` on 2026-07-19 and is kept because its per-patch
verification notes, the `normalizeVersion` defect writeup and the adoption-gate disclosure
are still the best record of those patches. **Its identity table and INCLUDED list are NOT
the current build** — use the `ra.4` section above for that.

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
