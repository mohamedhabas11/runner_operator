# runner-operator — Vision

> Living document. Agents append new sections at the bottom. Never edit existing entries.

---

## 1. Mission

runner-operator is a Kubernetes-native platform for running arbitrary workloads as managed Jobs. It provides the reliability guarantees of a platform engineering tool: correctness, observability, multi-tenancy, and safe defaults — so teams can run database backups, Terraform applies, CI pipelines, and other stateful/critical workloads without operator babysitting.

---

## 2. Core Principles

| # | Principle | Meaning |
|---|-----------|---------|
| 1 | **Correctness over features** | A job that completes but did the wrong thing is worse than a job that didn't run. |
| 2 | **Idempotent reconciliation** | Every reconcile loop must be safe to run multiple times. No duplicate side effects. |
| 3 | **Fail loud** | Errors surface in status conditions, Kubernetes Events, and logs. Never swallow failures. |
| 4 | **Least privilege** | Jobs run as non-root, read-only filesystem, dropped capabilities. Secrets never logged. |
| 5 | **Cross-namespace by default** | One operator installation serves all teams. Namespace isolation is opt-in, not a limitation. |
| 6 | **Kubernetes-native** | Use standard CRDs, OwnerReferences, Conditions. No custom frameworks or opaque patterns. |

---

## 3. Target User

- **Platform engineers** who need to offer a self-service workload execution layer to their organization.
- **Teams** running stateful workloads: database backups, Terraform/Ansible, data pipelines, CI/CD.
- **Requirements**: correctness guarantees, audit trail, multi-tenant isolation, no data loss.

---

## 4. Current State (v1alpha1)

### What Exists

| Component | Status | Notes |
|-----------|--------|-------|
| **Runner CRD** | ✅ Implemented | Maps 1:1 to `batch/v1.Job`. Spec hash drift detection. |
| **Workflow CRD** | ✅ Implemented | DAG steps, `when` conditions, retry with backoff, timeout. |
| **Job Grouping** | ✅ Implemented | Parallel job groups with shared volumes (EmptyDir/PVC). |
| **EventTrigger CRD** | ✅ Implemented | GitHub webhooks → Workflow creation. HMAC, rate limiting, IP whitelist. |
| **Webhook Server** | ✅ Implemented | HTTP server on port 8080, integrated in manager. |
| **Runner Controller** | ✅ Implemented | Creates Jobs, drift detection, status reconciliation. |
| **Workflow Controller** | ✅ Implemented | DAG evaluation, step orchestration, timeout enforcement. |
| **EventTrigger Controller** | ✅ Implemented | Route registration/deregistration. |
| **RBAC** | ✅ ClusterRole | Cluster-wide permissions. |
| **Security** | ✅ Implemented | Non-root, read-only rootfs, dropped capabilities, seccomp. |
| **E2E Tests** | ✅ 7 scenarios | Runner lifecycle, failure, drift, workflow, timeout, on_failure, git clone. |
| **CI** | ✅ GitHub Actions | Unit tests, E2E on Kind, lint. |

### What's Missing

| Gap | Severity | Impact |
|-----|----------|--------|
| No validation webhooks | P0 | Invalid CRs accepted, fail at runtime |
| `RunnerRef` namespace-locked | P0 | Cannot share Runner templates across namespaces |
| `AllowedNamespaces` dead code | P0 | No multi-tenant isolation |
| No error message capture | P1 | Workflow status lacks failure details |
| EventTrigger workflows have no owner | P1 | Orphaned workflows on trigger deletion |
| No Prometheus metrics | P2 | No operational visibility |
| No namespace quotas | P2 | One team can starve others |
| No workflow step DAG topological sort | P2 | Steps execute in slice order, not dependency order |

---

