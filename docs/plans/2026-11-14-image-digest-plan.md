# Record the Sandbox Image Digest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every run and eval result records which image its sandbox actually ran on, `eval-compare` warns about differing images, and `adlc agent promote` refuses evidence from more than one image.

**Architecture:** An optional `sandbox.ImageResolver` reads a sandbox's image ID after `Create`. The CLI and the eval runner put it into `harness.Config.SandboxImage` (copied into `run_started`) and into `evals.Result.ImageID`; compare and promotion read `ImageID`.

**Tech Stack:** Go 1.26, protobuf (`make proto`), `moby/moby/client` (already a dependency), SQLite control DB.

**Spec:** `docs/design/2026-11-14-image-digest-design.md`

## Global Constraints

- Read `AGENTS.md`; all its rules apply. No new dependencies.
- Protobuf add-only: `RunStarted.sandbox_image = 9`, new message `SandboxImage { string ref = 1; string id = 2; }`. Run `make proto`; never edit `gen/`.
- No new `eventlog.RunState` field.
- Control DB: append one migration to `migrations` in `internal/controlplane/store.go`; never edit an existing one.
- Failing to read an image ID never fails a run or an eval (spec anti-goal).
- Short image IDs print as `sha256:` + first 12 hex characters (`sandbox.ShortImageID`, Task 2).
- Branch `claude/wizardly-pascal-gylbmh` (at `e238810`); commit subjects `I.<n>: <summary>`; no AI attribution; commit only after `make ci` passes; `make test-docker` with a running daemon (`docker info` succeeds) before the last commit.

## Review Focus

1. A local image built without a registry (no RepoDigests) still records an ID. Test: Task 2 docker test uses a tag created with `docker tag` on a local image.
2. `ImageID` failing in a run → run still completes, `run_started` has `ref` and empty `id`, stdout warns. Test: Task 3 `TestRunSandboxImageIDFailureDoesNotFailRun`.
3. Grading uses the agent sandbox's resolved ID, not the tag. Test: Task 4 `TestGradeSandboxUsesResolvedImageID`.
4. A report with one errored result (no image) and the rest on one image is still promotable. Test: Task 6 table row "errored result without image".
5. A report written before this change (no `image_id` anywhere) is still promotable, with the warning. Test: Task 6 table row "old report".

---

### Task 1: Schema and harness (I.1)

**Files:** Modify `contracts/proto/adlc/v1/events.proto`, `contracts/contracts_test.go` (sample `run_started` sets `SandboxImage`), `internal/harness/harness.go` (`Config`), `internal/harness/loop.go:83`, `internal/harness/fork.go:128`; Test `internal/harness/run_test.go` (or the file holding run_started tests), `internal/harness/fork_test.go`; `docs/impl/interfaces.md` §3/§6 one line each.

**Interfaces — Produces:** `adlcv1.SandboxImage{Ref, Id}`; field `harness.Config.SandboxImage *adlcv1.SandboxImage` ("image of the run's sandbox, recorded in run_started; nil = none").

