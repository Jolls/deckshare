# Plan: #53 — `internal/fsrs/`: go-fsrs wrapper + batch-preview/grade-time consistency tests

Phase 1, step 4 (architecture.md §11). Pure package: no DB, no HTTP, no I/O (CLAUDE.md §17). It is the single implementation that live grading, batch-preview precompute, and `replayReviews` all call (architecture.md §6).

## 0. Library investigation — findings that fix the design

Verified against the tag source (`raw.githubusercontent.com/open-spaced-repetition/go-fsrs/v4.0.0/*.go`) and `proxy.golang.org`, not from memory.

- **Version to use: `github.com/open-spaced-repetition/go-fsrs/v4 v4.0.0`** (published 2026-07-17, `go 1.26`, MIT, zero transitive dependencies). v4 is the FSRS-6 line: `type Weights [21]float64`. v3.3.1 (2024-12-17) is FSRS-5 / 19 weights and is the wrong target — architecture.md §3 says "targets FSRS v6".
- **API shape (v4):**
  - `func NewFSRS(param Parameters) *FSRS`
  - `func (f *FSRS) Repeat(card Card, now time.Time) (RecordLog, error)` — all four branches; `RecordLog = map[Rating]SchedulingInfo`.
  - `func (f *FSRS) Next(card Card, now time.Time, grade Rating) (SchedulingInfo, error)` — single branch.
  - `type Card struct { Due time.Time; Stability, Difficulty float64; ScheduledDays, Reps, Lapses uint64; State State; LastReview time.Time; RemainingSteps int }` — note **v4 has no `ElapsedDays` field** (v3 did) and the learning-step counter is named `RemainingSteps`, not `LearningSteps`.
  - `type Parameters struct { RequestRetention, MaximumInterval float64; W Weights; Decay, Factor float64; EnableShortTerm, EnableFuzz bool; LearningSteps, RelearningSteps []float64; seed string }`
  - `Rating int8`: `Manual = 0`, `Again = 1`, `Hard = 2`, `Good = 3`, `Easy = 4`. `State int8`: `New/Learning/Review/Relearning = 0..3` — identical to our `user_card_state.state` encoding and `review_log.rating` CHECK (1–4).
  - `func DefaultParam() Parameters` (retention 0.9, max interval 36500, `EnableShortTerm: true`, `EnableFuzz: false`, learning steps `{1,10}` min, relearning `{10}` min), `func DefaultWeights() Weights`, `func MigrateWeights(weights []float64) (Weights, error)` (accepts len 17/19/21).
