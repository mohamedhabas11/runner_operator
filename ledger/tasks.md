# runner-operator — Ledger

## Session Log

### Session 14 — Vision Gaps (Remaining)

**Goal:** Close the remaining gaps identified in `ledger/vision.md §4 — What's Missing`.

#### P0 — Critical

- [ ] **Add namespace validation webhook** — Scaffold via `kubebuilder create webhook --group runners --version v1alpha1 --kind EventTrigger --programmatic-validation`. Validate: `Webhook.Path` uniqueness, `WorkflowTemplate.Name` non-empty, `AllowedNamespaces` entries valid DNS labels. Requires cert-manager + admission TLS. See `arch/blueprint.md` for cert-manager setup.

#### P1 — Important

- [ ] **Capture Pod logs in Workflow step status** — When a step Runner fails, fetch its Pod logs and store them in `WorkflowStepStatus.Message` or a new `Logs` field. Enables debugging without `kubectl logs`. Files: `internal/controller/workflow_controller.go` (reconcile logic), `api/v1alpha1/workflow_types.go` (status field).
- [x] **EventTrigger workflow ownership** — ✅ Already implemented (commit `a4c1440`). `server.go:340` calls `controllerutil.SetControllerReference` before creating the workflow.
- [x] **Implement `gitRepo.Path`** — ✅ Already handled by Session 13 refactoring: `script.go:36-42` validates path existence in init container; `runner_controller.go:153-157` sets `WorkingDir` with path. Original task was stale — written before the gitops factory replaced `buildGitInitContainer`.

#### P2 — Nice to Have

- [ ] **Workflow step DAG topological sort** — Process steps in dependency order (`dependsOn`) instead of slice order. Add `topologicalSort(steps)` before the reconcile loop in `workflow_controller.go`. Steps with no deps can run in parallel; cycles caught by existing `cycleDetector`.
- [ ] **Prometheus metrics** — Add `controller_runtime_metrics` for: reconciliation count, duration, error rate, job completion rate, workflow phase transitions. Use `metrics.Registry` from `controller-runtime`. File: `internal/controller/` (both controllers).
- [ ] **Namespace quotas** — Add `MaxConcurrentWorkflows` field to `WorkflowSpec` or controller config. Track active workflows per namespace and reject new ones when quota exceeded.
- [ ] **Cross-namespace template workflow decision** — Decide: create workflow in template namespace (for reuse) or trigger namespace (for isolation). Document in `arch/blueprint.md`. Update `internal/webhook/events/server.go:299` if needed.
- [ ] **SharedVolume PVC cross-namespace docs** — Document that PVC references are namespace-scoped. Add note to `arch/blueprint.md` and relevant CRD descriptions.
- [ ] **Expose `--webhook-event-port` flag docs** — Already configurable via flag in `cmd/main.go:67`. Add to `config/manager/manager.yaml` and document in `README.md`.
- [x] **Improve Conditions** — ✅ Implemented: `internal/controller/conditions.go` with `ConditionBuilder` factory (builder pattern), `ConditionTypeReady` constant, and predefined reason codes for all state transitions. All three controllers (Runner, Workflow, EventTrigger) now set proper `metav1.Condition` with `Reason`/`Message`/`ObservedGeneration` at every phase transition. 7 unit tests covering defaults, chaining, upsert, and per-controller helpers.

#### Chores

- [ ] **Add CI check for README examples** — Use `kubeconform` or `kubectl apply --dry-run=client` to catch drift between CRD schemas and README examples.
- [ ] **Add markdownlint/vale CI check** — For documentation consistency across `.md` files.
- [ ] **Dependabot config** — Add `.github/dependabot.yml` for GitHub Actions version updates.
- [ ] **Network isolation docs** — Add namespace isolation network policy examples to `config/` for multi-tenant deployments.

---

### Session 13 — GitOps Factory & Workflow Deduplication