- [ ] **Step 1:** Add the proto message and field (comments as design §5.2), `make proto`.
- [ ] **Step 2:** Write `TestRunStartedRecordsSandboxImage` (Run with `cfg.SandboxImage = &adlcv1.SandboxImage{Ref: "golang:1.26", Id: "sha256:abc"}` → first event's `RunStarted.SandboxImage` proto-equal) and `TestForkRecordsSandboxImage` (same for `harness.Fork`), plus nil → nil.
- [ ] **Step 3:** Run `go test ./internal/harness/ -run 'SandboxImage' -v`. Expected: FAIL (field not copied).
- [ ] **Step 4:** Copy `l.cfg.SandboxImage` into both `RunStarted` literals. **Step 5:** PASS; `make ci`; commit `I.1: record the sandbox image in run_started`.

### Task 2: `sandbox.ImageResolver` (I.2)

**Files:** Modify `internal/sandbox/sandbox.go`; Create `internal/sandbox/docker/image.go`, `internal/sandbox/docker/image_integration_test.go` (`//go:build docker`); Modify `internal/sandbox/sandboxtest/pausable.go`; Test `internal/sandbox/sandbox_test.go`, `internal/sandbox/sandboxtest/pausable_test.go`; `docs/impl/interfaces.md` §8.

**Interfaces — Produces:**
- `type ImageResolver interface { ImageID(ctx context.Context, h Handle) (string, error) }` (doc as design §5.1).
- `func ShortImageID(id string) string`: `"sha256:<64 hex>"` → `"sha256:<first 12 hex>"`; anything else returned unchanged.
- `(*docker.Backend).ImageID`: `ContainerInspect(h.ID)` → `.Image`; not found → wrap `sandbox.ErrNotFound`. `var _ sandbox.ImageResolver = (*Backend)(nil)`.
- `(*sandboxtest.Pausable).ImageID`: live handle → `"sha256:fake-" + spec.Image`; destroyed → `sandbox.ErrNotFound`; field `ImageIDErr error` forces a failure.

- [ ] **Step 1:** Write `TestShortImageID` (table: full sha256 id → 12 hex; short/other strings unchanged), `TestPausableImageID` (live, destroyed, forced error), docker `TestImageIDOfLocallyTaggedImage` (`docker tag busybox adlc-test-local:1` via the client's `ImageTag`, Create with that ref, `ImageID` equals `ImageInspect("adlc-test-local:1").ID`; remove the tag at cleanup).
- [ ] **Step 2:** Run `go test ./internal/sandbox/... -run 'ShortImageID|PausableImageID' -v` and `go test -tags docker ./internal/sandbox/docker/ -run ImageID -v`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement. **Step 4:** PASS. **Step 5:** `make ci`; commit `I.2: sandbox.ImageResolver and ShortImageID`.

### Task 3: Runs record their image (I.3)

**Files:** Modify `internal/cli/run_sandbox.go` (`startRunSandbox`, `runSandbox` gains `image *adlcv1.SandboxImage`), `internal/cli/run.go` and `internal/cli/fork.go` (set `cfg.SandboxImage = sb.image` after `buildRun`); Test `internal/cli/run_sandbox_test.go`.

**Interfaces — Consumes:** Task 1 `Config.SandboxImage`; Task 2 `ImageResolver`, `ShortImageID`, `Pausable.ImageIDErr`.

Behaviour: after `Create`, if the backend implements `sandbox.ImageResolver`: success → `image = {Ref: spec.Image, Id: id}` and print `sandbox: image <ref> (<ShortImageID(id)>)`; failure → `image = {Ref: spec.Image}` and print `sandbox: could not read the image ID: <err>`. Backend without the interface → `{Ref: spec.Image}`, no line. A nil `*runSandbox` gives nil image.

- [ ] **Step 1:** Write `TestRunSandboxRecordsImage` (pausableRun → `run_started.sandbox_image` = `{Ref: "golang:1.26", Id: "sha256:fake-golang:1.26"}`, stdout has the image line) and `TestRunSandboxImageIDFailureDoesNotFailRun` (`ImageIDErr` set → run still reaches awaiting approval, `Id` empty, warning line).
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'TestRunSandbox(RecordsImage|ImageIDFailure)' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement. **Step 4:** PASS. **Step 5:** `make ci`; commit `I.3: adlc run and fork record the sandbox image`.

### Task 4: Eval results record their image (I.4)

**Files:** Modify `internal/evals/runner.go` (`Result.ImageID string \`json:"image_id,omitempty"\``, `AgentRun` gains `ImageRef, ImageID string`, `run` and `grade`), `internal/cli/eval.go` (`evalRunAgent` sets `cfg.SandboxImage` from `ar.ImageRef`/`ar.ImageID` when `ImageRef != ""`); Test `internal/evals/runner_test.go`, `internal/cli/eval_test.go`.

Behaviour in `run`: right after `Create`, if `r.Sandbox` implements `sandbox.ImageResolver`, `res.ImageID, err = ImageID(h)`; on error log with `r.logf("read image id of %s: %v", h.ID, err)` and leave it empty. Pass `ImageRef: image, ImageID: res.ImageID` in `AgentRun`. Before calling `grade`, if `res.ImageID != ""` set `spec.Image = res.ImageID`.

- [ ] **Step 1:** Write `TestResultRecordsImageID` (fake sandbox implementing ImageResolver → `res.ImageID` set), `TestResultImageIDFailureLeavesEmpty` (resolver errors → result still graded, `ImageID == ""`), `TestGradeSandboxUsesResolvedImageID` (the second `Create` call's spec has `Image == res.ImageID`), and in cli `TestEvalRunAgentPassesSandboxImage` if `evalRunAgent` can be exercised without a real model; otherwise assert via a unit test of the small helper that maps `AgentRun` to `*adlcv1.SandboxImage`.
- [ ] **Step 2:** Run `go test ./internal/evals/ -run 'ImageID|GradeSandboxUsesResolved' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement (the existing fake sandbox in `internal/evals` tests gains an `ImageID` method if needed). **Step 4:** PASS. **Step 5:** `make ci`; commit `I.4: eval results record the sandbox image`.

### Task 5: `eval-compare` warns about images (I.5)

**Files:** Modify `internal/evals/compare.go` (`ReportInfo` gains `Images []string \`json:"images,omitempty"\``: sorted unique non-empty `ImageID`s; `infoOf` fills it; `PrintComparison` prints the warnings); Test `internal/evals/compare_test.go`.

Exact lines (design §5.5), after the suite warning, IDs via `sandbox.ShortImageID` joined with `, `:
- `warning: the reports used different images: base <ids>, new <ids>` when the two `Images` slices differ and both are non-empty;
- `warning: base report mixes images: <ids>` / `warning: new report mixes images: <ids>` when that side has more than one.

- [ ] **Step 1:** Write `TestPrintComparisonImageWarnings` (table: same single image → no warning; different → "used different images"; base with two → "base report mixes images"; one side without images → no warning).
- [ ] **Step 2:** Run `go test ./internal/evals/ -run TestPrintComparisonImageWarnings -v`. Expected: FAIL.
- [ ] **Step 3:** Implement. **Step 4:** PASS. **Step 5:** `make ci`; commit `I.5: eval-compare warns when images differ`.

### Task 6: Promotion gate and record (I.6)

**Files:** Modify `internal/controlplane/promote.go` (`gateSummary`, `Promotion`), `internal/controlplane/store.go` (append migration), `internal/cli/agent.go` (promote output); Test `internal/controlplane/promote_test.go`, `internal/cli/agent_test.go`; `docs/impl/interfaces.md` §11; work-plan status row `I.1–I.6`.

**Interfaces — Produces:** `Promotion` gains `ImageID string` and `ResultsWithoutImage int` (non-errored results of the version with empty `ImageID`). Migration: `ALTER TABLE promotions ADD COLUMN image_id TEXT NOT NULL DEFAULT ''`; the INSERT writes it.

Rule over the version's results with `Error == ""` (design §5.6): ≥2 distinct non-empty IDs → `ErrPromotionRefused` with `%s: the results used %d images (%s); promote from evidence on one image` (ref, count, short IDs joined by `, `); otherwise `ImageID` = the one ID or `""`.

CLI output after the existing `promoted …` line:
- `ImageID != ""` → `image: <ShortImageID>`; plus, when `ResultsWithoutImage > 0`, `warning: <k> of <n> results do not say which image they used`;
- `ImageID == ""` → `warning: the report does not say which image the results used`.

- [ ] **Step 1:** Write `TestPromoteImageRule` (table: one image → promoted with that ID; two images → refused, message contains `used 2 images`; errored result without image + others on one image → promoted; old report with no IDs → promoted, `ImageID ""`), `TestPromoteRecordsImageID` (read back the row's `image_id`), `TestMigrationAddsPromotionImageID` (a DB at the previous `user_version` opens and has the column), and in cli `TestAgentPromotePrintsImage` (both warning forms).
- [ ] **Step 2:** Run `go test ./internal/controlplane/ ./internal/cli/ -run 'PromoteImage|PromoteRecordsImageID|MigrationAddsPromotionImageID|AgentPromotePrintsImage' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** `make ci` and `make test-docker` (daemon running); update docs; commit `I.6: promotion refuses evidence from more than one image`.
