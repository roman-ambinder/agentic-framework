# C.4 — Agent registry and eval-gated promotion

Size: M · Depends on: C.3 · Branch: `task/C.4-registry` · PR title: `C.4: agent registry and eval-gated promotion`

## Goal

After this task the control database (task C.3) also holds an **agent
registry**:

```
adlc agent register AGENT_YAML            store an immutable version: hello@1, hello@2, ...
adlc agent list                           every version, its digest and environments
adlc agent show NAME@N|NAME@ENV           one version and its files
adlc agent promote NAME@N --to staging|prod --report eval-report.json
        [--min-pass-at-1 0.5] [--max-median-usd 0.50]
adlc run NAME@N|NAME@ENV --task ...       run a registered version
adlc resume RUN_ID NAME@N|NAME@ENV        resume one
```

A version is the exact bytes of the agent file plus its `system_file`,
addressed by `name@N` (N counts up per name) and by a sha256 **content
digest**. Registering identical content returns the existing version.
Promotion is refused unless the eval report contains results produced by
exactly that version (same digest) that meet the thresholds. Work-plan demo:
**`adlc agent promote` refuses a version whose eval report is below the
threshold.**

## Read first

- `AGENTS.md`; `docs/impl/m2.md`; `docs/impl/tasks/C.3-kill-switch.md`; `docs/impl/interfaces.md` §7, §9, §11
- `internal/controlplane/store.go`, `kill.go` (task C.3): `migrations`, `Open`, error style
- `internal/agentconfig/agentconfig.go`: `Load`, `Parse`, `parse`, `resolve`, `resolveSystemFile`, `Dir`;
  `internal/agentconfig/convert.go`: `SandboxWorkspace`, where `Dir` is used (tool and hook working directory)
- `internal/evals/runner.go` (`Result`, `RunOne`), `internal/evals/summary.go` (`Summarize`, `ProviderSummary`), `internal/evals/report.go`
- `internal/cli/run.go` (`runOptions`, `loadAgent`, `runAgentWith`), `internal/cli/resume.go`,
  `internal/cli/eval_report.go` (`readReport`), `internal/cli/summary.go` (`formatUSD`),
  `internal/cli/controldb.go` (task C.3), `internal/cli/run_test.go` (`helloDir`), `internal/cli/resume_test.go` (`crashedRun`)

## Files

Create:

```
internal/agentconfig/sources.go         AgentFileName, SourceFile, DigestFiles, (*Agent).Digest, LoadAt
internal/agentconfig/sources_test.go
internal/controlplane/registry.go       Env, Ref, ParseRef, ParseEnv, AgentVersion, Register, Version, Versions, Resolve
internal/controlplane/promote.go        Gate, Promotion, Promote
internal/controlplane/registry_test.go
internal/controlplane/promote_test.go
internal/cli/agent.go                   agent register | list | show | promote
internal/cli/agentref.go                resolveAgentRef, materialize
internal/cli/agent_test.go
```

Change:

```
internal/agentconfig/agentconfig.go     Agent.Sources; parse gets fileDir and workDir
internal/controlplane/store.go          append migration 2 (do not edit migration 1)
internal/evals/runner.go                Result.AgentDigest, set in RunOne
internal/evals/runner_test.go           TestRunOneRecordsAgentDigest
internal/cli/run.go                     runOptions.agentDir, --agent-dir, ref resolution, LoadAt
internal/cli/resume.go                  ref resolution
internal/cli/root.go                    AddCommand(newAgentCmd()) — add one line
internal/cli/cli_test.go                add "agent" to TestCommandsShareDefaultControlDB (persistent flag: look it up with cmd.PersistentFlags())
docs/impl/interfaces.md                 §7, §9, §11 (texts below)
```

No new dependencies. No change to `events.proto`.

## Design

### Content digest (`internal/agentconfig/sources.go`)

The digest must be computable wherever an agent file is loaded (the eval
runner needs it, not only the registry), so it lives in `agentconfig`.

