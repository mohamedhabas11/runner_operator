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

## Deferred / Skipped

### P2 — Nice to Have

- [ ] **Improve Conditions** — Add Reason/Message to status conditions for better observability. Low priority; functional behavior unchanged.
