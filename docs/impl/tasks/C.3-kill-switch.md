# C.3 — Control database and kill switch

Size: M · Depends on: — · Branch: `task/C.3-kill-switch` · PR title: `C.3: control database and kill switch`

## Goal

After this task there is a control database `.adlc/control.db` (package
`internal/controlplane`, m2.md decision 2) with a `kills` table, and an
operator can stop agents from another terminal:

```
adlc kill run RUN_ID --reason TEXT     stop one run
adlc kill agent AGENT --reason TEXT    stop every run of an agent (NAME or NAME@VERSION)
adlc kill all --reason TEXT            stop every run
adlc unkill run RUN_ID | agent AGENT | all
adlc kills                             list the active kills
```

The harness asks a small `KillSwitch` interface in `harness.Deps` (m2.md
decision 3; nil = off) before every model call and before every tool call.
A killed run ends with `run_completed{stop_reason: CANCELLED, detail:
"killed: <reason>"}`. `adlc run` and `adlc resume` pass a kill switch backed
by the control database when the file exists. Work-plan demo: **a kill
switch stops a running agent within one step**.

## Read first

- `AGENTS.md`; `docs/impl/m2.md` (decisions 2–4); `docs/impl/interfaces.md` §1, §6, §6.2
- `internal/eventlog/store.go`: `Open`, the pragmas, `migrate`, `ErrSchemaTooNew` — copy this pattern
- `internal/harness/harness.go` (`Deps`), `internal/harness/loop.go`: `steps`, `callModel`,
  `runToolCall`, `finish`, `stopForContext`, `policyError` (how `error_occurred` is written)
- `internal/harness/budget.go` and where `beforeModelCall` / `beforeToolCall` are called
- `internal/harness/helpers_test.go`, `harness_test.go` (`defaultDeps`, `wantKinds`, `wantStop`), `resume_test.go` (`crashRun`, `resumeOK`)
- `internal/cli/run.go` (`runOptions`, `runAgentWith`), `internal/cli/resume.go` (`resumeAgent`),
  `internal/cli/openlog.go`, `internal/cli/root.go`, `internal/cli/run_test.go` (`helloDir`), `internal/cli/resume_test.go` (`crashedRun`)

## Files

Create:

```
internal/controlplane/store.go          Store, Open, Close, migrations, options, ErrSchemaTooNew
internal/controlplane/kill.go           Scope, Kill, Kill/Unkill/Active/Killed, errors, validation
internal/controlplane/store_test.go
internal/controlplane/kill_test.go
internal/controlplane/killswitch_test.go   harness + two Stores on one file (the demo)
internal/harness/killswitch.go          KillSwitch interface, (*loop).checkKill
internal/harness/killswitch_test.go
internal/cli/controldb.go               defaultControlDBPath, open helpers
internal/cli/kill.go                    kill, unkill, kills commands
internal/cli/kill_test.go
```

Change:

```
internal/harness/harness.go     Deps.KillSwitch
internal/harness/loop.go        call checkKill in steps, callModel, runToolCall
internal/cli/run.go             runOptions.controlDB, --control-db, pass the kill switch
internal/cli/resume.go          --control-db, pass the kill switch
internal/cli/root.go            three added lines: AddCommand(newKillCmd()), AddCommand(newUnkillCmd()), AddCommand(newKillsCmd())
internal/cli/cli_test.go        TestCommandsShareDefaultControlDB
docs/impl/interfaces.md         §1 row, §6 Deps + KillSwitch, §6.2 row, new §11
```

No new dependencies (`modernc.org/sqlite` is already in `go.mod`). No change to `events.proto`.

## Design

### `internal/controlplane/store.go`

Copy `eventlog.Open` exactly (same `_pragma` values: `busy_timeout(10000)`,
`journal_mode(WAL)`, `synchronous(NORMAL)`, `_txlock=immediate`; same
`migrate` function with `PRAGMA user_version`), with `controlplane:` in
every error instead of `eventlog:`.