```go
// AgentFileName is the path of the agent file itself in Agent.Sources,
// whatever the file was called on disk (renaming a file is not a new version).
const AgentFileName = "agent.yaml"

// SourceFile is one file an agent definition is made of.
type SourceFile struct {
	Path string // slash-separated; AgentFileName, or system_file as written in the agent file
	Data []byte // exact bytes read from disk
}

// DigestFiles returns "sha256:" + lowercase hex of the content digest of files.
func DigestFiles(files []SourceFile) string

// Digest returns DigestFiles(a.Sources).
func (a *Agent) Digest() string

// LoadAt is Load, except that Agent.Dir — where tool and hook commands run
// and sandbox.workspace is resolved — is workDir (made absolute) instead of
// the file's directory. system_file is still read relative to the file.
func LoadAt(path, workDir string) (*Agent, error)
```

`DigestFiles` algorithm (exact bytes):

1. Copy `files` and sort the copy: `Path == AgentFileName` first, the rest by
   `Path` ascending (`strings.Compare`).
2. `h := sha256.New()`; for each file write, in order: the bytes of `Path`,
   one byte `0x00`, the ASCII decimal of `len(Data)` (`strconv.Itoa`), one
   byte `0x00`, `Data`.
3. Return `"sha256:" + hex.EncodeToString(h.Sum(nil))`.

Test vectors (use them in `TestDigestFilesVectors`):

| files | digest |
|-------|--------|
| none | `sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `agent.yaml` = `"name: x\n"` | `sha256:158ee744f1dcedd399b42b7ff90b39fc536fd0b13060a8101532d68f6f3d813a` |
| `agent.yaml` = `"system_file: p.md\n"`, `p.md` = `"hi\n"` | `sha256:35dba3fcccdffbbba703b9d8f510c8363f7eabd5e17b8e060afa880d8a9a4b5c` |

Add to `Agent` (after `Dir`): `Sources []SourceFile \`yaml:"-"\`` with the doc
comment "Sources are the files this definition was read from: the agent file
(Path AgentFileName) first, then system_file if set. Set by Load, LoadAt and Parse."

Change `parse(data, dir)` to `parse(data []byte, fileDir, workDir string)`:

1. decode as today; `a.Dir = fileDir`;
   `a.Sources = []SourceFile{{Path: AgentFileName, Data: bytes.Clone(data)}}`;
2. `applyDefaults`, `resolve` as today. In `resolveSystemFile`, in the
   `default:` case (file read and not empty) also append
   `SourceFile{Path: filepath.ToSlash(filepath.Clean(a.SystemFile)), Data: content}`;
3. `a.Dir = workDir`; then `Validate` as today.

`Load` calls `parse(data, dir, dir)`; `Parse` calls `parse(data, "", "")`;
`LoadAt` reads the file like `Load`, computes `wd, err := filepath.Abs(workDir)`
and calls `parse(data, filepath.Dir(abs), wd)`, with the same error prefix as
`Load` (`agentconfig: <path>: ...`).

**Policy files (task C.2).** If C.2 (`policy.include`, `internal/agentconfig/include.go`)
is merged when you start, policy files are part of the version too: give
`includeLoader` a `sources []SourceFile` field; in `load`, right after the
successful `os.ReadFile(f)` (C.2 step 5), append
`SourceFile{Path: rel, Data: data}` where `rel, err := filepath.Rel(baseOfAgent, f)`
made `filepath.ToSlash` (`baseOfAgent` = the agent file's directory, i.e. `a.Dir`
while `resolve` runs; if `Rel` fails use `filepath.ToSlash(f)`); at the end of
`resolvePolicyIncludes` append `l.sources` to `a.Sources`. Includes outside the
agent's directory then get a `../` path, which `Register` refuses. Test it with
`TestDigestCoversPolicyIncludes` (changing one byte of an included file changes
the digest). If C.2 is not merged, skip this paragraph and say so in the PR.

**What the digest does not cover:** values taken from the environment
(`${ADLC_ANTHROPIC_MODEL}`, prices). Two runs of one version with different
model variables have the same digest. Say this in the PR's Notes.

### Eval report proves the version (`internal/evals/runner.go`)

`evals.Result` does not record which file content produced it, so add, after `Agent`:

```go
	AgentDigest  string             `json:"agent_digest,omitempty"` // agentconfig.(*Agent).Digest() of the agent file (task C.4)
```

and set `AgentDigest: agent.Digest()` in the `Result` literal in `RunOne`.
Reports written before this task have no `agent_digest`; promotion refuses them.

### Schema (append to `migrations` in `internal/controlplane/store.go`)