## 5. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                          │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                 Manager (single binary)                    │   │
│  │                                                            │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │   │
│  │  │ Runner   │  │ Workflow │  │ Event    │  │ Webhook  │  │   │
│  │  │ Ctrl     │  │ Ctrl     │  │ Trigger  │  │ Server   │  │   │
│  │  └────┬─────┘  └────┬─────┘  │ Ctrl     │  │ :8080    │  │   │
│  │       │              │        └────┬─────┘  └──────────┘  │   │
│  └───────┼──────────────┼─────────────┼──────────────────────┘   │
│          │              │             │                           │
│          ▼              ▼             ▼                           │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                       │
│  │ batch/   │  │ Runner   │  │ Workflow │                       │
│  │ v1.Job   │  │ CR       │  │ CR       │                       │
│  └──────────┘  └──────────┘  └──────────┘                       │
└─────────────────────────────────────────────────────────────────┘
```

### Ownership Chain

```
Workflow ──owns──▶ Runner ──owns──▶ Job ──owns──▶ Pod
EventTrigger ──creates──▶ Workflow (via webhook server)
```

### Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Job mapping | Runner → `batch/v1.Job` | Built-in retry, backoff, completion, timeout |
| Cleanup | OwnerReferences + K8s GC | No finalizer logic, no resource leaks |
| Retry control | `BackoffLimit: 0` | Operator owns retry decisions, not Job controller |
| Status updates | `Status().Patch` with `MergeFrom` | Avoids 409 conflicts on concurrent updates |
| Drift detection | SHA-256 spec hash in label | Efficient comparison without full-spec diff |
| Webhook server | Integrated in manager | Single deployment, lightweight, extractable later |
| EventTrigger auth | HMAC-SHA256 | GitHub-compatible, zero new dependencies |
| Parameter extraction | Dot-path JSON selectors | Zero dependencies, sufficient for common fields |

---

## 6. Non-Functional Requirements

### Correctness

- [ ] Every Runner must map to exactly one Job. No duplicates.
- [ ] Spec drift on a running Job is deferred until completion. No mid-flight tear-down.
- [ ] Workflow timeout must be enforced controller-side, not just via Job `ActiveDeadlineSeconds`.
- [ ] Retry backoff must be exponential with configurable initial/max delay.
- [ ] DAG cycle detection must abort the workflow before any Runner is created.

### Multi-Tenancy

- [ ] Operator is deployed once, serves all namespaces.
- [ ] `AllowedNamespaces` on EventTrigger must be enforced.
- [ ] `RunnerRef` must support cross-namespace references.
- [ ] Teams cannot create Workflows/Runners in namespaces they don't own (via admission policy).

### Observability

- [ ] All phase transitions emit Kubernetes Events with reason codes.
- [ ] Status conditions use `metav1.Condition` with `Reason` and `Message`.
- [ ] Prometheus metrics: reconciliation count, duration, error rate, job completion rate.
- [ ] Structured logging with reconciliation context (name, namespace, phase).

### Security

- [ ] All Jobs run as non-root (UID 1000), read-only rootfs, dropped capabilities.
- [ ] Seccomp profile: RuntimeDefault.
- [ ] Secrets never logged or exposed in error messages.
- [ ] Webhook HMAC secrets stored in K8s Secrets, not env vars.
- [ ] Rate limiting on EventTrigger webhook endpoints.
- [ ] IP CIDR whitelist on EventTrigger webhook endpoints.

### Operational

- [ ] Leader election for HA (single active controller).
- [ ] Health/readiness probes on port 8081.
- [ ] Graceful shutdown with in-flight request drain.
- [ ] Resource limits on manager container.
- [ ] Configurable log level (`--zap-devel`).

---

## 7. Roadmap

### Phase 1 — Correctness Foundation (Current Focus)

**Goal**: Make the operator safe for stateful workloads (database backups, Terraform).

| Task | Status | Priority |
|------|--------|----------|
| Validation webhooks for CRDs | 🔲 | P0 |
| `RunnerRef` cross-namespace support | 🔲 | P0 |
| `AllowedNamespaces` enforcement | 🔲 | P0 |
| Capture Pod logs in Workflow step status | 🔲 | P1 |
| EventTrigger workflow ownership | 🔲 | P1 |
| Step timeout enforcement in controller | 🔲 | P1 |

### Phase 2 — Observability & Operations

**Goal**: Make the operator production-operable.

| Task | Status | Priority |
|------|--------|----------|
| Prometheus metrics (reconciliation, jobs) | 🔲 | P2 |
| Topological sort for step execution order | 🔲 | P2 |
| Namespace quotas / ResourceQuota integration | 🔲 | P2 |
| Tenant-aware metrics and events | 🔲 | P2 |
| Audit logging for cross-namespace ops | 🔲 | P2 |

### Phase 3 — Advanced Features

**Goal**: Expand platform capabilities.

| Task | Status | Priority |
|------|--------|----------|
| Workflow template library (shared across namespaces) | 🔲 | P3 |
| CEL expression support for `when` conditions | 🔲 | P3 |
| Workflow execution history (TTL-based cleanup) | 🔲 | P3 |
| Circuit breaker for external dependencies | 🔲 | P3 |
| Pod Disruption Budgets for long-running jobs | 🔲 | P3 |

---

## 8. Contributing

### For Agents

When working on this project:

1. **Read this vision document first** — understand the principles and current state.
2. **Check `ledger/tasks.md`** — find existing tasks and their priority.
3. **Add new tasks** — append to the relevant session section in `tasks.md`.
4. **Update this vision** — if your work changes the architecture or adds new capabilities, update sections 4-7.
5. **Never edit existing entries** in this file — only append new sections at the bottom.

### Task Format

```markdown
- [ ] **Task name** — Description with file:line references. Priority: P0/P1/P2.
```

### Session Log Format

```markdown
### Session N — Title

