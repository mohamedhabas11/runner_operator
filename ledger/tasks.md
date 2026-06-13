# runner-operator — Ledger

## Session Log

### Session 1 — Project Scaffolding & CRDs

**Work Done:**
- Initialized Kubebuilder v4.14.0 project (`go.kubebuilder.io/v4`, domain `runner-operator.io`)
- Defined **Runner** CRD (`runners.runner-operator.io/v1alpha1`):
  - Spec: Image, Env, EnvFrom, Volumes, Mounts, Args, Command, TimeoutAfter
  - Status: Phase, Conditions, ResourceHash (drift tracking), StartTime, CompletionTime
- Defined **Workflow** CRD (same group/version):
  - Spec: Steps with Name, Image/RunnerRef, Command, Args, Env, DependsOn, When (`on_success`/`on_failure`/`always`), Retry (MaxRetries + BackoffConfig), Timeout
  - Status: Phase, StepStatuses, Conditions
- Implemented `kustomize` with `vars` for namespace injection
- Created `requirements.md` at `arch/requirments.md`

### Session 2 — Controllers & Tests

**Work Done:**
- Implemented **Runner controller** (`internal/controller/runner_controller.go`):
  - Creates `batch/v1.Job` from Runner spec
  - SHA-256 spec hash drift detection (recreates Job on change)
  - Finalizer for Job cleanup
  - Status reconciliation (Job phase → Runner phase)
- Implemented **Workflow controller** (`internal/controller/workflow_controller.go`):
  - DAG evaluation via `dependsOn` / `when` conditions
  - Creates Runner resources per step (with owner references)
  - Step status tracking (Pending → Running → Succeeded/Failed)
  - Finalizer for owned Runner cleanup
- Wrote unit tests (`internal/controller/*_test.go`) — passing via `make test`
- Wrote **E2E tests** (`test/e2e/e2e_test.go`) — 4/4 specs passing on Kind:
  1. Manager pod is running
  2. Metrics endpoint is available
  3. Runner → Job → Succeeded lifecycle
  4. Workflow chained steps → Succeeded
- Set up CI workflows (`.github/workflows/`):
  - `test.yml` — unit tests
  - `test-e2e.yml` — E2E on Kind
  - `lint.yml` — golangci-lint
- Added `bin/` to `.gitignore`
- Committed and pushed to `main`

**Key Decisions:**
- Runner maps to `batch/v1.Job` (not bare Pods) for built-in retry/backoff/completion
- Controller uses OwnerReferences + K8s GC for cleanup (no finalizers)
- Workflow `when` uses string keywords (no complex expression evaluation)
- Removed `Multiplier` from BackoffConfig to avoid CRD float serialization issues; backoff uses fixed doubling
- E2E tests use separate `runner-operator-test` namespace (no restricted security policy)

### Session 3 — Code Review (Gemini)

**Work Done:**
- Invoked Gemini CLI to scan full project source
- Received 9-item prioritized improvement plan
- Created this ledger (`ledger/tasks.md`)

### Session 4 — Fix Gemini Review Findings

**Work Done:**
- **P0: Fixed retry logic** — `runnerPhaseToStepPhase` now does honest phase mapping. Retry handling moved to `reconcileSteps`: deletes failed Runner, increments `RetryCount`, sets phase to `StepPhaseRunning`
- **P0: Switched to Status().Patch** — Both controllers now use `client.MergeFrom` + `Status().Patch` instead of `Status().Update`, eliminating 409 conflict errors
- **P0: Added SecurityContext to Jobs** — Pod `SecurityContext` (RunAsNonRoot, Seccomp) and container `SecurityContext` (no privilege escalation, read-only rootfs, drop all caps) on all created Jobs
- **P1: Fixed destructive drift** — Runner controller now skips Job deletion when Job is running (has `StartTime` but no `CompletionTime`)
- **P1: Removed redundant finalizers** — Both controllers rely on OwnerReferences + K8s GC for cleanup. Removed `RunnerFinalizer`, `WorkflowFinalizer`, `reconcileDelete` functions, and finalizer RBAC markers
- **P2: Removed polling requeue** — Workflow controller no longer returns `RequeueAfter: 10s`; relies on `Owns(Runner)` for event-driven reconciliation
- **P2: Added resource limits** — Added `Resources corev1.ResourceRequirements` to `RunnerSpec`, wired into Job container spec

**Changes to notes:**
- Key decision updated: "Controller uses OwnerReferences + K8s GC for cleanup (no finalizers)"

---

## Completed Tasks

### P0 — Critical ✓

- [x] **Fix retry logic** — Increment `RetryCount` on failure; delete failed Runner to allow recreation
- [x] **Switch to Status().Patch** — Replace `Status().Update` with `Status().Patch` to avoid 409 conflicts
- [x] **Add SecurityContext to Jobs** — `runAsNonRoot`, `allowPrivilegeEscalation=false`, drop caps, seccomp