```sql
CREATE TABLE agent_versions (
	name              TEXT    NOT NULL CHECK (name <> ''),
	version           INTEGER NOT NULL CHECK (version > 0),
	agent_id          TEXT    NOT NULL,
	digest            TEXT    NOT NULL UNIQUE,
	source_dir        TEXT    NOT NULL,
	created_unix_nano INTEGER NOT NULL,
	PRIMARY KEY (name, version)
) WITHOUT ROWID;
CREATE TABLE agent_files (
	name    TEXT    NOT NULL,
	version INTEGER NOT NULL,
	path    TEXT    NOT NULL CHECK (path <> ''),
	data    BLOB    NOT NULL,
	PRIMARY KEY (name, version, path)
) WITHOUT ROWID;
CREATE TABLE promotions (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	name               TEXT    NOT NULL,
	version            INTEGER NOT NULL CHECK (version > 0),
	env                TEXT    NOT NULL CHECK (env IN ('staging', 'prod')),
	report_path        TEXT    NOT NULL,
	pass_at_1          REAL    NOT NULL,
	runs               INTEGER NOT NULL,
	median_usd_micros  INTEGER NOT NULL,
	promoted_unix_nano INTEGER NOT NULL
);
CREATE TRIGGER agent_versions_no_update BEFORE UPDATE ON agent_versions BEGIN SELECT RAISE(ABORT, 'agent versions are immutable'); END;
CREATE TRIGGER agent_versions_no_delete BEFORE DELETE ON agent_versions BEGIN SELECT RAISE(ABORT, 'agent versions are immutable'); END;
CREATE TRIGGER agent_files_no_update BEFORE UPDATE ON agent_files BEGIN SELECT RAISE(ABORT, 'agent versions are immutable'); END;
CREATE TRIGGER agent_files_no_delete BEFORE DELETE ON agent_files BEGIN SELECT RAISE(ABORT, 'agent versions are immutable'); END;
CREATE TRIGGER promotions_no_update BEFORE UPDATE ON promotions BEGIN SELECT RAISE(ABORT, 'promotions are append-only'); END;
CREATE TRIGGER promotions_no_delete BEFORE DELETE ON promotions BEGIN SELECT RAISE(ABORT, 'promotions are append-only'); END;
```

`agent_id` is the file's `name@version` (`a.AgentID()`). The current version
of an environment is the newest promotions row: `SELECT version FROM promotions
WHERE name = ? AND env = ? ORDER BY id DESC LIMIT 1`.

### Registry API (`internal/controlplane/registry.go`)

```go
// Env is a promotion target.
type Env string

const (
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// Ref names a registered version: NAME@N (Version > 0) or NAME@ENV (Env set).
type Ref struct {
	Name    string
	Version int
	Env     Env
}

// ParseRef parses ^([a-z][a-z0-9-]{0,31})@([1-9][0-9]{0,8}|staging|prod)$.
func ParseRef(s string) (Ref, error)
// String returns "name@N" or "name@env".
func (r Ref) String() string
// ParseEnv accepts "staging" and "prod".
func ParseEnv(s string) (Env, error)

// AgentVersion is one registered version.
type AgentVersion struct {
	Name      string
	Version   int
	AgentID   string // the file's name@version: Config.AgentID of its runs
	Digest    string
	SourceDir string // absolute directory of the file it was first registered from
	CreatedAt time.Time // UTC
	Envs      []Env  // environments whose current version this is; staging before prod
	Files     []agentconfig.SourceFile // DigestFiles order; nil from Versions
}

// Ref returns "name@N".
func (v AgentVersion) Ref() string

// Register stores a's Sources as a new version, or returns the version that
// already has a.Digest(); created reports which.
func (s *Store) Register(ctx context.Context, a *agentconfig.Agent) (v AgentVersion, created bool, err error)
// Version returns name@version with its files.
func (s *Store) Version(ctx context.Context, name string, version int) (AgentVersion, error)
// Versions returns every version without files, sorted by name, then version.
func (s *Store) Versions(ctx context.Context) ([]AgentVersion, error)
// Resolve returns the version ref names (NAME@N, or the current version of NAME@ENV), with files.
func (s *Store) Resolve(ctx context.Context, ref Ref) (AgentVersion, error)
```

Errors (tests check the full texts):