```go
// Package controlplane is the control database (.adlc/control.db): kill
// switches (task C.3) and the agent registry (task C.4). It never holds run events.
package controlplane

// ErrSchemaTooNew means the database was written by a newer version.
var ErrSchemaTooNew = errors.New("controlplane: database schema is newer than this binary")

// migrations[i] moves the schema from version i to i+1. Never edit an
// existing entry; append a new one (task C.4 appends the registry).
var migrations = []string{
	`CREATE TABLE kills (
		scope             TEXT    NOT NULL CHECK (scope IN ('run', 'agent', 'all')),
		target            TEXT    NOT NULL,
		reason            TEXT    NOT NULL CHECK (reason <> ''),
		created_unix_nano INTEGER NOT NULL,
		PRIMARY KEY (scope, target),
		CHECK ((scope = 'all') = (target = ''))
	) WITHOUT ROWID;`,
}

// Store is the control database. It is safe for concurrent use, including by
// several processes on the same file.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used for created times (default time.Now).
func WithClock(now func() time.Time) Option

// Open opens (creating if needed) the control database at path and brings
// its schema up to date. "" and ":memory:" are refused.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error)

// Close closes the database.
func (s *Store) Close() error
```

Error texts (tests check the first two):
`controlplane: Open needs a file path, got %q`;
`%w: database is v%d, this binary knows up to v%d` (wrapping `ErrSchemaTooNew`);
`controlplane: open %s: %w`, `controlplane: migrate: %w`,
`controlplane: read schema version: %w`, `controlplane: migration %d: %w`,
`controlplane: set schema version: %w`.

### `internal/controlplane/kill.go`

```go
// Scope says what a kill applies to.
type Scope string

const (
	ScopeRun   Scope = "run"   // Target is a run id
	ScopeAgent Scope = "agent" // Target is an agent name ("hello") or agent id ("hello@1")
	ScopeAll   Scope = "all"   // Target is ""
)

// Kill is one active kill.
type Kill struct {
	Scope     Scope
	Target    string
	Reason    string
	CreatedAt time.Time // UTC
}

var (
	// ErrInvalidKill means Kill or Unkill got a bad scope, target or reason.
	ErrInvalidKill = errors.New("controlplane: invalid kill")
	// ErrKillNotFound means Unkill found no such kill.
	ErrKillNotFound = errors.New("controlplane: no such kill")
)

// Kill records a kill. Killing the same scope and target again replaces its
// reason and created time.
func (s *Store) Kill(ctx context.Context, scope Scope, target, reason string) error

// Unkill removes a kill; ErrKillNotFound if there is none.
func (s *Store) Unkill(ctx context.Context, scope Scope, target string) error

// Active returns every kill, oldest first (ties: scope, then target).
func (s *Store) Active(ctx context.Context) ([]Kill, error)

// Killed reports whether a run must stop: it implements harness.KillSwitch.
func (s *Store) Killed(ctx context.Context, runID, agentID string) (bool, string, error)
```

**Validation** (`validateKill(scope, target)`; `Kill` also checks `reason`).
Exact error texts, all wrapping `ErrInvalidKill` with `fmt.Errorf("%w: ...")`:

| Input | Error after `controlplane: invalid kill: ` |
|-------|--------------------------------------------|
| scope not run/agent/all | `unknown scope %q (want run, agent or all)` |
| `all` with target != "" | `scope all takes no target` |
| `run`, target not `^[A-Za-z0-9._-]{1,64}$` | `invalid run id %q` |
| `agent`, target not `^[a-z][a-z0-9-]{0,31}(@[0-9]+)?$` | `invalid agent %q (want NAME or NAME@VERSION)` |
| `Kill` with `strings.TrimSpace(reason) == ""` | `reason is empty` |

The run id pattern is the one `validRunID` uses in `internal/cli/runid.go`;
the agent name pattern is the one `agentconfig` validates (`^[a-z][a-z0-9-]{0,31}$`).

**SQL.**

