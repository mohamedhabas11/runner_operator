# runner-operator — Code Review Outcomes

> Score: **7/10**. Defense-in-depth gaps in validation, HA operational model, and test coverage.

---

## Immediate (Next Session)

### I-1: Fix spec drift defer loss on restart
**Severity:** Medium | **Effort:** 2h | **Source:** Finding 7

Store the deferred intended spec hash in a separate `Status.DesiredSpecHash` field so it survives controller restart. On restart, compare actual Job spec-hash label against current Runner spec hash to re-detect drift.

**Files:** `api/v1alpha1/runner_types.go`, `internal/controller/runner_controller.go`

- [x] Add `DesiredSpecHash string` to `RunnerStatus`
- [x] Set it on drift defer instead of only patching condition
- [x] On restart, read `Status.DesiredSpecHash` and compare to current spec
- [x] Run `make manifests generate`
- [x] Unit test: simulate restart mid-defer → drift re-detected
- [ ] E2E test: spec change while job running → deferred → restart → replace on complete

### I-2: Add CRD validation markers
**Severity:** Medium | **Effort:** 1h | **Source:** Finding 10

Add `+kubebuilder:validation:Required`, `MinLength`, `Enum`, `Pattern` markers to all three CRD types. Run `make manifests`.

**Files:** `api/v1alpha1/*_types.go`

- [x] `RunnerSpec.Image`: `+kubebuilder:validation:Required`, `MinLength=1`
- [x] `RunnerSpec.GitRepo.URL`: `+kubebuilder:validation:Pattern` for URL format
- [x] `RunnerPhase` / `WorkflowPhase`: `+kubebuilder:validation:Enum`
- [x] `EventTriggerSpec.Webhook.Path`: `+kubebuilder:validation:MinLength=1`, `Pattern`
- [x] `RunnerSpec.TimeoutAfter`: `+kubebuilder:validation:Minimum=1`
- [x] Run `make manifests` and verify CRD YAML contains corresponding `x-kubernetes-validations` / OpenAPI constraints

### I-3: Enable leader election by default
**Severity:** Medium | **Effort:** 15m | **Source:** Finding 11

Change `cmd/main.go:71` default from `false` to `true`. Users who want single-replica without leader election can set `--leader-elect=false`.

**File:** `cmd/main.go`

- [x] Change `flag.BoolVar(&enableLeaderElection, "leader-elect", false, ...)` to `true`
- [x] Update Helm chart values to match default
- [x] Document in README upgrade note

---

## Short Term (Next 1-2 Sessions)

### ST-1: Scaffold validation webhooks for all 3 CRDs
**Severity:** Critical | **Effort:** 4h | **Source:** Finding 1

Scaffold via `kubebuilder create webhook` and implement admission validation for critical fields. Currently blocked on cert-manager — either install cert-manager in the dev cluster or use `--cert-...` flags for self-signed dev certs.

**Files:** `internal/webhook/*`, `api/v1alpha1/*_types.go`

- [ ] Install cert-manager in dev cluster (or document no-cert workaround for scaffold)
- [ ] Runner validating webhook: `spec.image` non-empty, `spec.timeoutAfter` positive, `spec.gitRepo.url` valid URL
- [ ] Workflow validating webhook: `spec.steps[].name` non-empty and unique, cycle detection at admission, `spec.timeout` positive
- [ ] EventTrigger validating webhook: `spec.webhook.path` non-empty and valid HTTP path, `spec.rateLimit.maxPerMinute >= 0`
- [ ] Unit tests for each validation function
- [ ] E2E test: `kubectl apply` with invalid spec → rejected by API server

### ST-2: Add finalizer to Runner
**Severity:** High | **Effort:** 3h | **Source:** Finding 5

Prevent silent mid-execution deletion of Runner (and its Job). On deletion: log warning, let job complete, emit `RunnerTerminated` event. Remove finalizer only after Job is confirmed complete.

**Files:** `internal/controller/runner_controller.go`, `api/v1alpha1/runner_types.go`

- [x] Add `runner-operator.io/cleanup` finalizer constant
- [x] Add finalizer in `Reconcile` on create
- [x] Handle deletion: wait for Job completion, emit event, remove finalizer
- [ ] Unit test: deletion during Running phase → finalizer blocks, Job continues
- [ ] E2E test: delete Runner mid-execution → Runner persists until Job completes

### ST-3: Expand integration tests with behavioral assertions
**Severity:** High | **Effort:** 5h | **Source:** Finding 2

Current integration tests are smoke-only (create CR, call Reconcile once, assert no error). Replace with meaningful state transition and error path tests.

**Files:** `internal/controller/*_test.go`

- [x] Runner: verify Job created with correct spec, phase transitions, spec drift replaces Job, validation failure persists condition
- [x] Workflow: 3-step DAG → all Runners complete → workflow Succeeded, `dependsOn` ordering verified, `when=on_failure` skip verified
- [x] EventTrigger: registration successful, path collision sets condition
- [x] Refactor test helpers for shared setup/teardown

### ST-4: Add Secret watch to EventTrigger controller
**Severity:** Medium | **Effort:** 2h | **Source:** Finding 6

Add `Watches()` on Secrets with an event mapper that maps secret changes to the EventTriggers referencing them. The `RegisterRoute` call already fetches the latest secret, so the controller just needs re-triggering.

**Files:** `internal/controller/eventtrigger_controller.go`