```go
var (
	ErrInvalidRef        = errors.New("controlplane: invalid agent reference")
	ErrInvalidAgent      = errors.New("controlplane: agent cannot be registered")
	ErrVersionNotFound   = errors.New("controlplane: no such agent version")
	ErrNotPromoted       = errors.New("controlplane: no version promoted")
	ErrReportMismatch    = errors.New("controlplane: report was not produced by this version")
	ErrPromotionRefused  = errors.New("controlplane: promotion refused")
)
```

| Where | Text (`%w` = the sentinel) |
|-------|-----------------------------|
| `ParseRef` | `%w: %q (want NAME@N, NAME@staging or NAME@prod)` |
| `ParseEnv` | `controlplane: unknown environment %q (want staging or prod)` (no sentinel) |
| `Register`, `a.Dir == ""` | `%w: %s was not loaded from a file` (`a.AgentID()`) |
| `Register`, a file not local | `%w: file %q must be a relative path inside the agent file's directory` |
| `Register`, a file named `agent.yaml` | `%w: file %q clashes with the agent file` |
| `Version` | `%w: %s@%d` |
| `Resolve` of an env | `%w: %s@%s` with `ErrNotPromoted` |

`Register` algorithm:

1. `a.Dir == ""` → error above. For `a.Sources[1:]`: `p == AgentFileName` →
   clash error; `!filepath.IsLocal(filepath.FromSlash(p))` → not-local error
   (absolute paths and `../x` are refused: they could not be written back
   inside a directory).
2. `d := a.Digest()`.
3. Begin a transaction (`_txlock=immediate` from C.3 makes it a write lock).
   `SELECT name, version FROM agent_versions WHERE digest = ?`. Found →
   `tx.Rollback()`, return `s.Version(ctx, name, version)` with `created = false`.
4. `SELECT COALESCE(MAX(version), 0) FROM agent_versions WHERE name = ?` → `n`.
5. Insert `agent_versions(a.Name, n+1, a.AgentID(), d, a.Dir, s.now().UnixNano())`
   and one `agent_files` row per `a.Sources` entry. Commit.
6. Return `s.Version(ctx, a.Name, n+1)` with `created = true`.

`Version`: read the row (`sql.ErrNoRows` → not found), the files
(`ORDER BY path`, then put `agent.yaml` first), and `Envs` (for `staging`
then `prod`: current version of that env == this version).

### Promotion (`internal/controlplane/promote.go`)

```go
// Gate is what an eval report must show for a promotion.
type Gate struct {
	MinPassAt1         float64 // 0..1
	MaxMedianUSDMicros int64   // < 0 = no limit
}

// Promotion is one recorded promotion.
type Promotion struct {
	Name            string
	Version         int
	Env             Env
	ReportPath      string
	PassAt1         float64
	Runs            int
	MedianUSDMicros int64
	PromotedAt      time.Time
}

// Promote makes name@version the current version of env if rep proves it
// meets g. reportPath is only recorded.
func (s *Store) Promote(ctx context.Context, name string, version int, env Env,
	rep evals.Report, reportPath string, g Gate) (Promotion, error)
```

`Promote` algorithm (`ref` = `v.Ref()`; `usd(m)` = `fmt.Sprintf("%d.%06d", m/1_000_000, m%1_000_000)`):

1. `g.MinPassAt1 < 0 || g.MinPassAt1 > 1` →
   `controlplane: min pass@1 must be between 0 and 1, got %v`.
2. `v, err := s.Version(ctx, name, version)`.
3. `results` = the entries of `rep.Results` with `AgentDigest == v.Digest`
   (results of other agents in the same report are ignored). None →
   `%w: no result in the report has agent_digest %s (%s)` (`ErrReportMismatch`, `v.Digest`, `ref`).
4. No entry of `results` has `Error == ""` →
   `%w: all %d results of %s in the report have errors` (`ErrPromotionRefused`).
5. `ps := evals.Summarize(results).ByProvider[results[0].Provider]` (one
   digest = one agent file = one provider, so there is exactly one key).
6. `ps.PassAt1 < g.MinPassAt1` →
   `%w: %s: pass@1 %.3f over %d runs is below the minimum %.3f` (`ps.Runs`).
7. `g.MaxMedianUSDMicros >= 0 && ps.MedianUSDMicrosSolved > g.MaxMedianUSDMicros` →
   `%w: %s: median cost of solved runs $%s is above the maximum $%s`.