```sql
-- Kill
INSERT INTO kills (scope, target, reason, created_unix_nano) VALUES (?, ?, ?, ?)
ON CONFLICT (scope, target) DO UPDATE SET reason = excluded.reason, created_unix_nano = excluded.created_unix_nano;
-- Unkill: RowsAffected() == 0 → fmt.Errorf("%w: %s", ErrKillNotFound, describe(scope, target))
DELETE FROM kills WHERE scope = ? AND target = ?;
-- Active
SELECT scope, target, reason, created_unix_nano FROM kills ORDER BY created_unix_nano, scope, target;
-- Killed (args: runID, agentID, agentName)
SELECT reason FROM kills
WHERE scope = 'all'
   OR (scope = 'run'   AND target = ?)
   OR (scope = 'agent' AND (target = ? OR target = ?))
ORDER BY CASE scope WHEN 'run' THEN 1 WHEN 'agent' THEN 2 ELSE 3 END, created_unix_nano
LIMIT 1;
```

`describe(scope, target)` is `"all"` for `ScopeAll`, else `scope + " " + target`
(e.g. `run r-1`), so `Unkill` of a missing run kill says
`controlplane: no such kill: run r-1`.

**Agent matching (decision).** `agentName, _, _ := strings.Cut(agentID, "@")`.
A kill with target `hello` stops every version of `hello`; a kill with target
`hello@1` stops only runs whose `Config.AgentID` is exactly `hello@1`. There is
no prefix matching (`hel` does not stop `hello@1`). `Killed` returns
`(false, "", nil)` when no row matches (`sql.ErrNoRows`). Other errors:
`controlplane: kill check: %w`.

### Harness (`internal/harness/killswitch.go`, `harness.go`, `loop.go`)

Add to `Deps` in `harness.go`, after `Sleep`:

```go
	// KillSwitch, if set, is asked before every model call and every tool
	// call whether the run must stop (task C.3). nil = no kill switch.
	KillSwitch KillSwitch
```

`killswitch.go`:

```go
// KillSwitch says whether a run has been killed; *controlplane.Store implements it.
type KillSwitch interface {
	// Killed reports whether the run runID of agent agentID (Config.AgentID,
	// "name@version") must stop, and why.
	Killed(ctx context.Context, runID, agentID string) (killed bool, reason string, err error)
}

// killComponent is error_occurred.component for a failed kill check.
const killComponent = "killswitch"

// checkKill asks the kill switch whether the run must stop. If it must,
// checkKill finishes the run (CANCELLED, "killed: <reason>") and returns true.
// If the check fails, it records error_occurred and returns false: the run goes on.
func (l *loop) checkKill() (bool, error)
```

`checkKill` algorithm:

1. `l.deps.KillSwitch == nil` → return `false, nil`.
2. `killed, reason, err := l.deps.KillSwitch.Killed(l.callCtx, l.cfg.RunID, l.cfg.AgentID)`.
3. `err != nil`:
   a. `if stopped, err2 := l.stopForContext(); stopped { return true, err2 }` (the
      caller cancelled or the wall clock ran out during the check);
   b. append `error_occurred{error_class: ERROR_CLASS_INTERNAL, message: err.Error(), component: "killswitch"}`
      with `l.add`; return `false, <the add error>` (nil when the append worked).
4. `!killed` → `false, nil`.
5. `return true, l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "killed: "+reason)`.

**Decision: a failing check does not stop the run (fail open).** The kill
switch is an operator action on top of the run's own budgets, which still
apply. The operator sees a broken control database at once, because
`adlc kill` itself fails, and can still stop a process with Ctrl-C or a
signal; a run that already did an hour of work is not thrown away because
SQLite was busy for ten seconds. Every failed check leaves an
`error_occurred` event, so the failure is visible in the trace. (Opening the
database at the start of `adlc run` is different: see CLI below; nothing has
been done yet, so that error stops the command.)

**Where it is called** (always right after the existing `ctx.Err()` check and
before the budget check, so a kill wins over a budget in the same place):

```go
// loop.steps, inside the for loop, between the ctx check and beforeModelCall:
		if stopped, err := l.checkKill(); stopped || err != nil {
			return l.final, err
		}

// loop.callModel, first statement inside the for loop (retries are model calls too):
		if attempt > 1 {
			if stopped, err := l.checkKill(); stopped || err != nil {
				return nil, err
			}
		}

// loop.runToolCall, between the ctx check and beforeToolCall:
	if stopped, err := l.checkKill(); stopped || err != nil {
		return err
	}
```

`Resume` needs no change: it continues through `steps`, so the first check
happens before its first model call. A kill does not interrupt a model or
tool call that is already running; it takes effect at the next check. That
is what "within one step" means.

