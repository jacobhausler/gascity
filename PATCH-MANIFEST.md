# ra.14 construction — PATCH-MANIFEST

Base: `main` at `3db4ed265db02a411b081e4581b435d80d4a5311` (fresh full clone, never a worktree, cut
2026-08-04; identified as `merge(sling): formula-attach execution routing metadata (ga-mwrstg)`).

Clone: `/Users/home/scratch/gc-build/ra14-clone`, branch `ra14-compose`. Repo-local identity `Jacob
Hausler <jacob@hausler.cc>`; `git var GIT_AUTHOR_IDENT` verified clean of any callindor/ts.net
identity leakage.

## A. Carry-forward from ra.13.1's 13 governed PRs — independently re-verified

Checked via `gh pr view <n> --repo gastownhall/gascity --json state,mergedAt` against the fresh pin
above (not trusted from any prior list):

| PR | ra.13.1 status | Re-verified 2026-08-04 | ra.14 disposition |
|---|---|---|---|
| #4740 | included (step 1) | **MERGED** 2026-08-04T03:33:11Z | DROPPED — already an ancestor of the base |
| #4562 | included (step 2) | OPEN | **CARRIED** (step 1 here) |
| #4741 | included (step 3) | OPEN | **CARRIED** (step 2) |
| #4742 | included (step 4) | OPEN | **CARRIED** (step 3) |
| #4828 | included (step 5) | OPEN | **CARRIED** (step 4) |
| #4765 | included (step 6) | **MERGED** 2026-08-04T17:19:26Z | DROPPED — already an ancestor of the base |
| #4752 | included (step 7) | OPEN | **CARRIED** (step 5) |
| #4759 | included (step 8) | **MERGED** 2026-08-04T14:21:33Z | DROPPED — already an ancestor of the base |
| #4830 | included (step 9) | OPEN | **CARRIED** (step 6) |
| #4829 | included (step 10) | OPEN | **CARRIED** (step 7) |
| #4747 | included (step 11) | **MERGED** 2026-08-04T11:38:24Z | DROPPED — already an ancestor of the base |
| #4491 | included (step 12) | OPEN | **CARRIED** (step 8) |
| #4757 | included (step 13) | **MERGED** 2026-08-04T12:56:47Z | DROPPED — already an ancestor of the base |