**Work Done:**
- What was done

**Decisions:**
- What was decided and why
```

---

## 9. Open Questions

| Question | Context | Status |
|----------|---------|--------|
| Should `RunnerRef` use `ObjectReference` or a custom type? | Cross-namespace template resolution | **Decided**: Custom type with Name+Namespace |
| Should EventTrigger-created workflows be in trigger namespace or template namespace? | Multi-tenant isolation vs. reuse | **Decided**: Trigger namespace (isolation) |
| Should we add validating webhooks or rely on controller-side validation? | Admission control strategy | **Decided**: Defer to separate session (requires cert-manager) |
| Should the webhook server be extracted to a separate deployment? | Scaling and isolation | Open |
| Should we support `batch/v1.Job` annotations for custom Pod configs? | Advanced scheduling (node affinity, tolerations) | Open |

---

*Last updated: Session 13 — GitOps Factory & Workflow Deduplication*

---

## 10. Session 13 — GitOps Factory & Workflow Deduplication

### Architecture Changes

**New package: `internal/gitops/`** (factory pattern)

```
NewAuthStrategy(gitRepo) → AuthStrategy interface
    ├─ noAuthStrategy    (public repos)
    ├─ sshAuthStrategy   (SSH keys)
    └─ httpAuthStrategy  (token / basicAuth)
```

Public builders: `BuildInitContainer`, `BuildVolumes`, `BuildCloneScript`. Extracted from `runner_controller.go:buildGitInitContainer` and `workflow_controller.go:cloneGitRepo`. Deduplicates ~80 LOC of inline container/volume logic shared by both controllers.

**Workflow controller refactoring** (generics + strategy pattern):

| Pattern | Before | After | Overlap |
|---------|--------|-------|---------|
| Step reconciliation | `reconcileSteps` + `replicateJobSteps` (copy-paste) | `reconcileStepLoop` + `buildRunner` closure | ~85% |
| Cycle detection | `detectCycle` + `detectJobCycle` (copy-paste) | `cycleDetector[T any]` | ~95% |
| Phase computation | `computeFlatWorkflowPhase` + `computeJobWorkflowPhase` (copy-paste) | `computeWorkflowPhase[T any]` | ~90% |

**API type changes:**

- `GitAuthType` enum (`none`, `ssh`, `http`) for declarative auth selection
- `GitRepo.Image` field for custom init container images
- `SecretRef` changed from `*corev1.LocalObjectReference` to `corev1.LocalObjectReference` (value type — no longer optional pointer)

### Updated Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                          │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                 Manager (single binary)                    │   │
│  │                                                            │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │   │
│  │  │ Runner   │  │ Workflow │  │ Event    │  │ Webhook  │  │   │
│  │  │ Ctrl     │  │ Ctrl     │  │ Trigger  │  │ Server   │  │   │
│  │  └────┬─────┘  └────┬─────┘  │ Ctrl     │  │ :8080    │  │   │
│  │       │              │        └────┬─────┘  └──────────┘  │   │
│  │       │              │             │                       │   │
│  │       │     ┌────────┴────────┐    │                       │   │
│  │       │     │ gitops factory  │    │                       │   │
│  │       │     │ (shared)        │    │                       │   │
│  │       │     └─────────────────┘    │                       │   │
│  └───────┼──────────────┼─────────────┼──────────────────────┘   │
│          │              │             │                           │
│          ▼              ▼             ▼                           │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                       │
│  │ batch/   │  │ Runner   │  │ Workflow │                       │
│  │ v1.Job   │  │ CR       │  │ CR       │                       │
│  └──────────┘  └──────────┘  └──────────┘                       │
└─────────────────────────────────────────────────────────────────┘
```

### Gap Status Update

| Gap (from §4) | Previous Severity | Current Status |
|----------------|-------------------|----------------|
| No validation webhooks | P0 | Unchanged (deferred — needs cert-manager) |
| `RunnerRef` namespace-locked | P0 | ✅ Resolved Session 12 |
| `AllowedNamespaces` dead code | P0 | ✅ Resolved Session 12 |
| No error message capture | P1 | Unchanged |
| EventTrigger workflows have no owner | P1 | Unchanged |
| No Prometheus metrics | P2 | Unchanged |
| No namespace quotas | P2 | Unchanged |
| No workflow step DAG topological sort | P2 | Unchanged |
| Runner/Workflow git init logic duplication | P1 | ✅ Resolved — `internal/gitops/` factory pattern |
| Step reconciliation copy-paste | P1 | ✅ Resolved — `reconcileStepLoop` + closure strategy |