8. Insert the promotions row (`s.now()`), return the `Promotion`.

Example: `controlplane: promotion refused: hello@1: pass@1 0.333 over 3 runs is below the minimum 0.500`.

### Running a registered version (`internal/cli/agentref.go`, `run.go`, `resume.go`)

Add `agentDir string` to `runOptions` and to `adlc run` only:
`f.StringVar(&o.agentDir, "agent-dir", "", "for NAME@N or NAME@ENV: directory that tool commands, hooks and sandbox.workspace are resolved against (default: the directory the version was registered from)")`.
Change `run`'s `Use` to `run AGENT_YAML|NAME@N|NAME@ENV --task TEXT`.

```go
// resolveAgentRef turns a registry reference in o.agentPath into a file: it
// writes the version's files into a new temporary directory and returns
// options whose agentPath is <dir>/agent.yaml and whose agentDir is the
// version's SourceDir unless --agent-dir was given. cleanup removes the
// directory. A path that exists on disk is always a file.
func resolveAgentRef(ctx context.Context, o runOptions) (out runOptions, cleanup func(), err error)
```

1. `os.Stat(o.agentPath)` succeeds → if `o.agentDir != ""` return
   `errors.New("run: --agent-dir only applies to a registered agent (NAME@N or NAME@ENV)")`;
   else return `o, func() {}, nil`.
2. `ref, err := controlplane.ParseRef(o.agentPath)`; error → same `--agent-dir`
   check, then return `o, func() {}, nil` (the normal load reports the missing file).
3. `store, err := openExistingControlDB(ctx, o.controlDB)`; `defer store.Close()`.
4. `v, err := store.Resolve(ctx, ref)`.
5. `dir, err := os.MkdirTemp("", "adlc-agent-")`; `materialize(dir, v.Files)`: for
   each file, `filepath.IsLocal(filepath.FromSlash(f.Path))` else
   `fmt.Errorf("run: %s: stored file %q is not a relative path", v.Ref(), f.Path)`;
   `os.MkdirAll` its parent; `os.WriteFile(..., 0o644)`. On error remove `dir`.
6. `o.agentPath = filepath.Join(dir, agentconfig.AgentFileName)`; if
   `o.agentDir == ""` set `o.agentDir = v.SourceDir`.

In `loadAgent`, replace `agentconfig.Load(o.agentPath)` by
`agentconfig.LoadAt(o.agentPath, o.agentDir)` when `o.agentDir != ""`.
In `runAgentWith` (first statement) and in `resumeAgent` (after
`resumeAgentPath`, before `loadAgent`):

```go
	o, cleanupRef, err := resolveAgentRef(ctx, o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer cleanupRef()
```

The harness `AgentID` of such a run stays the file's `name@version`
(`hello@1` even for registry version `hello@3`), so `adlc resume RUN_ID
hello@3` matches the run and C.3 agent kills by name keep working. The
registry number is only a registry address.

### `adlc agent` (`internal/cli/agent.go`)

`newAgentCmd()` returns `agent` with a persistent flag `--control-db`
(default `defaultControlDBPath`) and four subcommands. All errors exit 1
(`&exitError{code: 1, err: err}`).

| Subcommand | Behaviour | stdout |
|------------|-----------|--------|
| `register AGENT_YAML [--model-name X]` (`ExactArgs(1)`) | `loadAgent(runOptions{agentPath, modelName})` (env variables the file names must be set or replaced by `--model-name`), `openControlDB`, `Register` | new: `registered hello@1 (sha256:<hex>)\n`; existing: `already registered as hello@1 (sha256:<hex>)\n` |
| `list` (`NoArgs`) | missing db → `no registered agents\n` (never create it); else `Versions` | table: `tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)`, header `VERSION\tAGENT_ID\tDIGEST\tENVS\tREGISTERED`; DIGEST = first 19 characters (`sha256:` + 12 hex); ENVS joined with `,` or `-`; REGISTERED = RFC3339 UTC; no rows → `no registered agents\n` |
| `show REF` (`ExactArgs(1)`) | `ParseRef`, `openExistingControlDB`, `Resolve` | lines below |
| `promote REF --to ENV --report PATH [--min-pass-at-1 F] [--max-median-usd X]` | below | `promoted hello@1 to staging (pass@1 0.667 over 3 runs, median $0.120000)\n` (`formatUSD`) |