Ancestry for every DROPPED row confirmed by `git merge-base --is-ancestor <upstream-merge-commit>
3db4ed265` — all five are ancestors of the ra.14 base, so their fixes ride in via the base per
RELEASE-CULTURE §1.4 ("Upstream merges one of ours → drop the entry at the next candidate and prove
it by ancestry against the new base").

Surviving carry-forward order (relative order preserved from ra.13.1's manifest, drops removed):
#4562, #4741, #4742, #4828, #4752, #4830, #4829, #4491 — 8 PRs.

**Flagged per dispatch, not added:** #4786 and #4763 both postdate ra.13.1's governed set and are
explicitly excluded per the dispatch instructions ("do NOT add new open PRs beyond the survivors
without flagging"). Both remain OPEN as of this build and are candidates for a future rung's governed
set, not this one.

## B. gascity#5000 — ReleaseWorkBead live-status re-verify

Per addendum, cherry-picked the **rebased** commit `23086a16d` from
`/Users/home/scratch/gc-build/ra11.2-clone` (branch `fix/release-clobber-live-verify`) in preference
to the raw `4813a6d6b` supervisor-pin commit — `23086a16d` sits directly on this build's exact base
commit (`3db4ed265`, verified by `git merge-base --is-ancestor`), so it cherry-picked with zero
conflict. This composite build **retires the ra.11.2 supervisor-only plist-pin deviation**
(`~/.local/gc-builds/ra.11.2-4813a6d6/BUILT-REVISION.txt`'s "REUNIFICATION DEBT" note) per `ra-23p34d`
— the fix is now carried in the normal composite rather than a standalone supervisor pin.

## C. Four fresh mechanic-authored patches (gascity#5010–#5013)

Each verified independently (own clone, own branch, own tests) by the mechanic per beads `ra-vsvjlx`,
`ra-nxppyo`, `ra-3x46cy` before this compose; re-verified again here after cherry-pick. Applied in
addendum order (#5012 before #5013, disjoint NudgeSession regions, second cherry-picks clean as
predicted).

## INCLUDED — final commit table (13 patches, one commit each)

| Step | PR | Commit (ra14-compose) | True-patch scope | Result |
|---|---|---|---|---|
| 1 | #4562 | `5c32215ee` | 4 files, +202/-1 (session_beads.go, build_desired_state_pool_info.go, +2 test files) | clean apply |
| 2 | #4741 | `fbc797dd6` | 4 files, +244/-1 (names.go, build_desired_state_pool_info.go, +2 test files) | clean apply |
| 3 | #4742 | `7b8219189` | 2 files, +44 (names.go, names_test.go) | **conflict** — see CONFLICT-RESOLUTIONS.md §1 |
| 4 | #4828 | `adc03bf3b` | 5 files, +177/-8 (session_reconciler.go + 4 more) | clean apply |
| 5 | #4752 | `b7c34f619` | 2 files, +37/-1 (compute_awake_set.go, its test) | clean apply |
| 6 | #4830 | `fdbce3a50` | 4 files, +71 (productmetrics_testhook.go, compile.go, +2 test files) | **conflict** — see CONFLICT-RESOLUTIONS.md §2 |
| 7 | #4829 | `b436bdadd` | 4 files, +104/-2 (cmd_start.go + 3 more) | clean apply |
| 8 | #4491 | `3881c81eb` | 6 files, +77/-1 (order_dispatch.go, config.go, docs/schema, +test) | **conflict** — see CONFLICT-RESOLUTIONS.md §3 |
| 9 | #5000 | `eabec6452` | 2 files, +117 (work_assignment.go-family + new regression test) | clean apply (rebased onto exact base) |
| 10 | #5010 | `4a803fb89` | 2 files, +74/-1 (pool_desired_state.go + new test); PR-BODY.md scratch file stripped | clean apply |
| 11 | #5011 | `4b42afdd4` | 4 files, +236 (beadmail.go, wisp_gc.go + 2 test files); PR-BODY.md scratch file stripped | clean apply |
| 12 | #5012 | `05abace2d` | 4 files, +138/-3 (tmux.go + 3 test files); PR-BODY.md scratch file stripped | clean apply |
| 13 | #5013 | `647fac223` | 2 files, +147/-6 (tmux.go, new integration test); PR-BODY.md scratch file stripped | clean auto-merge (disjoint region of tmux.go, as predicted) |

**FINAL_COMPOSITE_SHA = `647fac22324f78df12cd6e73e43eb29d5b2762e5`**

`git diff main..HEAD --stat` shows exactly the 13 true-patch scopes above plus 3 mechanical
conflict-resolution brace fixes (no unexplained hunk).

## DEFERRED

None. All 13 governed-set entries (8 carried PRs + #5000 + 4 fresh) applied; the 5 merged-upstream PRs
from ra.13.1's set are not "deferred" — they are satisfied via the base per RELEASE-CULTURE §1.4 and
are enumerated in the table above rather than as deferrals. #4786/#4763 are out of scope per dispatch,
not deferred (never entered this rung's governed set to begin with).

## B0 provenance checks

```
$ git merge-base --is-ancestor 3db4ed265db02a411b081e4581b435d80d4a5311 647fac22324f78df12cd6e73e43eb29d5b2762e5
(exit 0)
$ git status --porcelain=v1
(empty)
```

## Gates

```
$ go vet ./...
(clean, exit 0)
```

Test packages run (`-count=1`), all green except the pre-existing symlink-resolution flake noted below:

- `go test ./internal/execgrace ./internal/beads ./internal/formula ./internal/molecule ./internal/session ./internal/mail/beadmail ./internal/config` —
  all `ok` except `internal/formula`'s `TestDescriptionFileBaseDirResolvesSymlinkedParentWithMissingLeaf`
  and `TestCanonicalExistingPathResolvesSymlinkedGrandparentWithTwoMissingLevels`, both confirmed
  **pre-existing on the base pin** (`3db4ed265`, verified via throwaway clone before any patch applied)
  — a macOS `/var`→`/private/var` TMPDIR symlink quirk unrelated to this queue; no patch here touches
  `internal/formula`.
- `go test ./internal/runtime/tmux/...` (non-integration) — `ok`.
- `go test -tags integration ./internal/runtime/tmux/... -run 'TestNudgeSession|TestSubmitEnterAndConfirm'` —
  all 20 subtests pass, including #5012's and #5013's new tests.
- `go test ./cmd/gc -run 'Release|Unclaim|Retire|DeadRuntime|SessionBead|CloseBead|Orphan|WorkAssignment'` —
  `ok` (covers #5000's own regression test plus every touched work-assignment/session-bead path).
- `go test ./cmd/gc -run 'TestOrderDispatchMaxDispatchesPerTickConfig|TestCountClosedOrderTrackingRetentionEligible'` —
  `ok` (#4491, post-conflict-resolution re-verify).
- `go test -tags productmetrics_testhook ./cmd/gc -run TestProductMetricsTaggedProcessFixtureIsEnabled` and
  `go test ./internal/molecule -run TestCookHumanStepGateDefersResolverWithoutAssignee` — `ok` (#4830,
  post-conflict-resolution re-verify).
- `go test ./cmd/gc -run '<every remaining Test func name in session_beads_test.go, session_wpool_twins_test.go, compute_awake_set_test.go, cmd_runtime_drain_test.go, cmd_start_test.go, session_reconciler_test.go, telemetry_lifecycle_metrics_test.go, provider_factory_census_test.go>'` —
  `ok` (covers #4562, #4741, #4742, #4828, #4752, #4829 exhaustively by file).

**NOT RUN: full unfiltered `go test ./cmd/gc`.** Attempted (10m+ wall, 0.0% CPU after the first few
seconds — stalled, not merely slow) and killed. This is the same pre-existing box-specific gap recorded
in `~/.local/gc-builds/ra.11/BUILT-REVISION.txt` ("NOT RUN: full unfiltered ./cmd/gc (pre-existing
timeout on this box, stock main too — same known gap ra.10 recorded)") and in bead `ra-vsvjlx`'s notes
(a leaked `dolt sql-server` subprocess from `TestInitFromWithoutHostedPreservesTemplate` reproduces
identically across unrelated clones on this box). Coverage gap closed instead by running every touched
test file's full function set explicitly, enumerated above — every test in every file any of the 13
patches modifies has been run and passes.

`make build` output and the binary's provenance are recorded in `BUILT-REVISION.txt`.