Add a row to `interfaces.md` §6.2: `| Kill switch says the run is killed (task C.3) | CANCELLED | killed: <reason> |`.

### CLI

`internal/cli/controldb.go`:

```go
// defaultControlDBPath is the default --control-db of every command that uses
// the control database (m2.md decision 2).
const defaultControlDBPath = ".adlc/control.db"

// openControlDB opens the control db, creating its directory and file
// (used by commands that write: kill).
func openControlDB(ctx context.Context, path string) (*controlplane.Store, error)

// openExistingControlDB refuses a path that does not exist:
// fmt.Errorf("control db %s does not exist", path).
func openExistingControlDB(ctx context.Context, path string) (*controlplane.Store, error)

// openKillSwitch opens the control db for a run. A missing file gives a nil
// KillSwitch, a no-op close and no error; the file is never created.
// Any other failure: fmt.Errorf("run: control db: %w", err).
func openKillSwitch(ctx context.Context, path string) (harness.KillSwitch, func(), error)
```

`openControlDB`: `os.MkdirAll(filepath.Dir(path), 0o755)` (error
`control db: create directory: %w`), then `controlplane.Open`.
`openKillSwitch`: `os.Stat(path)`; `errors.Is(err, fs.ErrNotExist)` →
`return nil, func() {}, nil` (an untyped nil interface); other stat error or
`Open` error → the wrapped error; else return the store and
`func() { _ = store.Close() }`.

`adlc run` and `adlc resume`: add `controlDB string` to `runOptions` and the
flag `f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")`
to both commands. In `runAgentWith`, right after the event store is opened
(before `startRunSandbox`), and in `resumeAgent`, right after `checkResumable`:

```go
	ks, closeKS, err := openKillSwitch(ctx, o.controlDB)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer closeKS()
```

and after `buildRun` returns successfully: `deps.KillSwitch = ks`. If task C.1
(`adlc approve`, `internal/cli/approve.go`) is merged when you start, add the
same `--control-db` flag and the same three lines to it, and say so in the PR
(its continued run goes through `steps` and `runToolCall`, so it is checked
like any other). A killed run
prints the normal summary (`stop:    cancelled`, `detail:  killed: <reason>`)
and exits 2, like every other run that did not complete.

`internal/cli/kill.go` — three commands, each with
`--control-db` (default `defaultControlDBPath`):

| Command | Args | Behaviour | stdout on success |
|---------|------|-----------|-------------------|
| `kill` | `cobra.ArbitraryArgs`; `--reason` required (`MarkFlagRequired`) | `parseKillArgs`, `openControlDB`, `Kill` | `kill set: <describe> (reason: <reason>)\n` |
| `unkill` | `cobra.ArbitraryArgs` | `parseKillArgs`, `openExistingControlDB`, `Unkill` | `kill removed: <describe>\n` |
| `kills` | `cobra.NoArgs` | missing file → print `no active kills\n` (do not create it); else `Active` | table below |

```go
// parseKillArgs maps ["run", ID] / ["agent", AGENT] / ["all"] to a scope and target.
// Anything else: errors.New(`kill: want "run RUN_ID", "agent AGENT" or "all"`).
func parseKillArgs(args []string) (controlplane.Scope, string, error)
```

`describe` in the CLI output is the same text as in the store (`run r-1`,
`agent hello`, `all`). All errors exit 1 with `Error: <message>` (return
`&exitError{code: 1, err: err}`). `kills` table, written with
`tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)`: header `SCOPE\tTARGET\tREASON\tSINCE`,
one row per kill, target `-` for `all`, SINCE = `CreatedAt.UTC().Format(time.RFC3339)`;
no rows → `no active kills\n` instead of the table.

### `interfaces.md` additions

§1: `| internal/controlplane | C.3, C.4 | control database .adlc/control.db: kill switches, agent registry |`.
§6: the `KillSwitch` field in `Deps` and the `KillSwitch` interface as above.
New §11 "Control plane — `internal/controlplane`" with `Open`, `Close`,
`WithClock`, `Scope` + constants, `Kill`, the four methods, the three errors,
and one sentence: "Agent kills match `Config.AgentID` exactly (`hello@1`) or by
name (`hello` = every version)."