- [x] Implement `mapSecretToTriggers` event mapper
- [x] Add `Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapper))` in `SetupWithManager`
- [x] Unit test: secret update → trigger reconcile queued
- [ ] E2E test: rotate HMAC secret → webhook still accepts new signature

### ST-5: Inject webhook parameters to all steps
**Severity:** Medium | **Effort:** 2h | **Source:** Finding 8

Instead of injecting only into `Steps[0].Env` or `Jobs[0].Steps[0].Env`, inject parameters as workflow-level env vars that get prepended to every step. Or inject into all steps/jobs uniformly.

**Files:** `internal/webhook/events/server.go`, `api/v1alpha1/workflow_types.go`

- [x] Inject parameters into all steps (not just first)
- [x] Update `server.go` parameter injection logic
- [x] Update webhook tests
- [ ] E2E test: 3-step workflow → parameters available in step 2 and 3

---

## Medium Term (3-6 Sessions)

### MT-1: Replace O(N) namespace quota list
**Severity:** High | **Effort:** 3h | **Source:** Finding 4

`checkNamespaceQuota` lists ALL workflows per namespace. Replace with label selector on terminal phases, or maintain a running counter in a separate CR or status field.

**Files:** `internal/controller/workflow_controller.go`

- [ ] Design: label selector vs. aggregated counter CR vs. status-based approach
- [ ] Implement chosen approach
- [ ] Unit test: quota enforcement at scale (mock 1000+ workflows)
- [ ] Benchmark: verify single-digit millisecond quota check at 10K workflows

### MT-2: Implement distributed rate limiting
**Severity:** High | **Effort:** 5h | **Source:** Finding 3

Replace per-replica in-memory rate counter with a shared mechanism. Options: EventTrigger status as distributed lease (lastTriggerTime with optimistic concurrency), or lightweight external rate limiter (Redis sidecar).

**Files:** `internal/webhook/events/server.go`, `api/v1alpha1/eventtrigger_types.go`

- [ ] Design decision: status-based lease vs. Redis sidecar vs. ConfigMap leader-elected writes
- [ ] Implement shared rate limiter
- [ ] Unit test: concurrent requests across simulated replicas → correct combined rate
- [ ] E2E test: 2 webhook server replicas → rate limit is N total, not 2N
- [ ] Update README rate limit docs

### MT-3: Add Helm chart e2e test to CI
**Severity:** Low | **Effort:** 3h | **Source:** Roadmap

Add a CI job that deploys the Helm chart to Kind and verifies basic functionality (operator pod running, CRDs installed).

**Files:** `.github/workflows/test-e2e.yml` or new workflow

- [ ] Add CI step: `helm install` → verify pod ready → `kubectl apply` sample CR → verify reconciliation
- [ ] Separate from existing e2e test workflow (or merge with flag)

### MT-4: Add observability alerts
**Severity:** Low | **Effort:** 2h | **Source:** Roadmap

Document / implement Prometheus alerting rules for: rate limit bypass, orphaned jobs (Runner exists without Job or vice versa), lease contention.

**Files:** `config/prometheus/` (new), README

- [ ] Define alert rules as PrometheusRule CR or documentation
- [ ] Add rate limit bypass detection (requests / replica count vs. configured max)
- [ ] Add orphaned job detection (Job not owned by any Runner)

---

## Long Term (6+ Sessions)

### LT-1: Cross-namespace WorkflowTemplate
**Effort:** 5h | **Source:** Roadmap

Allow WorkflowTemplate in a shared namespace to be parameterized by EventTrigger. Template holds the workflow spec, trigger namespace provides parameters.

### LT-2: Workflow-level retry policy
**Effort:** 3h | **Source:** Roadmap

Retry entire workflows on failure, not just individual steps. Needs careful idempotency design.

### LT-3: Admission webhook for orphaned resource adoption
**Severity:** Low | **Effort:** 4h | **Source:** Finding 15

In the `apierrors.IsAlreadyExists` path, check the existing Job's owner reference. If it points to a deleted Runner UID, adopt the Job (update owner reference) or delete and recreate.

### LT-4: CycleDetector O(N²) → O(N)
**Severity:** Low | **Effort:** 1h | **Source:** Finding 13

Build a name→item map once and use it in DFS traversal instead of scanning the list each time. Pure perf optimization.

### LT-5: Workflow timeout on controller restart
**Severity:** Low | **Effort:** 2h | **Source:** Finding 14

Store deadline in `Status.Deadline` and check on startup regardless of `RequeueAfter`. Without a change to trigger reconcile, timeout fires on the next step transition. Consider setting `startTime` to nil on restart to force initial reconcile.

### LT-6: fetchPodLogs sort by creation timestamp
**Severity:** Medium | **Effort:** 1h | **Source:** Finding 9

Sort pods by creation timestamp descending and take the most recent, instead of `podList.Items[0]` which may be a retry from an old attempt.

### LT-7: EventTrigger path uniqueness index
**Severity:** Low | **Effort:** 2h | **Source:** Finding 12

Replace O(N) cluster-wide list with annotation-based index or field-selector query. Acceptable at current scale (<100 triggers); flag for future.

---

## Summary

| Horizon | Items | Total Effort |
|---------|-------|-------------|
| Immediate | 3 | ~3h |
| Short Term | 5 | ~16h |
| Medium Term | 4 | ~13h |
| Long Term | 5 | ~13h |
| **Total** | **17** | **~45h** |