- **The library coerces. Confirmed, and worse than `ts-fsrs` did** (architecture.md §3's warning holds): `NewFSRS` calls `clipParameters(&param)` — clamping every weight into a hard-coded per-index range — and then, *if `Validate()` still fails, silently replaces the whole set with `DefaultParam()`*. `decayAndFactor()` does the same: invalid params fall back to the default decay. A corrupt `user_fsrs_params` row therefore schedules happily and wrongly. **We validate before calling it, and we detect the clip afterwards** (§3.3).
- `(*Parameters).Validate()` exists and checks finiteness, `W[20] > 0`, `RequestRetention ∈ (0,1]`, `MaximumInterval ∈ (0,36500]`, non-negative finite steps. It **cannot** check parameter count (the field is a fixed `[21]float64`), which is precisely the `fsrs_version` check invariant §2.3 requires — that one is ours.
- **Fuzz is non-deterministic across wall-clock time.** `Scheduler.initSeed()` builds the PRNG seed as `fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability)`. A preview computed at fetch time and a recompute at grade time use different `now` in *milliseconds*, so with fuzz on the two would disagree by design and CLAUDE.md §10.2's test would be flaky rather than meaningful. Fuzz only engages for intervals ≥ 2.5 days (`ApplyFuzz`). **Our wrapper hard-codes `EnableFuzz: false` and exposes no way to turn it on.** (`DefaultParam()` also defaults it off — we do not rely on that default.)
- `Preview()` is literally `Review(Again/Hard/Good/Easy)` over one `Scheduler`, and `Repeat`/`Next` build the scheduler separately — so preview-vs-recompute parity inside the library is real but not tautological, which is what makes §3.6's design choice worth making.

## 1. Dependency

`go.mod`, added with `go get github.com/open-spaced-repetition/go-fsrs/v4@v4.0.0` (do not hand-edit; commit the resulting `go.sum`):

```
require github.com/open-spaced-repetition/go-fsrs/v4 v4.0.0
```

It joins `github.com/jackc/pgx/v5` in the direct-require block. No `// indirect` additions — the library has none.

Import alias everywhere in the package (our package is also `fsrs`):

```go
gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
```

## 2. Files

| File | Contents |
|---|---|
| `internal/fsrs/doc.go` | unchanged (existing package comment already states the purity rule) |
| `internal/fsrs/errors.go` | sentinel errors (§4) |
| `internal/fsrs/params.go` | `Params`, `NewParams`, `NewDefaultParams`, validation, engine construction + clip detection |
| `internal/fsrs/schedule.go` | `Rating`, `State`, `CardState`, `Outcome`, `Preview`, `Schedule`, `PreviewAll`, `ElapsedDays`, conversions |
| `internal/fsrs/params_test.go` | validation rejection table |
| `internal/fsrs/schedule_test.go` | conversion/mapping units, fuzz-off assertions, library fuzz characterisation |
| `internal/fsrs/consistency_test.go` | CLAUDE.md §10.2 property test |

No other package changes. `internal/review/` stays a stub — this issue does not touch it.

## 3. Public API — exact signatures

### 3.1 `schedule.go` types

```go
// Rating is a grade, wire-identical to review_log.rating (CHECK 1..4).
type Rating uint8

const (
	Again Rating = 1
	Hard  Rating = 2
	Good  Rating = 3
	Easy  Rating = 4
)

// Ratings is the canonical iteration order for the four branches.
var Ratings = [4]Rating{Again, Hard, Good, Easy}

func (r Rating) Valid() bool  // 1..4; go-fsrs's Manual (0) is never valid here
func (r Rating) String() string

// State is wire-identical to user_card_state.state (CHECK 0..3).
type State uint8

const (
	New        State = 0
	Learning   State = 1
	Review     State = 2
	Relearning State = 3
)

func (s State) Valid() bool
func (s State) String() string

// CardState is the prior scheduling state of one card for one user.
// Field-for-field the subset of user_card_state that FSRS reads; the caller
// maps the row, this package never sees a database.
type CardState struct {
	Due           time.Time // zero for a never-scheduled card
	Stability     float64
	Difficulty    float64
	State         State
	Reps          int32
	Lapses        int32
	ScheduledDays int32
	LearningSteps int16     // go-fsrs v4 Card.RemainingSteps
	LastReview    time.Time // zero = never reviewed
}

// Outcome is what one rating produces. Every field is the value to store in
// user_card_state; nothing here is advisory.
type Outcome struct {
	Due           time.Time // always UTC
	Stability     float64
	Difficulty    float64
	State         State
	Reps          int32
	Lapses        int32
	ScheduledDays int32
	LearningSteps int16
}

// Preview holds the four branches invariant §2.6 requires the server to
// precompute at batch-fetch time. A struct, not a map: fixed arity, no
// iteration-order surprises, and every branch is statically reachable.
type Preview struct {
	Again Outcome
	Hard  Outcome
	Good  Outcome
	Easy  Outcome
}

// For returns the branch matching r, or ErrInvalidRating.
func (p Preview) For(r Rating) (Outcome, error)
```

`Outcome` deliberately carries **no** `ElapsedDays`: go-fsrs v4 dropped the field, and `review_log.elapsed_days_before` is a property of the *prior* state, so it gets its own helper:

```go
// ElapsedDays is whole UTC days from prior.LastReview to now (0 if never
// reviewed, 0 if now precedes LastReview). Mirrors go-fsrs's own
// dateDiffInDays so review_log.elapsed_days_before matches what the
// scheduler used.
func ElapsedDays(prior CardState, now time.Time) int32
```

### 3.2 The two entry points

```go
// Schedule is the grade-time recompute: the authoritative path (CLAUDE.md
// §2.7). It is what the live grading path and replayReviews call.
func Schedule(p Params, prior CardState, rating Rating, now time.Time) (Outcome, error)

// PreviewAll is the batch-fetch precompute: all four branches for one card
// under one prior state (CLAUDE.md §2.6).
func PreviewAll(p Params, prior CardState, now time.Time) (Preview, error)
```

Both take `now` explicitly — no `time.Now()` anywhere in the package. That is what makes the §10.2 property testable at all, and it is part of the purity rule.

### 3.3 `params.go`

```go
// Params is a validated parameter set. The zero value is not usable; the
// only ways to obtain one are NewParams and NewDefaultParams, so no
// unvalidated parameter set can reach go-fsrs.
type Params struct {
	version          int
	weights          gofsrs.Weights
	desiredRetention float64
}

func (p Params) Version() int             // for review_log.fsrs_version
func (p Params) DesiredRetention() float64

// NewParams validates a stored user_fsrs_params row. weights is the decoded
// params JSON array; fsrsVersion is the row's fsrs_version.
func NewParams(fsrsVersion int, weights []float64, desiredRetention float64) (Params, error)

// NewDefaultParams is the empty-params-array case: migration 00012 documents
// `params = '[]'` as "use the library defaults", which is the MVP state
// (architecture.md §11 step 10 defers fitting entirely).
func NewDefaultParams(desiredRetention float64) (Params, error)
```

`NewDefaultParams` uses `gofsrs.DefaultWeights()` and `version = 6`, then runs the same `desiredRetention` check as `NewParams` — one validation path, no bypass.

### 3.4 Validation, in this exact order

`NewParams` checks, returning on the first failure:

1. **Known version.** `weightCountFor` is the map that encodes invariant §2.3:
   ```go
   var weightCountFor = map[int]int{45: 17, 5: 19, 6: 21} // FSRS-4.5, 5, 6
   ```
   Missing key → `ErrUnknownVersion`.
2. **Weight count matches the declared version.** `len(weights) != weightCountFor[fsrsVersion]` → `ErrWeightCount`. Checked for all three versions, so a 19-element array declared as `fsrs_version = 6` is rejected as a count error, not mistaken for a supported set.
3. **Supported version.** `fsrsVersion != 6` → `ErrUnsupportedVersion`. MVP is FSRS-6 only (Resolved Decision 4).
4. **Every weight finite.** `math.IsNaN(w) || math.IsInf(w, 0)` → `ErrNonFiniteWeight` (wrapped with the index).
5. **Desired retention.** non-finite, `<= 0`, or `> 1` → `ErrRetentionRange` (Resolved Decision 2).

No clip-detection / anti-coercion check (Resolved Decision 5): finite, in-range-per-step-4/5 weights are passed straight to `gofsrs.NewFSRS`, which may itself clamp them into its own internal ranges or (in the pathological case) fall back to its built-in defaults, silently and without an error. This is a known go-fsrs rough edge — tracked in [enshu#67](https://github.com/Jolls/deckshare/issues/67), which also notes it as a candidate to report upstream — accepted for the Phase 1 MVP rather than built around here.

### 3.5 Engine construction — fuzz off, deterministic, no knobs

One unexported constructor, used by every call site:

```go
func (p Params) engine() *gofsrs.FSRS {
	return gofsrs.NewFSRS(gofsrs.Parameters{
		RequestRetention: p.desiredRetention,
		MaximumInterval:  36500,                            // library default
		W:                p.weights,
		EnableShortTerm:  true,                             // Anki-equivalent short-term steps
		EnableFuzz:       false,                            // §10.2 parity: seed is wall-clock derived
		LearningSteps:    gofsrs.DefaultLearningSteps(),    // {1, 10} minutes
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),  // {10} minutes
	})
}
```

`EnableFuzz: false` is a literal, not a parameter, not a field on `Params`, and there is no exported way to change it. A comment above it records the seed formula from `scheduler.go` and the reason.

### 3.6 Load-bearing design call: `Schedule` uses `Next`, `PreviewAll` uses `Repeat`

`Schedule` could trivially be `PreviewAll(...).For(rating)`. **Do not implement it that way.** That would make CLAUDE.md §10.2's property true by construction and the repo's second-highest-priority test would assert nothing. Instead:

- `PreviewAll` → `eng.Repeat(card, now)` → convert all four entries of the `RecordLog`.
- `Schedule` → `eng.Next(card, now, rating)` → convert the one `SchedulingInfo`.

Both share the same unexported `toLibCard(CardState) (gofsrs.Card, error)` and `fromLibCard(gofsrs.Card) Outcome`, so there is exactly one conversion implementation, and the property test then covers a genuine seam: the library's `Repeat` vs `Next` paths, our two call sites, and the shared conversion applied twice.

### 3.7 Conversions

`toLibCard` rejects, with `ErrInvalidCardState` (wrapped with which field):
- `!prior.State.Valid()`
- `prior.Reps < 0 || prior.Lapses < 0 || prior.ScheduledDays < 0 || prior.LearningSteps < 0` (the library's fields are `uint64`/`int`; a negative int32 would wrap silently)
- non-finite `Stability`/`Difficulty`
- `prior.State != New && (Stability < 0.001 || Difficulty < 1.0)` — matches the library's `validateCard` bounds so the failure names our field, not theirs

`!prior.LastReview.IsZero() && prior.LastReview.After(now)` is left to the library (`validateCard` rejects it); `Schedule`/`PreviewAll` wrap the returned error with `ErrSchedule` using `%w`. Note for the caller: architecture.md §6 already requires clamping/rejecting a future `reviewedAt` upstream.

`fromLibCard` sets `Due: card.Due.UTC()` (CLAUDE.md §9 — UTC everywhere) and narrows `uint64 → int32` for `Reps`/`Lapses`/`ScheduledDays`; `ScheduledDays` is bounded by `MaximumInterval` 36500, and `Reps`/`Lapses` increment by ≤1 per review, so no overflow guard is warranted.

Both entry points call `rating.Valid()` / build the engine before touching the library; every returned error from `gofsrs` is checked and wrapped — no `_ = err`, and no `any`/`interface{}` appears anywhere in the package (CLAUDE.md §9).

## 4. `errors.go` — sentinels

```go
var (
	ErrUnknownVersion     = errors.New("fsrs: unknown fsrs_version")
	ErrUnsupportedVersion = errors.New("fsrs: unsupported fsrs_version (FSRS-6 only)")
	ErrWeightCount        = errors.New("fsrs: weight count does not match fsrs_version")
	ErrNonFiniteWeight    = errors.New("fsrs: non-finite weight")
	ErrRetentionRange     = errors.New("fsrs: desired_retention outside (0, 1]")
	ErrInvalidRating      = errors.New("fsrs: invalid rating")
	ErrInvalidCardState   = errors.New("fsrs: invalid card state")
	ErrSchedule           = errors.New("fsrs: go-fsrs rejected the input")
)
```

All returns wrap with `fmt.Errorf("...: %w", ...)` carrying the offending index/value; tests assert with `errors.Is`.

## 5. Tests

### 5.1 `consistency_test.go` — CLAUDE.md §10.2, the top priority here

**Recommendation: a manual random-sequence generator with `math/rand/v2`, not `testing/quick`.** Reasons, stated so this is not relitigated: `quick.Check` fills structs by reflection, so ~every generated `CardState` would violate the library's own validity bounds and the test would degenerate into an exercise of the rejection path; a valid *sequence* has cross-field invariants (`LastReview` monotone, `State` reachable, `Stability ≥ 0.001` once non-`New`) that `Generator` can only express by hand-writing the generator anyway; and `quick` gives no shrinking, so a failure reports an opaque blob instead of a replayable seed. A manual generator with a fixed seed logged via `t.Logf` reproduces exactly.

```go
func TestPreviewMatchesRecompute(t *testing.T)
```

- Seed: constant `const seed = 0x5253_4653` (plus a second constant for `rand.NewPCG`), logged at the top of the test so any failure is replayable.
- `sequences = 500`, `reviewsPerSequence` random in `[1, 20]`.
- Params: alternate per sequence between `NewDefaultParams(0.9)` and `NewParams(6, jitteredDefaultWeights(rng), retention)` where `retention` is random in `[0.7, 0.98]` and the jitter stays inside the library's clamp ranges, so the generated params are ones go-fsrs accepts without clamping.
- Each sequence starts at `CardState{State: New}` and `now = 2026-01-01T00:00:00Z`.
- Each step:
  1. advance `now` by a random delta drawn from a mix of minutes (`1m..90m`, to exercise learning steps) and days (`1..400d`, to exercise review/relearning and intervals well past the 2.5-day fuzz threshold);
  2. `pv, err := PreviewAll(p, state, now)` — the batch-fetch precompute;
  3. for **each** of the four ratings: `got, err := Schedule(p, state, r, now)` — the grade-time recompute — and assert `got` equals `pv.For(r)` **field by field** (`Due` via `.Equal` and both required UTC; the seven scalar fields via `==`). No `reflect.DeepEqual`: its signature is an `any` escape hatch, and a `time.Time` struct compare is wrong on monotonic/location grounds anyway.
  4. pick one rating at random, set `state = applyOutcome(state, out, now)` (an in-test helper that writes the outcome back the way `internal/review` will write `user_card_state`: `Due/Stability/Difficulty/State/Reps/Lapses/ScheduledDays/LearningSteps` from the outcome, `LastReview = now`), and continue.

**"Same prior state" means the identical `CardState` value *and* the identical `now` passed to both calls.** That is the property, and the test must not blur it: at grade time the real server calls `Schedule` with the event's `reviewedAt`, which is later than the batch fetch, and `Due = now + interval` legitimately shifts by that delta. A preview going stale as wall-clock advances is the UX matter architecture.md §6 already accounts for (the grade-time recompute is always what gets stored); a *disagreement at the same instant* is the two-implementations drift this test exists to catch.

Two more properties in the same file, sharing the generator:

```go
func TestScheduleIsDeterministic(t *testing.T)  // same (p, state, rating, now) twice => identical Outcome;
                                                // fails loudly if fuzz is ever re-enabled
func TestReplayIsDeterministic(t *testing.T)    // replaying one generated (rating, now) sequence twice
                                                // converges to the same final CardState — the
                                                // replayReviews idempotence architecture.md §3 calls for
```

### 5.2 `params_test.go` — validation rejections, table-driven

Each row asserts `errors.Is(err, want)` and that the returned `Params` is the zero value:

| Input | Expected |
|---|---|
| `fsrsVersion = 7`, 21 finite weights | `ErrUnknownVersion` |
| `fsrsVersion = 6`, 19 weights | `ErrWeightCount` |
| `fsrsVersion = 5`, 21 weights | `ErrWeightCount` |
| `fsrsVersion = 45`, 21 weights | `ErrWeightCount` |
| `fsrsVersion = 5`, 19 valid weights | `ErrUnsupportedVersion` |
| `fsrsVersion = 45`, 17 valid weights | `ErrUnsupportedVersion` |
| defaults with `W[3] = math.NaN()` | `ErrNonFiniteWeight` |
| defaults with `W[3] = math.Inf(1)` / `math.Inf(-1)` | `ErrNonFiniteWeight` |
| retention `0`, `-0.1`, `1.5`, `NaN`, `+Inf` | `ErrRetentionRange` |
| retention `1.0` | accepted (Resolved Decision 2) |
| `NewParams(6, DefaultWeights()[:], 0.9)` | no error; `Version() == 6` |
| `NewDefaultParams(0.9)` | no error; `Version() == 6` |
| `NewParams(6, nil, 0.9)` | `ErrWeightCount` (empty array is `NewDefaultParams`'s job, not a silent default) |

Plus `TestZeroParamsRejected`: `Schedule(Params{}, ...)` and `PreviewAll(Params{}, ...)` must return an error rather than schedule with an all-zero weight set.

### 5.3 `schedule_test.go` — mapping, fuzz, and the library's behaviour on record

- `TestFuzzIsOff`: `p.engine().EnableFuzz` is `false` for both `NewParams` and `NewDefaultParams`, and `PreviewAll` on a state whose `Good` interval exceeds the 2.5-day fuzz threshold returns identical `ScheduledDays` across 100 calls with `now` jittered by ±500 ms (the seed's granularity).
- `TestLibraryFuzzIsWallClockSeeded`: constructs a `gofsrs.FSRS` **directly** with `EnableFuzz: true`, calls `Repeat` at two `now` values 1 ms apart on a long-interval card, and asserts the results *differ*. This is a characterisation test: it fails if a future go-fsrs upgrade changes the seeding, which is exactly when the "force fuzz off" decision needs re-reading. Its comment names architecture.md §3 and the `initSeed` formula. If a library change makes fuzz card-deterministic, this test failing is the signal to revisit — not a reason to skip it.
- `TestInvalidRating`: `Schedule(..., Rating(0), ...)` and `Rating(5)` → `ErrInvalidRating`; `Preview.For` likewise.
- `TestInvalidCardState`: negative `Reps`/`Lapses`/`ScheduledDays`/`LearningSteps`, `State(4)`, `NaN` stability, `Review` state with `Stability = 0` → `ErrInvalidCardState`.
- `TestLastReviewAfterNow`: → `ErrSchedule`.
- `TestNewCardOutcomes`: a `State: New` card previewed at a fixed `now` yields `Again → Learning`, `Easy → Review`, all four `Due` strictly after `now`, all `Due` in UTC, `Reps == 1`, and `Lapses` incremented only on `Again`.
- `TestElapsedDays`: zero `LastReview` → 0; `now` before `LastReview` → 0; 23 h spanning a UTC midnight → 1 (day-boundary semantics, matching `dateDiffInDays`); 47 h → 1.

## 6. Sequencing

1. `go get github.com/open-spaced-repetition/go-fsrs/v4@v4.0.0`; commit `go.mod` + `go.sum`.
2. `errors.go`, then `params.go` (validation + clip detection), then `params_test.go` — the rejection path lands before anything can schedule.
3. `schedule.go` types and conversions, then `Schedule`/`PreviewAll`, then `schedule_test.go`.
4. `consistency_test.go` last — it needs both entry points.
5. `go build ./... && go vet ./... && golangci-lint run && go test ./...` (CLAUDE.md §14). `CHANGELOG.md` under `### Added`, one line linking #53.
6. Sanity check the purity rule before opening the PR: `go list -deps ./internal/fsrs` must show only stdlib plus `go-fsrs` — no `pgx`, no `net/http`, no `internal/db` (CLAUDE.md §17).

## 7. Anticipated friction

- **`docs/schema.md` drift.** Line 309 says `user_card_state.learning_steps` mirrors "`go-fsrs`'s `Card.LearningSteps`"; in v4 the field is `Card.RemainingSteps`. The column is right, the doc's field name is stale. Fix it in the same PR (one word) — do not rename the column.
- **`review_log.elapsed_days_before`** has no library source in v4; `ElapsedDays(prior, now)` is what `internal/review` must call. Say so where the helper is defined, since the omission would otherwise look like a gap.
- **`Reschedule`/`Rollback`/`MemoryState`/`Forget`** exist in v4 and are tempting for `replayReviews`. Out of scope: replay is `internal/review`'s to build out of `Schedule`, so there stays exactly one path through this package.

---

## Resolved decisions

1. **`fsrs_version` encoding for FSRS-4.5: `45`.** `{45, 5, 6}` as the three known versions — confirmed, no schema change.
2. **`desired_retention` upper bound: `(0, 1]` per the issue.** `retention == 1` is accepted by the package (unreachable from a stored row, since the DB CHECK is stricter — that gap is expected, not a bug).
3. **Default `desired_retention`: `0.9`**, matching `go-fsrs`'s own `DefaultParam()` value. Applies to the #59 retention-only row default.
4. **MVP supports `fsrs_version = 6` only.** 4.5 and 5 rows are rejected with `ErrUnsupportedVersion`; `MigrateWeights` is not wired up. Deferred, not tracked as a separate issue for now — revisit when `.apkg` import (step 8) actually needs to bring in older param sets.
5. **No clip-detection / anti-coercion check — allow silent clamping.** `NewParams`/`NewDefaultParams` do not compare what `gofsrs.NewFSRS` kept against what was passed in (§3.3/§3.4 updated accordingly; `ErrParamsCoerced` removed from §4 and the §5.2 test table). This go-fsrs behavior (silent clamp, or full silent substitution with `DefaultParam()` if `Validate()` still fails) is tracked in [enshu#67](https://github.com/Jolls/deckshare/issues/67), which also notes it as a candidate to report upstream. Revisit if a corrupt/badly-fitted row silently scheduling wrong ever becomes an observed problem.