## Steps

1. Branch from latest `main`.
2. `controlplane/store.go` + `store_test.go`; `go test ./internal/controlplane/`.
3. `controlplane/kill.go` + `kill_test.go`.
4. `harness`: `Deps.KillSwitch`, `killswitch.go`, the three call sites; run
   `go test ./internal/harness/` — every existing test passes unchanged.
5. `harness/killswitch_test.go`.
6. `controlplane/killswitch_test.go` (the demo test).
7. CLI: `controldb.go`, `kill.go`, root registration, `run`/`resume` wiring, `kill_test.go`.
8. `interfaces.md`.
9. `make fmt && make ci`.

## Tests

`internal/controlplane/store_test.go` (databases in `t.TempDir()`):

| Test | Asserts |
|------|---------|
| `TestOpenCreatesSchema` | `PRAGMA user_version` (read with `s.db`) == `len(migrations)`; table `kills` exists |
| `TestOpenRejectsMemoryPath` | `""` and `":memory:"` → error containing `Open needs a file path` |
| `TestOpenRejectsNewerSchema` | set `PRAGMA user_version = 99` with `database/sql` on the file, then `Open` → `errors.Is(err, ErrSchemaTooNew)` |
| `TestReopenKeepsKills` | `Kill`, `Close`, `Open` again → `Active` has the kill |

`internal/controlplane/kill_test.go`:

| Test | Asserts |
|------|---------|
| `TestKillRejectsInvalid` | table over every row of the validation table: `errors.Is(err, ErrInvalidKill)` and the exact message |
| `TestKillTwiceReplacesReason` | two `Kill`s of `run r-1` → `Active` has one kill with the second reason and the second time (`WithClock`) |
| `TestUnkillMissing` | `Unkill(run, "r-1")` on an empty db → `ErrKillNotFound`, message `controlplane: no such kill: run r-1` |
| `TestUnkillRemoves` | `Kill` then `Unkill` → `Active` empty |
| `TestActiveOrder` | three kills at times 3, 1, 2 (`WithClock`) → returned in time order; `CreatedAt` is UTC |
| `TestKilledMatching` | table: kills `[]` / `run r-1` / `agent hello` / `agent hello@1` / `all` against `(runID, agentID)` pairs `("r-1","x@1")`, `("r-2","hello@1")`, `("r-2","hello@2")`, `("r-2","hel@1")`, `("r-2","hello-x@1")` → killed and reason as the matching rules say |
| `TestKilledPrefersRunReason` | kills `all: "a"` and `run r-1: "r"` → `Killed("r-1", ...)` returns reason `r` |

`internal/harness/killswitch_test.go`. Use a fake:

```go
// fakeKillSwitch kills from the killAt-th call on (0 = never) and records its arguments.
type fakeKillSwitch struct {
	mu            sync.Mutex
	killAt, calls int
	reason        string
	err           error // returned by every call when set
	runIDs, agentIDs []string
}
```

With `twoReads()` (from `resume_test.go`) the checks happen in this order:
1 before model call `m1`, 2 before tool `t1`, 3 before tool `t2`, 4 before model call `m2`.

| Test | Asserts |
|------|---------|
| `TestKillSwitchNotKilledChangesNothing` | `twoReads()` with `KillSwitch: &fakeKillSwitch{}` → the same kinds as with `KillSwitch: nil`; status COMPLETED; 4 calls |
| `TestKilledBeforeFirstModelCall` | `killAt: 1, reason: "stop now"` → kinds `run_started, run_completed`; `wantStop(CANCELLED, "killed: stop now")`; fake `Remaining() == 2` |
| `TestKilledBeforeToolCall` | `killAt: 3` → `t1` has `tool_call_completed`, `t2` has no `tool_call_requested`; last event `run_completed` CANCELLED `killed: <reason>`; no `checkpoint` |
| `TestKilledBeforeNextStep` | `killAt: 4` → one `checkpoint`, one `model_call_completed`, then `run_completed` CANCELLED |
| `TestKilledBeforeModelRetry` | first turn is a retryable `*model.Error` (`Retryable: true`), `deps.Sleep` returns nil at once, `killAt: 2` → kinds `run_started, model_call_started, model_call_failed, run_completed`; one `model_call_started` |
| `TestKillSwitchGetsRunAndAgentID` | every recorded call has `runIDs[i] == "r1"` and `agentIDs[i] == "test@1"` (`baseConfig()`) |
| `TestKillSwitchErrorIsRecordedAndRunContinues` | `err: errors.New("db locked")` → status COMPLETED; 4 `error_occurred` events, each `component == "killswitch"`, `message == "db locked"`, class INTERNAL; `state.Errors == 4` |
| `TestResumeChecksKillSwitch` | `crashRun(t, 2, ...)` with `twoReads()` (log ends with `model_call_started`), then `Resume` with `killAt: 1` → appended kinds `run_resumed, run_completed`; CANCELLED `killed: <reason>`; the new fake is not called |