**Work Done:**
- **`internal/gitops/` factory package** — `NewAuthStrategy(gitRepo)` returns `AuthStrategy` interface with `BuildInitContainer`, `BuildVolumes`, `BuildCloneScript`. Three strategies: `noAuthStrategy` (public), `sshAuthStrategy` (SSH keys), `httpAuthStrategy` (token/basic auth). Replaces inline git init logic duplicated in `runner_controller.go` and `workflow_controller.go`. **98.8% test coverage** via 25 unit tests.
- **`reconcileStepLoop` extraction** — Unified `reconcileSteps` + `reconcileJobSteps` (~85% overlap) into single function driven by `buildRunner func(step) *Runner` closure.
- **`cycleDetector[T any]`** — Generic 3-color DFS replaces `detectCycle` + `detectJobCycle` (~95% overlap). Type-safe via generics.
- **`computeWorkflowPhase[T any]`** — Generic phase aggregation replaces `computeFlatWorkflowPhase` + `computeJobWorkflowPhase` (~90% overlap).
- **API type changes:** Added `GitAuthType` enum, `GitRepo.Image` field, changed `SecretRef` to value type. Ran `make generate` for deepcopy regeneration.
- **Inline WHAT/WHY docs** — Added engineer-facing documentation to all refactored code blocks.
- **`make test`** — all passing. **`make test-e2e`** — 11/11 specs passing (173s on isolated Kind cluster `runner-operator-test-e2e`).

**Files modified:**
- `internal/gitops/` (new) — 7 files: `git.go`, `git_test.go`, `auth.go`, `auth_test.go`, `clone.go`, `clone_test.go`, `initcontainer.go`
- `internal/controller/runner_controller.go` — git init replaced with gitops factory calls
- `internal/controller/workflow_controller.go` — `reconcileStepLoop`, `cycleDetector[T]`, `computeWorkflowPhase[T]`, git init replaced with factory calls
- `api/v1alpha1/runner_types.go` — `GitAuthType`, `GitRepo.Image`, `SecretRef` value type
- `api/v1alpha1/zz_generated.deepcopy.go` — regenerated

**Verification:**
- `make test` — 25 new unit tests, all passing
- `make lint-fix` — 0 issues
- `go build ./...` — OK
- `make test-e2e` — 11/11 specs passing

**Key Decisions:**
- `AuthStrategy` as interface (not func pointer) — allows multiple builders per strategy, extensible for future auth methods
- Generics for `cycleDetector[T]` and `computeWorkflowPhase[T]` — type safety without interface{} cast, zero runtime overhead
- Timeout handlers NOT extracted — genuinely different logic (nested vs flat loops); extraction would add indirection without real savings
- `SecretRef` value type — needed for GitAuth factory; `make generate` regenerated deepcopy

---

### Session 12 — Cross-Namespace Fixes & Release Workflow Hardening

**Work Done:**
- **Session 10 P1 items (all 4):**
  - fetch-depth: changed 0 → 1 + explicit `git fetch --tags`
  - Release notes: added `--no-merges` to `git log`
  - gh-pages: added branch existence check before worktree; removed `|| true` masking
  - URLs: parameterized all hardcoded GitHub URLs via `$GITHUB_REPOSITORY` in release workflow
- **Session 10 P2 (5 of 8):**
  - Verified ingress file exists
  - Fixed NetworkPolicy CIDR docs with pod CIDR note + broader RFC 1918 ranges
  - Corrected CRD preserveUnknownFields doc (default prunes, not preserves)
  - Added `kubectl logs` alternative to stern
  - Updated runnerRef CRD table to new RunnerRef type
- **Session 11 P0 (2 of 3):**
  - RunnerRef: new custom type with Name+Namespace, cross-namespace resolution in controller
  - AllowedNamespaces: enforced in webhook server + controller with status/events
  - Validation webhook: deferred (requires cert-manager scaffold)
- **Session 11 P1 (1 of 4):**
  - Webhook server RBAC: added namespace read permission

**Files modified:**
- `.github/workflows/release-chart.yaml` — 4 improvements
- `api/v1alpha1/workflow_types.go` — new RunnerRef type, updated WorkflowStep field
- `internal/controller/workflow_controller.go` — cross-namespace resolution in buildStepRunner
- `internal/webhook/events/server.go` — AllowedNamespaces enforcement in createWorkflow
- `internal/controller/eventtrigger_controller.go` — AllowedNamespaces enforcement + RBAC marker
- `README.md` — CRD table update, NetworkPolicy docs, preserveUnknownFields fix, stern alternative
- `config/crd/bases/` — regenerated (RunnerRef schema change)
- `config/rbac/role.yaml` — regenerated (namespace RBAC marker)
- `api/v1alpha1/eventtrigger_types.go` — removed `omitempty` from `Registered bool` (was swallowing `false` in JSON patches)
- `api/v1alpha1/zz_generated.deepcopy.go` — regenerated (RunnerRef DeepCopy)
- `test/e2e/e2e_test.go` — RunnerRefCrossNamespace + AllowedNamespaces test contexts

