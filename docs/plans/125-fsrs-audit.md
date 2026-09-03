# Issue #125 — Audit: `internal/fsrs/`

Audit zone: `internal/fsrs/` (7 files, 912 lines incl. tests). Simplification / best practices only — **no behavior change intended**. Anything that would change a computed FSRS output is in **Open questions**, not decided here.

Baseline verified before planning: `go test ./internal/fsrs/` passes, `golangci-lint run ./internal/fsrs/...` reports 0 issues.

## Library facts, re-verified against the module cache

`go.mod:11` pins `github.com/open-spaced-repetition/go-fsrs/v4 v4.0.0` (FSRS-6 line, `Weights [21]float64`). Everything below was read from `$(go env GOMODCACHE)/github.com/open-spaced-repetition/go-fsrs/v4@v4.0.0/`, not from memory:

- `fsrs.go:18` `NewFSRS` → `clipParameters(&param)`; then `if param.Validate() != nil { param = DefaultParam() }`. **Neither path returns an error.** Clipping ranges are `parameters.go`'s `clampRanges [21][2]float64`.
- `clamp(NaN, lo, hi)` is `math.Max(lo, math.Min(NaN, hi))` = `NaN`, so a non-finite weight **survives clipping**, fails `Validate()`, and discards the whole set *including `RequestRetention`*.
- `parameters.go:58` `Validate()` checks weight finiteness, `W[20] > 0`, `RequestRetention ∈ (0,1]`, `MaximumInterval ∈ (0,36500]`, finite non-negative steps. It cannot check parameter count (fixed-size array) — that check is ours.
- `scheduler.go:52` `initSeed()`: `s.parameters.seed = fmt.Sprintf("%d_%d_%f", t.UnixMilli(), reps, mul)` where `t` is **the caller-supplied `now`**, `reps` is `current.Reps`, `mul` is `Difficulty*Stability`. **The seed is a pure function of its inputs — not the process clock, not `crypto/rand`.**
- `arithmetic.go:60` `nextInterval` short-circuits fuzz when `!EnableFuzz || interval < 2.5`; `alea.go` is a deterministic PRNG over the seed string.
- `arithmetic.go:8,10` `sMin = 0.001`, `dMin = 1.0` are **unexported**, so `schedule.go:211`'s literals necessarily mirror them by hand.

This resolves both items architecture.md §3 flags as *unverified*. The prose answer changes in one important respect from what the code comments currently claim — see Edit 1.

## Areas read and found clean — no edits proposed