`internal/controlplane/killswitch_test.go` (package `controlplane`; it imports
`harness`, `eventlog`, `model`, `policy`, `toolrouter` — no import cycle, because
`harness` does not import `controlplane`):

| Test | Asserts |
|------|---------|
| `TestKillFromSecondStoreStopsRunWithinOneStep` | the demo. `runner := Open(path)` is the run's kill switch; `operator := Open(path)` is a second `Store` on the same file (another `*sql.DB`, as another process would have). Static tool `t__kick` (risk READ) whose `Fn` calls `operator.Kill(ctx, ScopeAgent, "hello", "demo")`. Script: turn 1 calls `t__kick`, turn 2 calls `t__kick` again, turn 3 is a final text. `harness.Run` with `AgentID: "hello@1"`, `policy.AllowAll{}`, `KillSwitch: runner` → kinds exactly `run_started, model_call_started, model_call_completed, tool_call_requested, policy_decided, tool_call_completed, checkpoint, run_completed`; CANCELLED `killed: demo`; fake `Remaining() == 2` |
| `TestKillFromSecondStoreStopsBeforeNextToolCall` | same, but turn 1 calls `t__kick` and then `t__echo` → `t__echo` never runs (its `Fn` counter is 0) and has no `tool_call_requested` |
| `TestUnkillLetsNewRunsStart` | kill `all`, `Unkill(ScopeAll, "")`, then a new run → COMPLETED |

`internal/cli/kill_test.go` (always pass `--control-db` inside `t.TempDir()`):

| Test | Asserts |
|------|---------|
| `TestKillCommandSetsKill` | `kill run r-1 --reason maintenance` → exit 0, stdout `kill set: run r-1 (reason: maintenance)\n`; `Active` has it |
| `TestKillCommandNeedsReason` | `kill all` without `--reason` → exit 1, stderr contains `required flag(s) "reason" not set` |
| `TestKillCommandBadArgs` | table `[]`, `["run"]`, `["all", "x"]`, `["nope", "x"]` → exit 1, stderr contains `kill: want "run RUN_ID", "agent AGENT" or "all"` |
| `TestKillCommandInvalidAgent` | `kill agent Bad --reason r` → exit 1, stderr contains `invalid agent "Bad" (want NAME or NAME@VERSION)` |
| `TestUnkillCommand` | kill then `unkill run r-1` → stdout `kill removed: run r-1\n`; again → exit 1, stderr contains `no such kill: run r-1` |
| `TestUnkillMissingDB` | → exit 1, stderr contains `control db <path> does not exist` |
| `TestKillsListing` | kills `run r-1` and `all` → first line fields `SCOPE TARGET REASON SINCE`, rows `run r-1 ...` and `all - ...` (compare `strings.Fields`); empty db → `no active kills\n` |
| `TestKillsMissingDBDoesNotCreateIt` | `kills` on a missing path → `no active kills\n`, exit 0, file still missing |
| `TestRunStopsWhenAgentKilled` | `helloDir`; `kill agent hello --reason frozen`; `run ... --control-db <same>` → exit 2; stdout contains `stop:    cancelled\n` and `detail:  killed: frozen\n`; the log has no `model_call_started` |
| `TestRunWithoutControlDBDoesNotCreateIt` | `run` with `--control-db <tmp>/missing.db` → exit 0 and the file does not exist |
| `TestCommandsShareDefaultControlDB` (in `internal/cli/cli_test.go`, next to `TestCommandsShareDefaultDB`, same shape) | `defaultControlDBPath == ".adlc/control.db"`; `run`, `resume`, `kill`, `unkill`, `kills` each have `--control-db` with that default |
| `TestResumeStopsWhenRunKilled` | `crashedRun(t, agent, script, db)` (run `r1`); `kill run r1 --reason x`; `resume r1 <agent> ...` → exit 2, stdout contains `detail:  killed: x\n` |