### P1 — Important ✓

- [x] **Fix destructive drift** — Don't kill running Jobs on non-critical spec changes
- [x] **Remove redundant finalizers** — Rely on OwnerReferences + GC instead

### P2 — Nice to Have ✓

- [x] **Remove polling requeue** — Workflow controller: stop 10s requeue, use event-driven only
- [x] **Add resource limits** — Expose CPU/Memory `ResourceRequirements` in RunnerSpec

### Session 5 — Gemini Re-review + Follow-up Fixes

**Work Done:**
- Re-invoked Gemini for a follow-up review of the fixed code
- Fixed **Workflow status persistence** — StartTime/Phase/CompletionTime changes now correctly set `updated = true` to trigger a patch
- Fixed **redundant pointer comparison** — Added `metav1TimePtrEqual` helper to compare `*metav1.Time` by value instead of pointer, eliminating redundant patches
- Fixed **CompletedAt on retry** — `upsertStepStatus` now clears `CompletedAt` when a step transitions back to Running (retry)
- Fixed **RunnerRef logic** — `buildStepRunner` now fetches the referenced Runner template and uses its `Spec` as a base, with step-level fields overriding (instead of using the Runner name as image)

**Gemini second review verdict:** "The operator is much more robust" — all previous critical issues resolved.

### Session 5 — Code Review & Improvement Plan

**Work Done:**
- Performed comprehensive codebase scan via `opencode`
- Identified 20 improvement areas across API design, controller logic, observability, testing, and code quality
- Prioritized top 5 most impactful: Conditions population, validating webhooks, timeout enforcement, retry backoff, events + ObservedGeneration

### Session 6 & 7 — Implement Improvements & E2E Tests (opencode)

**Work Done:**
- **Populated `ObservedGeneration`** in both `RunnerStatus` and `WorkflowStatus`, set on every successful reconciliation
- **Added Event recording** to both controllers:
  - Runner: JobCreated, SpecDrift, PhaseChanged
  - Workflow: NoSteps, CycleDetected, TimedOut, PhaseChanged, StepRunnerCreated, StepRetrying