- **`doc.go`, `errors.go`** — 3 and 15 lines. Nine sentinels, every one reachable and asserted by `errors.Is` in a test. Nothing dead.
- **Purity (CLAUDE.md §17)** — confirmed: the package imports only `errors`, `fmt`, `math`, `time`, `math/rand/v2` (test) and `go-fsrs`. No `time.Now()` anywhere; both entry points take `now`. No `any`/`interface{}`, no swallowed errors (CLAUDE.md §9).
- **`Schedule` uses `Next`, `PreviewAll` uses `Repeat`** (schedule.go:160, 177) — deliberate (plan 53 §3.6): making `Schedule` a `PreviewAll(...).For(r)` wrapper would make CLAUDE.md §10.2's property true by construction. Do not "simplify" this.
- **`consistency_test.go`** — the §10.2 property test is well-built: fixed logged seed, 500 sequences, field-by-field comparison with `Due` via `.Equal` plus a UTC-location assertion, no `reflect.DeepEqual`. Only the duplicated fold (Edit 3) is a finding.
- **`clampToInt32`** (schedule.go:246) and the `toLibCard` rejection ladder — each check named against the library bound it mirrors.
- **`weightCountFor = map[int]int{45: 17, 5: 19, 6: 21}`** — this *is* invariant §2.3 in one line, with a comment naming the three versions. No change; the keys appear once each, so hoisting them into constants would add declarations without removing a magic number from a second site.
- **`docs/schema.md:326`** — already says `Card.RemainingSteps` (plan 53 §7's drift note was fixed). No action.

---

## Edit 1 — `internal/fsrs/params.go:81-85`: the fuzz comment is factually wrong

The comment says the seed is "derived from wall-clock milliseconds at call time". It is not: `initSeed` uses the **`now` argument**, so the seed is reproducible given identical inputs. The distinction is load-bearing — it means replay is safe either way and *preview parity* is the only thing fuzz would break. Plan 53 §0 stated this correctly; the shipped comment drifted from it.

**Before** (lines 81-85):
```go
// engine builds the go-fsrs scheduler over p. Fuzz is always off: the library's fuzz seed is
// derived from wall-clock milliseconds at call time (scheduler.go's initSeed), so a batch-preview
// precompute and a grade-time recompute of the same prior state at different instants would
// disagree by design with fuzz on -- which would make the CLAUDE.md §10.2 parity property flaky
// rather than meaningful. There is no exported way to turn it on.
```

**After:**
```go
// engine builds the go-fsrs scheduler over p. Fuzz is always off. The library's seed is
// fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability) (scheduler.go's initSeed)
// -- a pure function of the caller-supplied now, not of the process clock, so it is reproducible
// given identical inputs and replay would stay deterministic either way. What breaks is preview
// parity: the batch-fetch precompute passes the fetch instant and the grade-time recompute passes
// the event's reviewedAt, never the same millisecond, so with fuzz on the two would disagree by
// design -- which would make the CLAUDE.md §10.2 parity property flaky rather than meaningful.
// Pinned by TestLibraryFuzzSeedIsPureInItsInputs and TestLibraryFuzzVariesWithNow
// (schedule_test.go). There is no exported way to turn it on.
```

## Edit 2 — name the remaining magic numbers

**2a. `params.go`** — insert above `NewParams` (before line 34):
```go
// supportedVersion is the only fsrs_version this package will schedule with: FSRS-6, 21 weights
// (docs/plans/53-fsrs-wrapper.md Resolved Decision 4). 4.5 and 5 stay in weightCountFor so a row
// declaring one gets ErrUnsupportedVersion rather than ErrUnknownVersion.
const supportedVersion = 6
```
- line 45: `if fsrsVersion != 6 {` → `if fsrsVersion != supportedVersion {`
- line 67: `return NewParams(6, w[:], desiredRetention)` → `return NewParams(supportedVersion, w[:], desiredRetention)`

**2b. `params.go`** — line 90 currently hardcodes a local copy of go-fsrs's own default
(`MaximumInterval: 36500,`). Resolved decision (see Open questions): source it from the library
instead of duplicating the number, so a future go-fsrs default change is inherited automatically;
if enshu ever wants a ceiling that diverges from the library default, that becomes an explicit,
separate user-facing setting rather than a silent local override. Change line 90 to:
```go
MaximumInterval:  gofsrs.DefaultParam().MaximumInterval,
```
No new constant is introduced. `gofsrs` is already imported by `params.go`.

**2c. `schedule.go`** — insert above `toLibCard` (before line 189):
```go
// libStabilityMin and libDifficultyMin mirror go-fsrs's unexported sMin/dMin (arithmetic.go:8,10),
// the bounds its validateCard applies to a non-New card. Checked here so the error names our
// field rather than theirs; keep them in step with the library on upgrade.
const (
	libStabilityMin  = 0.001
	libDifficultyMin = 1.0
)
```
- line 211: `if prior.State != New && (prior.Stability < 0.001 || prior.Difficulty < 1.0) {` → `if prior.State != New && (prior.Stability < libStabilityMin || prior.Difficulty < libDifficultyMin) {`

## Edit 2d — `fromLibCard`'s `RemainingSteps` cast gets the same defensive clamp as its siblings

`schedule.go:228-238`'s `fromLibCard` clamps `Reps`, `Lapses` and `ScheduledDays` via `clampToInt32`
before narrowing, but casts `card.RemainingSteps` (an `int`) straight to `int16` with only a
comment arguing the bound holds (`schedule.go:237`). Resolved decision (see Open questions): add
the same defensive posture for consistency with its three siblings in the same function.

Add next to `clampToInt32` (after `schedule.go:249`):

```go
// clampToInt16 mirrors clampToInt32's rationale (saturate rather than wrap on cast) for
// LearningSteps, the one Outcome field narrowed to int16 instead of int32.
func clampToInt16(v int) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(v)
}
```

Replace `schedule.go:237`, old:

```go
		LearningSteps: int16(card.RemainingSteps), // bounded by maxLearningSteps well within int16
```

new:

```go
		LearningSteps: clampToInt16(card.RemainingSteps), // defensive: mirrors Reps/Lapses/ScheduledDays above
```

## Edit 3 — the missing exported symbol: `Outcome.CardStateAt`

The `Outcome → CardState` fold exists twice, field-for-field identical:
- `internal/review/replay.go:25-37` `outcomeToCardState` — the production fold, called at `replay.go:53` (reached by `ReplayCard` and `gradeOutOfOrder`).
- `internal/fsrs/consistency_test.go:46-58` `applyOutcome` — a test-local copy, called at lines 134, 160, 186.

That is a live drift hazard, not just tidiness: adding a field to `Outcome`/`CardState` updates one copy and silently leaves the §10.2 parity test folding a *different* way than `internal/review` actually does. The fold can only be shared by living in `internal/fsrs` (it is that package's two types, and `internal/review` already imports it). It stays pure — `time.Time` only.

**3a. `internal/fsrs/schedule.go`** — insert after the `Outcome` type (after line 98):
```go
// CardStateAt carries an Outcome forward into the prior CardState a later Schedule/PreviewAll
// call reads. Outcome has no LastReview of its own -- that "when" belongs to review_log, not to a
// single Schedule call (architecture.md §6) -- and the caller always knows it, since reviewedAt is
// the now that produced the Outcome. It lives here rather than in internal/review because
// internal/review's replay and this package's CLAUDE.md §10.2 parity test must fold identically:
// two copies of the same field list drift the moment a field is added to either struct.
func (o Outcome) CardStateAt(reviewedAt time.Time) CardState {
	return CardState{
		Due:           o.Due,
		Stability:     o.Stability,
		Difficulty:    o.Difficulty,
		State:         o.State,
		Reps:          o.Reps,
		Lapses:        o.Lapses,
		ScheduledDays: o.ScheduledDays,
		LearningSteps: o.LearningSteps,
		LastReview:    reviewedAt,
	}
}
```

**3b. `internal/review/replay.go`** — delete lines 21-37 (`outcomeToCardState` and its doc comment); line 53 becomes:
```go
		state = outcome.CardStateAt(row.ReviewedAt)
```
`outcomeToCardState` has exactly one caller — verify with `grep -rn "outcomeToCardState" --include=*.go .` returning nothing after the edit. `replay.go` keeps its `time` import (`LoggedReview.ReviewedAt`).

**3c. `internal/fsrs/consistency_test.go`** — delete lines 43-58 (`applyOutcome` and its doc comment); replace its three call sites:
- line 134: `state = applyOutcome(out, now)` → `state = out.CardStateAt(now)`
- line 160: `state = applyOutcome(first, now)` → `state = first.CardStateAt(now)`
- line 186: `state = applyOutcome(out, now)` → `state = out.CardStateAt(now)`

## Edit 4 — `internal/fsrs/schedule_test.go:50-93`: a test that cannot fail

`TestLibraryFuzzIsWallClockSeeded` has two branches: results equal → `t.Skip`, results differ → fall off the end. **Neither branch can fail**, so the characterisation its own comment promises ("if a future go-fsrs upgrade changes this seeding and this test starts failing") is not actually guarded — a library change to card-deterministic seeding would turn it into a silent SKIP and CI would stay green. Plan 53 §5.3 specified "asserts the results *differ*"; the shipped test does not.

Replace lines 50-93 in full with two tests that can fail, one per half of the fact:

```go
// The two tests below characterise go-fsrs itself, not our wrapper. They pin what architecture.md
// §3 flagged as unverified: exactly what the library's fuzz seed depends on. scheduler.go's
// initSeed builds it as fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability), so
// it is deterministic -- §3's worry about a "non-reproducible source" does not hold -- but now is
// one of its inputs, and preview (fetch instant) and grade (reviewedAt) never pass the same now.
// That, and only that, is why params.go's engine hard-codes EnableFuzz: false. If a go-fsrs
// upgrade changes the seeding, one of these two fails, and that is the signal to revisit the
// fuzz-off decision -- not a reason to skip or delete them.

// fuzzOnEngine is what params.go's engine() would build with fuzz turned on: the one
// configuration our wrapper deliberately refuses to produce, so it is constructed here by hand.
func fuzzOnEngine() *gofsrs.FSRS {
	return gofsrs.NewFSRS(gofsrs.Parameters{
		RequestRetention: 0.9,
		MaximumInterval:  36500,
		W:                gofsrs.DefaultWeights(),
		EnableShortTerm:  true,
		EnableFuzz:       true,
		LearningSteps:    gofsrs.DefaultLearningSteps(),
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),
	})
}

// fuzzableCard is a Review-state card whose intervals land far past the 2.5-day threshold below
// which fuzz is a no-op (arithmetic.go's nextInterval), so fuzz actually engages.
func fuzzableCard() gofsrs.Card {
	return gofsrs.Card{
		State:      gofsrs.Review,
		Stability:  50,
		Difficulty: 5,
		LastReview: fixedNow().Add(-30 * 24 * time.Hour),
	}
}

func TestLibraryFuzzSeedIsPureInItsInputs(t *testing.T) {
	engine := fuzzOnEngine()
	card := fuzzableCard()

	first, err := engine.Repeat(card, fixedNow())
	if err != nil {
		t.Fatalf("Repeat: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := engine.Repeat(card, fixedNow())
		if err != nil {
			t.Fatalf("Repeat: %v", err)
		}
		for _, r := range []gofsrs.Rating{gofsrs.Again, gofsrs.Hard, gofsrs.Good, gofsrs.Easy} {
			if again[r].Card.ScheduledDays != first[r].Card.ScheduledDays {
				t.Fatalf("iteration %d rating %v: ScheduledDays = %d, want %d -- even with fuzz on, the same (card, now) must be reproducible; a process-clock or crypto/rand seed would break replay determinism, not just preview parity",
					i, r, again[r].Card.ScheduledDays, first[r].Card.ScheduledDays)
			}
		}
	}
}

func TestLibraryFuzzVariesWithNow(t *testing.T) {
	engine := fuzzOnEngine()
	card := fuzzableCard()

	distinct := map[uint64]struct{}{}
	for i := 0; i < 200; i++ {
		log, err := engine.Repeat(card, fixedNow().Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("Repeat: %v", err)
		}
		distinct[log[gofsrs.Good].Card.ScheduledDays] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Fatalf("Good.ScheduledDays took %d distinct value(s) across 200 one-millisecond shifts of now, want >= 2 -- now is a seed input (scheduler.go's initSeed), which is the whole reason EnableFuzz stays false in params.go's engine", len(distinct))
	}
}
```

Both assertions are deterministic, not probabilistic: `alea` has no entropy source beyond the seed string, and every input here is fixed. `TestLibraryFuzzVariesWithNow` is already known to hold on this exact card — the current test passes (not skips) on a 1 ms shift of the same `(Review, S=50, D=5, LastReview=now-30d)` state, i.e. the two-value case is reached at `i=1`.

`TestFuzzIsOff` (lines 25-48) is unchanged and stays — it is the wrapper-side half.

## Edit 5 — `internal/fsrs/params_test.go`: pin the library's coercion

architecture.md §3's "don't trust an FSRS library's own parameter validation" is the reason `NewParams` exists at all, and it is currently asserted nowhere — only stated in prose in plan 53 §0. Append to `params_test.go` (its `math` and `gofsrs` imports already cover this; `defaultWeights` and `withWeight` are already defined at lines 11 and 94):

```go
// The four tests below characterise go-fsrs itself, not our wrapper. They pin the second thing
// architecture.md §3 flagged as unverified, so the reason NewParams validates before calling
// NewFSRS is an executable fact rather than only prose. NewFSRS (fsrs.go) clips every weight into
// a hard-coded per-index range and then, if Validate() still fails, silently replaces the entire
// Parameters value with DefaultParam(). Neither path returns an error.

func libParams(w gofsrs.Weights, retention float64) gofsrs.Parameters {
	return gofsrs.Parameters{
		RequestRetention: retention,
		MaximumInterval:  36500,
		W:                w,
		EnableShortTerm:  true,
		LearningSteps:    gofsrs.DefaultLearningSteps(),
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),
	}
}

func TestLibraryClipsOutOfRangeWeightSilently(t *testing.T) {
	w := gofsrs.DefaultWeights()
	w[4] = 999 // clipParameters' range for W[4] is {1.0, 10.0} (parameters.go)

	engine := gofsrs.NewFSRS(libParams(w, 0.9))
	if engine.W[4] != 10.0 {
		t.Errorf("W[4] = %v, want 10 -- silently clipped, no error returned; if this now errors or preserves the value, revisit enshu#67", engine.W[4])
	}
}

func TestLibraryReplacesTheWholeSetOnNonFiniteWeight(t *testing.T) {
	w := gofsrs.DefaultWeights()
	w[3] = math.NaN() // clamp(NaN, lo, hi) is NaN, so this survives clipping and Validate still fails

	engine := gofsrs.NewFSRS(libParams(w, 0.75))
	if engine.W != gofsrs.DefaultWeights() {
		t.Errorf("W = %v, want DefaultWeights() -- one NaN weight discards the entire fitted set, silently", engine.W)
	}
	if engine.RequestRetention != 0.9 {
		t.Errorf("RequestRetention = %v, want 0.9 -- the fallback replaces the whole Parameters value, so the user's desired_retention goes with the weights", engine.RequestRetention)
	}
}

func TestLibraryReplacesRetentionOutOfRange(t *testing.T) {
	engine := gofsrs.NewFSRS(libParams(gofsrs.DefaultWeights(), 1.5))
	if engine.RequestRetention != 0.9 {
		t.Errorf("RequestRetention = %v, want 0.9 -- a hand-edited desired_retention of 1.5 schedules at 0.9 with no error, which is what ErrRetentionRange catches first", engine.RequestRetention)
	}
}

// TestOurValidationDoesNotCatchClipping records the gap enshu#67 tracks as a test rather than a
// paragraph nobody re-reads: a finite but out-of-range weight passes NewParams (which checks
// count, finiteness and retention -- not per-index ranges) and is then silently clipped by the
// library. Behaviour unchanged by #125; if a later change makes NewParams reject this, the test
// fails and is the place to record the new decision.
func TestOurValidationDoesNotCatchClipping(t *testing.T) {
	p, err := NewParams(6, withWeight(defaultWeights(), 4, 999), 0.9)
	if err != nil {
		t.Fatalf("NewParams: %v, want accepted (enshu#67: no clip detection)", err)
	}
	if got := p.engine().W[4]; got != 10.0 {
		t.Errorf("engine W[4] = %v, want 10 -- accepted by us, clipped by the library", got)
	}
}
```

## Edit 6 — `docs/architecture.md` §3: replace both "unverified" paragraphs

Both are now verified. Replace lines 196-214 (the paragraph beginning **"Don't trust an FSRS library's own parameter validation…"** through the one beginning **"Verify `go-fsrs`'s fuzz behaviour…"**) with:

```markdown
**`go-fsrs` does not validate; it coerces — verified, and we check explicitly before calling it.**
`NewFSRS` (`fsrs.go`) clips every weight into a hard-coded per-index range, and if `Validate()`
still fails after clipping it silently replaces the *entire* `Parameters` value — all 21 weights
and `RequestRetention` with them — with `DefaultParam()`. Neither path returns an error, so a
corrupt optimiser fit or a hand-edited `user_fsrs_params` row would schedule happily and wrongly:
exactly the "plausible but wrong, forever" failure invariant §2.3 exists to prevent, and a step
worse than the `ts-fsrs` coercion the TypeScript prototype hit. `internal/fsrs.NewParams`
therefore rejects first — wrong parameter count for the declared `fsrs_version`, any non-finite
weight, `desired_retention` outside `(0, 1]`. The library's behaviour is pinned by
`TestLibraryClipsOutOfRangeWeightSilently`, `TestLibraryReplacesTheWholeSetOnNonFiniteWeight` and
`TestLibraryReplacesRetentionOutOfRange` (`internal/fsrs/params_test.go`). One gap is deliberate
and tracked in [#67](https://github.com/Jolls/deckshare/issues/67): a finite but out-of-range weight
passes our check and is clipped silently, recorded by `TestOurValidationDoesNotCatchClipping`.

**`go-fsrs`'s fuzz is deterministic in its inputs, but `now` is one of them — so it is forced off.
Verified.** `Scheduler.initSeed` (`scheduler.go`) builds the PRNG seed as
`fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability)`. It is *not* drawn from the
process clock or any other non-reproducible source, so the historical-replay path is safe either
way: `replayReviews` feeds stored `reviewed_at` values back in and converges on the same
`user_card_state` every time. Preview parity is the case that breaks — the four-branch preview is
computed at the batch-fetch instant while the grade-time recompute passes the event's
`reviewedAt`, never the same millisecond, so with fuzz on the two would disagree by design and
CLAUDE.md §10.2's consistency test would be flaky rather than meaningful. `internal/fsrs`'s engine
therefore hard-codes `EnableFuzz: false` with no exported way to turn it on. Library behaviour
pinned by `TestLibraryFuzzSeedIsPureInItsInputs` and `TestLibraryFuzzVariesWithNow`
(`internal/fsrs/schedule_test.go`); ours by `TestFuzzIsOff`.
```

Note this also corrects `(0, 1)` → `(0, 1]`, which is what the code and `errors.go`'s `ErrRetentionRange` message have always said (plan 53 Resolved Decision 2).

## Edit 7 — `CHANGELOG.md`

New version block above `## [0.1.32]`:

```markdown
## [0.1.33] - YYYY-MM-DD

### Added
- Explicit tests pinning `go-fsrs`'s two previously-unverified behaviours — that it silently clips
  or wholesale-replaces out-of-range parameters, and that its fuzz seed is reproducible but takes
  `now` as an input ([#125](https://github.com/Jolls/deckshare/issues/125)).
- `fsrs.Outcome.CardStateAt`, replacing two copies of the same `Outcome` → `CardState` fold
  ([#125](https://github.com/Jolls/deckshare/issues/125)).

### Fixed
- The go-fsrs fuzz characterisation test could not fail in either branch, so a library change to
  its seeding would have gone unnoticed ([#125](https://github.com/Jolls/deckshare/issues/125)).

### Changed
- Corrected the fuzz-seed explanation in `internal/fsrs` and architecture.md §3 (the seed comes
  from the caller-supplied `now`, not the process clock), sourced `MaximumInterval` from the
  library's own default instead of duplicating it, and named the remaining magic numbers — no
  behavior change ([#125](https://github.com/Jolls/deckshare/issues/125)).
```

## Sequencing

1. Branch `feature/125-fsrs-audit` (CLAUDE.md §14 — currently on `main`).
2. Edits 1, 2 (comment + constants). `go build ./... && go vet ./...`.
3. Edit 4, then Edit 5 — the two characterisation-test groups. **Run them before continuing**: they encode claims read off the library source, and a failure here means the fact in the plan is wrong and belongs in Open questions, not in a test.
4. Edit 3 in order 3a → 3b → 3c, then `grep -rn "outcomeToCardState\|applyOutcome" --include=*.go .` must return nothing.
5. Edits 6, 7 (docs).
6. `go build ./... && go vet ./... && golangci-lint run && go test ./...` (CLAUDE.md §14 step 1). Note `internal/review` and `internal/http` tests are DB-backed — run `bash .claude/skills/run-app/reset-db.sh` first if they fail oddly (§16).
7. Re-assert purity: `go list -deps ./internal/fsrs` shows only stdlib plus `go-fsrs` — no `pgx`, no `net/http`, no `internal/db`.
8. Per CLAUDE.md rule 5, this zone always ships a test; per §14 step 3, recommend `/code-review high` (FSRS zone), then pause for manual testing before committing.

## Considered and not proposed

- **Caching `*gofsrs.FSRS` on `Params`.** `engine()` is called once per `Schedule`/`PreviewAll`, so a 20-card batch builds 20 engines, each re-running `clipParameters` over 21 weights. Real but trivial, and caching would put a pointer in a value type currently compared with `==` (`params_test.go:46`). CLAUDE.md rule 2: no speculative optimisation.
- **Deduplicating the `gofsrs.Parameters` literal** shared by `params.go`'s `engine()` and the new `fuzzOnEngine()` test helper. It cannot be shared: the test's entire purpose is to build the one configuration `engine()` exists to forbid.
- **Moving `maxLearningSteps`** (params.go:79) to `schedule.go` next to its only user. It is a property of the engine's step lists, which `params.go` owns. Leave.
- **`Rating.String()` / `State.String()`** have no non-test caller, but they are standard `fmt.Stringer` implementations that test failure messages use implicitly. Not dead.
- **`docs/plans/53-fsrs-wrapper.md` §5.3's "47 h → 1"** contradicts the shipped `TestElapsedDays` (47 h → 2, correct for two crossed UTC midnights from noon). Merged plans are dated records; not corrected here.

---

## Resolved decisions

All open questions below were resolved with the user before implementation. None require further
judgment calls during implementation.

1. **`Preview.For` / `Ratings`** — keep both as the documented lookup API. No change.
2. **`State.Valid()`** — keep exported, keep the tautology (it mirrors go-fsrs's `isValidState`
   verbatim). No change.
3. **`Params.valid bool`** — keep the explicit field. No change.
4. **`NewParams`'s missing `W[20] > 0` check** — out of scope for #125 (it's a behavior change);
   leave for #67. No change here.