`show` prints, each label padded with `%-12s`:

```
version:    hello@2
agent id:   hello@1
digest:     sha256:<full hex>
registered: 2026-11-13T10:00:00Z
source dir: /abs/dir
envs:       staging,prod          (or -)
file:       agent.yaml (412 bytes)
file:       system.md (88 bytes)
```

`promote`: `--to` and `--report` are required (`MarkFlagRequired`);
`--min-pass-at-1` float, default `0.5`; `--max-median-usd` string, default `""`
(= `MaxMedianUSDMicros: -1`), else `agentconfig.ParseMicroUSD` (error
`agent promote: --max-median-usd: %w`). Steps: `ParseRef`; a ref with an env
→ `fmt.Errorf("agent promote: want NAME@N, got %q", arg)`; `ParseEnv(--to)`;
`readReport(--report)` (from `eval_report.go`); `filepath.Abs(--report)`;
`openExistingControlDB`; `Promote`.

### `interfaces.md` additions

§7, after the Go entry points:

```go
const AgentFileName = "agent.yaml"                    // task C.4
type SourceFile struct{ Path string; Data []byte }     // Agent.Sources: agent file first, then system_file
func DigestFiles(files []SourceFile) string            // "sha256:<hex>", algorithm in brief C.4
func (a *Agent) Digest() string
func LoadAt(path, workDir string) (*Agent, error)      // Agent.Dir = workDir; system_file relative to the file
```

§9: "`Result.AgentDigest` (`agent_digest`, task C.4) is the digest of the agent file that produced the result."
§11: `Env`, `Ref`, `ParseRef`, `ParseEnv`, `AgentVersion`, `Register`, `Version`,
`Versions`, `Resolve`, `Gate`, `Promotion`, `Promote` and the six errors, as above.

## Steps

1. Rebase on `main` with C.3 merged; branch.
2. `agentconfig`: `sources.go`, the `parse` change, `sources_test.go`; `go test ./internal/agentconfig/` (old tests unchanged).
3. `evals`: `AgentDigest`; `go test ./internal/evals/`.
4. `controlplane`: migration 2, `registry.go`, `registry_test.go`.
5. `controlplane`: `promote.go`, `promote_test.go`.
6. CLI: `agentref.go`, `run.go`/`resume.go` changes; `agent.go`; root; `agent_test.go`.
7. `interfaces.md`; `make fmt && make ci`.

## Tests

`internal/agentconfig/sources_test.go`:

| Test | Asserts |
|------|---------|
| `TestDigestFilesVectors` | the three vectors above |
| `TestDigestFilesOrder` | `[agent.yaml, b, a]`, `[a, agent.yaml, b]` and `[agent.yaml, a, b]` give one digest |
| `TestLoadSetsSources` | `Load("testdata/system-file.yaml")` → `Sources` = `{agent.yaml, file bytes}`, `{prompt.md, bytes of testdata/prompt.md}` |
| `TestParseSetsSources` | `Parse(data)` → `Sources == [{agent.yaml, data}]` |
| `TestDigestTracksContentNotName` | copy agent + prompt to a temp dir: renaming the agent file keeps the digest; changing one byte of the prompt changes it |
| `TestLoadAtUsesWorkDir` | agent in dir A with `system_file: p.md` and `sandbox.workspace: ./ws`, `ws/` only in dir B: `Load` fails on `sandbox.workspace`; `LoadAt(A/agent.yaml, B)` succeeds, `Dir` = B (absolute), `System` = A/p.md content, `SandboxWorkspace()` = B/ws |

`internal/evals/runner_test.go`: `TestRunOneRecordsAgentDigest` — `res.AgentDigest == agent.Digest()` and has prefix `sha256:`.

`internal/controlplane/registry_test.go` (agents written to `t.TempDir()` and loaded with `agentconfig.Load`):