- **Enforced Workflow timeout** — `handleTimeout()` marks all incomplete steps as Failed, sets workflow phase to Failed, and patches CompletionTime
- **Implemented retry backoff** — `retryBackoffElapsed()` computes exponential backoff (`InitialDelay * 2^retryCount`, capped at `MaxDelay`) and waits before retrying
- **Fixed `When` docs** — Changed from incorrect `"success()"`/`"always()"` examples to actual accepted values `"on_success"`, `"on_failure"`, `"always"`
- **Added step name sanitization** — `sanitizeStepName()` lowercases, replaces invalid chars, truncates to 50 chars for safe K8s resource names
- **Fixed retry phase** — Changed from `StepPhaseRunning` to `StepPhasePending` when retrying (Runner hasn't started yet)
- **Added DAG cycle detection** — `detectCycle()` uses DFS to find dependency cycles and surfaces via events
- **Made dev logging configurable** — Changed `--zap-devel` flag (defaults to `false`), controlled via `Development` option
- **Added RBAC markers** for `events.k8s.io` on both controllers

**Test Results:** `make test` — 2/2 passing, coverage 41.2% (up from 32.4%)
**Lint:** `make lint-fix` — 0 issues

### Session 7 — Improve E2E Tests

**Work Done:**
- **Fixed cleanup** — Added ClusterRoleBinding deletion in `AfterAll` to prevent resource leaks
- **Added Runner failure test** (`RunnerFailure` context) — Creates a Runner that exits non-zero (`exit 1`), verifies it reaches `Failed` phase and the underlying Job reports failure
- **Added spec drift test** (`SpecDrift` context) — Creates a Runner, waits for completion, updates the command, verifies the Job is recreated with a new UID, then verifies the Runner completes successfully with the new spec
- **Added Workflow timeout test** (`WorkflowTimeout` context) — Creates a Workflow with a 10s timeout and a step that runs for 120s, verifies the workflow reaches `Failed` and sets `completionTime`
- **Added Workflow on_failure test** (`WorkflowOnFailure` context) — Creates a Workflow where step-one fails (`exit 1`) and step-two runs with `when: "on_failure"`, verifies step-one fails, step-two succeeds, and the workflow overall is `Failed`
- **Fixed syntax bug** — Added missing closing `)` at end of `Describe(...)` call, which caused a compile error

**E2E compilation:** `go vet -tags=e2e ./test/e2e/` — OK
**Lint:** `make lint-fix` — 0 issues

## Current Session — Bug Fixes & Push

*(session complete — pushed to main)*

## Deferred / Skipped

### P2 — Nice to Have

- [ ] **Improve Conditions** — Add Reason/Message to status conditions for better observability. Low priority; functional behavior unchanged.
- [ ] **Topological sort of steps** — Process steps in dependency order, not slice order.

---

## Next Major Features — Event Triggers, Job Grouping, Webhooks

### Session 8 — Architecture Design (opencode)

**Context:** Three major features requested: event-driven triggers (GitHub webhooks → workflow creation), job grouping (parallel job sets with `needs`), and an HTTP webhook endpoint exposed via Ingress.

**Work Done:**
- Comprehensive gap analysis: 30 GitHub Actions features audited — 5 fully implemented, 7 partially, 18+ missing
- Explored existing infrastructure: no webhook server, no ingress, no HTTP framework, controller-runtime manager on 9443/8081
- Made 4 architectural decisions via engineering judgment (see `arch/blueprint.md` — ADR section updated)

**Engineering Decisions (ADRs):**

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| ADR-5 | Webhook server location | **Integrated** into manager binary on port 8080 | Single deployment, lightweight processing, can extract later if needed |
| ADR-6 | Job grouping model | **Logical groups with shared PVC** — steps within a job share a volume; each step still creates a Runner CR | Minimal change to existing architecture; parallel jobs via controller coordination; shared volume for artifacts |
| ADR-7 | EventTrigger CRD | **New CRD** with webhook path, HMAC secret, workflow template ref, parameter mapping | Declarative, Kubernetes-native, controller can reconcile |
| ADR-8 | Parameter extraction | **Dot-path JSON field selectors** (e.g., `$.ref`, `$.repository.full_name`) | Zero new dependencies, sufficient for common webhook fields, can upgrade to CEL later |

**Key Changes to Notes:**
- Blueprint updated at `arch/blueprint.md` with ADR section, webhook server diagram, EventTrigger CRD spec, and extended Workflow with Jobs
- Requirements updated at `arch/requirements.md` with new must/should items for event triggers, webhooks, and job grouping

### Session 9 — Implementation (opencode)

**Work Done (all phases complete):**

| Phase | Status | What |
|-------|--------|------|
| 1a | ✅ | Extended `WorkflowSpec` with `Jobs []JobSpec` (backward-compatible; `Steps` fallback when `Jobs` empty) |
| 1b | ✅ | Added `JobStatus` tracking to `WorkflowStatus`, `SharedVolume` type for artifact passing between steps in a job |
| 1c | ✅ | Rewrote `Reconcile` to dispatch to job-based (`reconcileJobWorkflow`) or flat-step path; added `detectJobCycle`, `evaluateJobWhen`, `reconcileJobSteps`, `buildJobStepRunner` with shared volume injection, `computeJobWorkflowPhase` |
| 2 | ✅ | Created `EventTrigger` CRD via kubebuilder scaffold; added safety fields: `WebhookConfig.Path`, `WebhookConfig.SecretRef` (HMAC), `WebhookConfig.AllowedIPs`, `ParameterMapping.Sanitize`, `RateLimitConfig.MaxPerMinute`, `RateLimitConfig.MaxConcurrent`, `AllowedNamespaces` |
| 3 | ✅ | Built `internal/webhook/events/server.go` — HTTP server on port 8080 (manager Runnable), GitHub HMAC-SHA256 validation, payload size limit (1MB), dot-path parameter extraction, rate limiter, IP whitelist, request logging middleware |
| 4 | ✅ | Updated `EventTriggerReconciler` — registers routes on webhook server on create/update, deregisters on delete, fires K8s Events for registration success/failure |
| 5 | ✅ | Created `config/webhook/webhook_event_service.yaml` (Service port 80→8080), `config/webhook/kustomization.yaml`; added port 8080 to manager Deployment |
| 6 | 🔲 | Tests — unit tests exist but no dedicated tests yet for new paths |

**Safety features built into EventTrigger:**
- HMAC-SHA256 payload validation (GitHub X-Hub-Signature-256)
- Payload size capped at 1MB
- Rate limiting (configurable max per minute)
- IP CIDR whitelist
- Parameter sanitization (strips shell metacharacters)
- Secret handling via K8s Secrets (never logged or exposed in error messages)
- Webhook route deregistration on trigger deletion

**Key implementation details:**
- `buildJobStepRunner` injects shared volume (emptyDir/PVC), job-level env vars, and job-level gitRepo into each step's Runner spec
- Steps in a job get label `runner-operator.io/job: <name>` for filtering
- `evaluateJobWhen` supports `on_success`, `on_failure`, `always` at the job level (same semantics as step-level `when`)
- Jobs without `needs` run in parallel; jobs with `needs` wait for dependencies
- Existing flat-step Workflows continue to work unchanged (backward compatible)
- Parameter extraction uses safe dot-path JSON selectors (`$.ref`, `$.repository.full_name`)
- Workflow template lookup uses the referenced Workflow CR as a blueprint; parameters injected as env vars on the first step

**Test Results:** `make test` — passing (23.4% coverage), `make lint` — 0 issues
**Build:** `go build ./...` — OK
