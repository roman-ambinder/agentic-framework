# Record the sandbox image digest — design

Status: draft, builder-checked. Approach B and promotion rule 1 chosen by the owner on 2026-11-14.
Gap 3 of the AX comparison (pin images by digest), scaled to adlc.

## 1. Intent

Two "identical" eval runs, or a promotion and the evals behind it, must not
quietly use different images. Today every default is a moving tag
(`agentconfig.DefaultSandboxImage = "golang:1.26"`, `evals.DefaultImage =
"golang:1.26"`), and nothing but the pause snapshot records which image a run
actually ran on. Promotion (`controlplane.Promote`) checks only that eval
results carry the version's `agent_digest`.

This design keeps tags in agent files and on the command line, but resolves
each sandbox image to its content ID when the sandbox is created and records
that ID everywhere evidence is kept.

## 2. Goals, non-goals, anti-goals

**Goals**
- G1. Every run that creates a sandbox records the image reference it was
  given and the image ID it got, in its `run_started` event.
- G2. Every eval result records the image ID its sandbox ran on.
- G3. `adlc eval-compare` warns when the compared results used different
  images, or when one report mixes images.
- G4. `adlc promote` refuses evidence whose gating results used more than one
  image, and records the image ID with the promotion.

**Non-goals**
- Rejecting tags (approach A) or a lock file (approach C).
- Resolving "the image a promoted version will run with" (promotion rule 2):
  nothing deploys promoted versions yet.
- Registry manifest digests (`RepoDigests`): the local image ID is what a
  container actually runs and is always present, also for locally built
  images.

**Anti-goals**
- An existing report (written before this change, no `image_id`) that could
  be promoted today can no longer be promoted.
- A run or eval that worked before fails because the image ID cannot be
  read.

## 3. Constraints

- Protobuf: add only. `RunStarted` gains field 9; nothing renumbered.
- `eventlog.RunState` gains no field (its `Digest()` would change for every
  run); the image stays in the event only.
- `controlplane` schema: add a migration (append to `migrations`), never edit
  an existing one.
- No new dependencies. `docker.Backend` already calls `ImageInspect` in
  `ensureImage`.
- Tests never use the network; Docker tests stay behind `//go:build docker`.

## 4. Approaches

| | Approach | Trade-off |
|---|---|---|
| A | Require digests in agent files and `--image` | Strongest, but every file and default changes and updating an image becomes manual |
| **B (chosen)** | Keep tags; resolve and record the image ID at sandbox creation; compare and gate on it | No friction; every result says what it ran on; one schema field |
| C | Lock file written by `adlc lock` | Reproducible without editing agent files, but a new file and command to keep in sync |

Promotion rule: **1 (chosen)**, all gating results must share one image ID,
which the promotion records. Rule 2 (also compare against the version's
deployment image) waits until something deploys promoted versions.

## 5. Design

### 5.1 Sandbox: resolving the image

Optional interface in `internal/sandbox`, same pattern as `Suspender`:

```go
// ImageResolver is implemented by backends that can tell which image a
// sandbox runs.
type ImageResolver interface {
    // ImageID returns the content ID ("sha256:<hex>") of the image the
    // sandbox h was created from.
    ImageID(ctx context.Context, h Handle) (string, error)
}
```

`docker.Backend` implements it with `ContainerInspect(h).Image` (the ID of
the image the container really uses, even if the tag moved after creation).
`sandboxtest.Pausable` implements it, returning `"sha256:fake-" + spec.Image`
like its snapshots do.

### 5.2 Event schema

```proto
message RunStarted {
  ...
  // Image of the run's sandbox, if it has one.
  SandboxImage sandbox_image = 9;
}

message SandboxImage {
  // Reference as given, e.g. "golang:1.26".
  string ref = 1;
  // Content ID the reference resolved to, e.g. "sha256:…".
  string id = 2;
}
```

### 5.3 Runs (`adlc run`, `adlc fork`)

- `harness.Config` gains `SandboxImage *adlcv1.SandboxImage`; `loop.go` and
  `fork.go` copy it into `RunStarted`.
- `internal/cli` `startRunSandbox`: after `Create`, if the backend implements
  `ImageResolver`, resolve and keep `{Ref: spec.Image, ID: id}` on
  `runSandbox`; `buildRun`'s caller sets `cfg.SandboxImage`. If resolving
  fails, print `sandbox: could not read the image ID: <err>` and set only
  `Ref` (anti-goal: never fail the run for this).
- `approve`/`resume` write no new `run_started` and need nothing.
- `adlc trace` (or the run summary) prints the image line when present:
  `image: golang:1.26 (sha256:…)`.

### 5.4 Evals

- `evals.Result` gains `ImageID string \`json:"image_id,omitempty"\``.
- The runner resolves the ID right after each `Sandbox.Create` (per result,
  not once per suite: a tag can move mid-suite) when the backend implements
  `ImageResolver`, stores it in the result, and passes the same
  `SandboxImage` into the run's `harness.Config`.
- Resolution failure: empty `ImageID`, no error (anti-goal).
- Grading runs in a second, fresh sandbox (`grade()` in `runner.go`). When
  the agent sandbox's ID was resolved, the grading sandbox is created from
  that ID instead of the tag, so agent and grader always run on the same
  image and one `ImageID` per result is enough. Unresolved: the tag, as today.

### 5.5 `eval-compare`

`PrintComparison` already prints `warning: the reports are from different
suites`. Add, in the same style:
- `warning: the reports used different images: base <ids>, new <ids>` when
  the sets of non-empty image IDs differ;
- `warning: <base|new> report mixes images: <ids>` when one report has more
  than one non-empty image ID.
IDs are printed shortened to 12 hex characters after `sha256:`.

### 5.6 Promotion

In `gateSummary`, after selecting the version's results, the image check
looks only at results with `Error == ""` (errored results may have failed
before a sandbox existed and have no image):
- Two or more different non-empty `ImageID`s → `ErrPromotionRefused`:
  `<ref>: the results used <n> images (<ids>); promote from evidence on one image`.
- One non-empty `ImageID` (empty ones may be present) → allowed, and that
  ID is recorded. If some results have no ID, `adlc promote` prints
  `warning: <k> of <n> results do not say which image they used`.
- No non-empty `ImageID` at all (a report written before this change) →
  allowed; the promotion records `image_id = ""` and `adlc promote` prints
  `warning: the report does not say which image the results used`.
- `Promotion` gains `ImageID string`; migration adds
  `ALTER TABLE promotions ADD COLUMN image_id TEXT NOT NULL DEFAULT ''`.
  `adlc promote` prints it in its output when non-empty (no command lists
  promotions today).

## 6. Calls made by the design author (not decided by the owner)

- Record the local image ID, not the registry digest.
- Resolve per eval result, not once per suite.
- A failure to read the ID never fails a run or eval.
- Reports without `image_id` stay promotable, with a warning; only
  non-errored results are checked, and only two or more different IDs
  refuse.
- The grading sandbox is created from the agent sandbox's resolved image ID.
- `ImageResolver` as an optional interface rather than a change to
  `sandbox.Sandbox`.

## 7. Left to the builder

Task split and briefs; exact wording of the trace/summary line; where the
promotion warning is printed; test helper reuse.