| Test | Asserts |
|------|---------|
| `TestRegisterAssignsVersions` | content A → `hello@1` created; content B → `hello@2`; agent `other` → `other@1` |
| `TestRegisterSameContentReturnsExisting` | A twice → `created == false`, version 1; A copied to another dir → version 1, `SourceDir` still the first dir |
| `TestRegisterStoresFiles` | `Version` returns `Files` equal to `a.Sources`, `Digest == a.Digest()`, `AgentID == "hello@1"` |
| `TestRegisterRejectsNonLocalFiles` | `system_file` absolute, and `../p.md` → `ErrInvalidAgent`, exact message |
| `TestRegisterRejectsParsedAgent` | `agentconfig.Parse` result → `ErrInvalidAgent`, `hello@1 was not loaded from a file` |
| `TestVersionNotFound` | → message `controlplane: no such agent version: hello@3` |
| `TestVersionsAreImmutable` | `UPDATE agent_versions ...` and `DELETE FROM agent_files` via `s.db` → errors containing `agent versions are immutable` |
| `TestParseRef` | valid `hello@1`, `hello@staging`, `hello@prod`; invalid `hello`, `hello@0`, `Hello@1`, `hello@dev`, `@1` → `ErrInvalidRef` |
| `TestResolveEnvNotPromoted` | `hello@prod` → `controlplane: no version promoted: hello@prod` |
| `TestMigrateFromV1` | file with only migration 1 applied (`user_version = 1`) and one kill → `Open` → `user_version == 2`, `Active` still has the kill |

`internal/controlplane/promote_test.go` (reports built in Go: `evals.Result{AgentDigest, Provider: "fake", Difficulty: evals.Easy, Grade: &evals.Grade{Solved, USDMicros}}`; errors set `Error` and `Grade: nil`):

| Test | Asserts |
|------|---------|
| `TestPromoteMeetsGate` | 2 of 3 solved, `MinPassAt1: 0.5`, no cost limit → `Promotion{PassAt1: 0.667, Runs: 3}`; `Resolve(hello@staging)` is version 1; `Version(1).Envs == [staging]` |
| `TestPromoteRefusedBelowPassAt1` | 1 of 3 → `ErrPromotionRefused`, exact text `controlplane: promotion refused: hello@1: pass@1 0.333 over 3 runs is below the minimum 0.500`; `Resolve(hello@staging)` still `ErrNotPromoted` |
| `TestPromoteRefusedAboveMedianCost` | solved costs 100000 and 300000 → limit 150000 refused with `median cost of solved runs $0.200000 is above the maximum $0.150000`; limit -1 accepted |
| `TestPromoteRefusesReportOfOtherVersion` | results carry hello@2's digest, promoting hello@1 → `ErrReportMismatch`, message names hello@1's digest and `(hello@1)` |
| `TestPromoteRefusesReportWithoutDigest` | `AgentDigest: ""` → `ErrReportMismatch` |
| `TestPromoteIgnoresOtherAgents` | 2 solved results of hello@1 plus 3 failed results with another digest → pass@1 1.000, accepted |
| `TestPromoteAllErrors` | 2 results with `Error` → `all 2 results of hello@1 in the report have errors` |
| `TestPromoteInvalidMin` | 1.5 → `controlplane: min pass@1 must be between 0 and 1, got 1.5` |
| `TestPromoteLatestWins` | prod ← 1, then prod ← 2 → `Resolve(hello@prod)` = 2; `Version(1).Envs` empty |

`internal/cli/agent_test.go` (always `--control-db` in `t.TempDir()`; agent from `helloDir`; reports written with `evals.WriteReport` using `agentconfig.Load(agent).Digest()`):