**Verification:**
- `make test` — passing
- `make lint-fix` — 0 issues
- `go build ./...` — OK
- **`make test-e2e` — 11/11 specs passing** (RunnerRefCrossNamespace + AllowedNamespaces now green after `omitempty` fix)

---

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

## Open Tasks

All open tasks are tracked in **Session 14** above, organized by priority (P0–P2 + Chores). Items from previous sessions' deferred/skipped sections have been migrated there.

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

---

## Session 10 — Code Review Follow-up (Latest 3 Commits)

**Work Done:**
- Reviewed commits 7de5dec (release workflow), 91602e1 (kustomization), 2484058 (README)

### P1 — Important ✓

- [x] **Release workflow: fetch-depth optimization** — Changed `fetch-depth: 0` to `fetch-depth: 1` + `git fetch --tags` in `.github/workflows/release-chart.yaml:15-20`
- [x] **Release workflow: clean release notes** — Added `--no-merges` to `git log` in release notes generation (lines 61, 63)
- [x] **Release workflow: gh-pages error handling** — Added explicit `git ls-remote --exit-code` check for gh-pages branch before worktree; replaced `|| true` with proper `git diff --cached --quiet` guard
- [x] **Release workflow + README: parameterize GitHub URLs** — Replaced hardcoded `mohamedhabas11/runner_operator` with `$OWNER/$REPO` from `GITHUB_REPOSITORY` env var

### P2 — Nice to Have

- [x] **README: verify ingress example file** — `config/webhook/ingress.yaml` exists ✅
- [x] **README: fix NetworkPolicy CIDR** — Added pod CIDR note and broadened `except` list to include all RFC 1918 ranges
- [x] **README: fix CRD preserveUnknownFields doc** — Corrected statement: structural schemas prune unknown fields by default; added workaround note
- [x] **README: note stern as optional** — Added `kubectl logs` alternative for controller logs
- [x] **README: update runnerRef CRD docs** — Changed from `LocalObjectReference` to new `RunnerRef` type (name + namespace)
- [ ] **Add CI check for README examples** — Use `kubeconform` or `kubectl apply --dry-run=client` to catch drift
- [ ] **Add markdownlint/vale check** — For documentation consistency

---

## Session 11 — Cross-Namespace & Multi-Tenancy Deep Dive

**Work Done:**
- Analyzed RBAC, manager deployment, controller watching, webhook server, CRD types
- Identified 10 gaps preventing reliable cross-namespace operation

### P0 — Critical (Incorrectness)

- [x] **RunnerRef: add Namespace field** — Changed `RunnerRef *corev1.LocalObjectReference` to custom `RunnerRef` type with `Name`+`Namespace` in `api/v1alpha1/workflow_types.go:62`. Updated `buildStepRunner` at `internal/controller/workflow_controller.go:359` to resolve cross-namespace. CRD regenerated with `make manifests generate`.
- [x] **Enforce AllowedNamespaces** — Added check in `internal/webhook/events/server.go:createWorkflow` (rejects if trigger namespace not in allowed list). Added enforcement in `EventTriggerReconciler.Reconcile` with status patch and event. Added RBAC marker for namespace read.
- [ ] **Add namespace validation webhook** — Deferred. Requires cert-manager + admission webhook TLS + kubebuilder scaffold. Needs separate session. See Deferred section.

### P1 — Important (Robustness)

- [ ] **EventTrigger workflow ownership** — In `server.go:createWorkflow`, set `controllerutil.SetControllerReference(trigger, workflow, scheme)` so deleting an EventTrigger cleans up created workflows. Requires trigger and workflow in same namespace.
- [ ] **Cross-namespace template workflow** — Decide: create workflow in template namespace (for reuse) or trigger namespace (for isolation). Document decision. If template namespace, update `server.go:299`.
- [ ] **SharedVolume PVC cross-namespace** — Document that PVC references are namespace-scoped. For cross-namespace job grouping, use EmptyDir or CSI-driven cross-namespace volumes.
- [x] **Webhook server RBAC** — Added `// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch` to EventTrigger controller. Generated ClusterRole updated.

### P2 — Nice to Have (Operational)

- [ ] **Namespace quotas** — Add `NamespaceQuota` field to WorkflowSpec or integrate with Kubernetes ResourceQuota to limit concurrent workflows per namespace.
- [ ] **Tenant-aware metrics** — Add `namespace` label to all Prometheus metrics and Kubernetes Events for cost attribution.
- [ ] **Network isolation** — Add namespace isolation network policies between tenant namespaces.
- [ ] **Audit logging** — Log all cross-namespace operations with tenant identity for compliance.