Break each piece once on purpose (e.g. drop the `runToolCall` check, swap the
ORDER BY) and see the matching test fail.

## Verify

```bash
go test -race ./internal/controlplane/ ./internal/harness/ -run 'Kill|Open|Reopen|Active|Unkill' -v   # all PASS
go test ./internal/cli/ -run 'Kill|Unkill|Kills|RunStopsWhen|RunWithoutControl|ResumeStopsWhen' -v  # all PASS
make build
./bin/adlc kill agent hello --reason "demo freeze" --control-db /tmp/adlc-c3/control.db
#   kill set: agent hello (reason: demo freeze)
./bin/adlc kills --control-db /tmp/adlc-c3/control.db   # one row: agent  hello  demo freeze  <time>
./bin/adlc run examples/hello.yaml --task "What does greeting.txt say?" \
  --fake-script examples/hello.fake.json --db /tmp/adlc-c3/events.db --control-db /tmp/adlc-c3/control.db
#   stop:    cancelled / detail:  killed: demo freeze ; exit code 2 (echo $?)
./bin/adlc unkill agent hello --control-db /tmp/adlc-c3/control.db   # kill removed: agent hello
make ci                                                              # passes
```

## Out of scope

- The agent registry (task C.4 adds migration 2 to the same database).
- `adlc eval` honouring kills (it builds its own `harness.Deps`; a later task).
- Interrupting a model or tool call that is already running when the kill is written.
- Kill history / audit log, expiry times, pausing (as opposed to killing) runs.
- A server or API in front of the control database (`adlc serve`, M4).
- Any change to `events.proto`: a kill is recorded as an ordinary `run_completed`.

## Pitfalls

- `openKillSwitch` must return an **untyped** `nil` interface when the file is
  missing. Returning a nil `*controlplane.Store` as `harness.KillSwitch` makes
  `deps.KillSwitch != nil` true and the run panics on the first check.
- Never create the control db just to check for kills (`run`, `resume`, `kills`
  only `os.Stat` it). Only `kill` creates it.
- CLI tests run in `internal/cli/`; the default `--control-db` is relative. Every
  test that kills must pass `--control-db` in `t.TempDir()`, or it leaves an
  `.adlc/` directory in the package.
- Put the check after the `ctx.Err()` check and before the budget check, in all
  three places. In `callModel` only for `attempt > 1`: the first attempt was
  already checked by `steps`.
- `Killed` uses `l.callCtx` (it has the wall-clock deadline), not `l.ctx`.
- The demo test needs two `Open` calls on the same path. One `Store` would also
  pass but would not prove that another process's write is seen.
- `controlplane` must not import `harness` in non-test code; `*Store`
  satisfies `harness.KillSwitch` structurally. The cross-package test lives in
  `controlplane`, not in `harness`, because task C.4 makes `controlplane`
  import `internal/evals`, which imports `harness` (a `harness` test importing
  `controlplane` would then be an import cycle).
- `Unkill` of `all` uses target `""`.

## Done checklist

- [ ] `internal/controlplane` with migrations, `Open`, `Close`, `Kill`, `Unkill`, `Active`, `Killed`
- [ ] `harness.Deps.KillSwitch`; checks before every model call (incl. retries) and every tool call
- [ ] A failed check writes `error_occurred` (component `killswitch`) and the run continues
- [ ] `adlc kill`, `adlc unkill`, `adlc kills`; `run` and `resume` use the control db only if it exists
- [ ] Demo test with two Stores on one file passes
- [ ] Existing harness and CLI tests pass unchanged
- [ ] `interfaces.md` §1, §6, §6.2, §11 updated
- [ ] `make ci` passes