| Test | Asserts |
|------|---------|
| `TestAgentRegisterCommand` | first: stdout starts `registered hello@1 (sha256:`; second: starts `already registered as hello@1 (` |
| `TestAgentRegisterChangedContent` | edit the file → `registered hello@2 (` |
| `TestAgentListCommand` | header fields `VERSION AGENT_ID DIGEST ENVS REGISTERED`; row fields 0–3 = `hello@1 hello@1 sha256:<12 hex> -` |
| `TestAgentListMissingDB` | `no registered agents\n`, the file is not created |
| `TestAgentShowCommand` | contains `version:    hello@1\n` and `file:       agent.yaml (`; `show hello@9` → exit 1, `no such agent version: hello@9` |
| `TestAgentPromoteCommand` | 1 solved result costing 10000 → stdout `promoted hello@1 to staging (pass@1 1.000 over 1 runs, median $0.010000)\n`; `show` has `envs:       staging` |
| `TestAgentPromoteRefused` (demo) | 1 of 2 solved with `--min-pass-at-1 0.8` → exit 1, stderr contains `promotion refused: hello@1: pass@1 0.500 over 2 runs is below the minimum 0.800`; `show` has `envs:       -` |
| `TestAgentPromoteNeedsNumberedVersion` | `promote hello@prod ...` → `agent promote: want NAME@N, got "hello@prod"` |
| `TestRunRegisteredVersion` | register, then overwrite the original file with `garbage` → `run hello@1 --task ... --fake-script ... --db ... --control-db ...` exits 0 with `result:  The file says: hello, world` (the stored copy ran; `./workspace` came from `SourceDir`) |
| `TestRunPromotedEnv` | register, promote to staging, `run hello@staging` → exit 0 |
| `TestRunRefWithoutControlDB` | → exit 1, `control db <path> does not exist` |
| `TestRunAgentDirOnlyForRefs` | `run <file> --agent-dir x` → exit 1 with the `--agent-dir only applies` text |
| `TestRunAgentDirOverridesSourceDir` | second dir with `workspace/greeting.txt` = `other\n`; `run hello@1 --agent-dir <it>` → the `tool_call_completed` output contains `other` |
| `TestResumeRegisteredVersion` | `crashedRun` with the file, register it, `resume r1 hello@1` → exit 0, completed |

See every new test fail once by breaking the code (e.g. drop the `0x00`
separators, skip the digest filter in `Promote`).

## Verify

```bash
go test ./internal/agentconfig/ ./internal/evals/ ./internal/controlplane/ -v -run 'Digest|Sources|LoadAt|Register|Version|Ref|Resolve|Promote|Migrate'   # all PASS
go test ./internal/cli/ -run 'Agent|RunRegistered|RunPromoted|RunRef|RunAgentDir|ResumeRegistered' -v                        # all PASS
make build
./bin/adlc agent register examples/hello.yaml --control-db /tmp/adlc-c4/control.db   # registered hello@1 (sha256:...)
./bin/adlc agent register examples/hello.yaml --control-db /tmp/adlc-c4/control.db   # already registered as hello@1 (...)
./bin/adlc agent list --control-db /tmp/adlc-c4/control.db                            # one row, ENVS -
./bin/adlc run hello@1 --task "What does greeting.txt say?" --fake-script examples/hello.fake.json \
  --db /tmp/adlc-c4/events.db --control-db /tmp/adlc-c4/control.db                    # stop: completed
make ci                                                                              # passes
```

## Out of scope

- Promotion order rules (staging before prod), rollback commands, deleting versions.
- Recording the resolved model name or prices in the digest or the report.
- `adlc eval` taking `NAME@N` (evaluate the original file; its digest matches).
- Any server or remote registry (m2.md decision 4).

## Pitfalls

- Hash the bytes read from disk, never re-encoded YAML or the resolved `Agent`.
- `Sources` keeps `system_file` as written (`prompt.md`), not the absolute path,
  or the digest would depend on where the repository is checked out.
- Do not change migration 1; append migration 2. Test the upgrade from v1.
- `Load` must keep reading `system_file` relative to the file and checking
  `sandbox.workspace` against `Dir`; only `LoadAt` separates the two. Switch
  `a.Dir` to `workDir` after `resolve` and before `Validate`.
- A path that exists on disk is a file even if it looks like `name@1`.
- The registry number N is not the file's `version:`; the run's `AgentID`
  stays the file's. Do not overwrite `Config.AgentID` with `name@N`.
- `evals.Result` is also changed by M2-Q tasks; add the one field and touch
  nothing else in the struct, to keep merges simple.
- `list` and `show` must not create the control db; only `register` does.

## Done checklist

- [ ] `agentconfig`: `Sources`, `DigestFiles`, `Digest`, `LoadAt`; test vectors pass
- [ ] `evals.Result.AgentDigest` written by `RunOne`
- [ ] Migration 2 with immutable versions and append-only promotions; upgrade from v1 tested
- [ ] `Register` (same content → same version), `Version`, `Versions`, `Resolve`, `Promote` with the gate
- [ ] `adlc agent register|list|show|promote`; `adlc run`/`resume` accept `NAME@N` and `NAME@ENV`
- [ ] Demo: `adlc agent promote` refuses a report below the threshold
- [ ] `interfaces.md` §7, §9, §11 updated
- [ ] `make ci` passes