5. **`NewParams` check order** — deliberate and already pinned by `params_test.go:25-29`. No change.
6. **`MaximumInterval: 36500` hard-coded** — **source from the library instead of duplicating the
   literal.** Rationale from the user: use library defaults where possible; if enshu later wants a
   ceiling that diverges from the library's own default, that should be an explicit,
   settings-driven value, not a silent local override. Implemented in Edit 2b above
   (`gofsrs.DefaultParam().MaximumInterval`) — no new constant, since sourcing from the library
   replaces naming the literal.
7. **`fromLibCard`'s unchecked `int16(card.RemainingSteps)`** — add the same defensive clamp its
   three sibling fields already get, for consistency. Implemented in Edit 2d above
   (`clampToInt16`).
8. **FSRS-4.5 / FSRS-5 rows rejected outright, `MigrateWeights` unwired** — file a follow-up issue.
   `.apkg` import has shipped since plan 53 deferred this, so older-version rows are now a real
   possibility; wiring `MigrateWeights` is its own behavior-changing scope, not #124/#125. The
   implementing session should file this issue (reference plan 53 Resolved Decision 4 and this
   plan) as part of landing #125, separate from the code changes.
9. **Edit 3 (`Outcome.CardStateAt`) touching `internal/review/replay.go`** — approved as in-scope.
   It moves code *into* the pure `internal/fsrs` package rather than out (the method takes/returns
   only `fsrs`'s own types plus a `time.Time`), so it does not weaken CLAUDE.md §17's
   dependency-free invariant, and it closes a real drift hazard: without it, a field added to
   `Outcome`/`CardState` could update `internal/review`'s production fold while leaving the
   `internal/fsrs` consistency test's copy stale, silently checking a different fold than
   production uses.
