[![CI](https://github.com/mohamedhabas11/runner_operator/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/mohamedhabas11/runner_operator/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mohamedhabas11/runner_operator)](https://goreportcard.com/report/github.com/mohamedhabas11/runner_operator)

# runner-operator

A Kubernetes operator that runs OCI containers as disposable batch Jobs and
orchestrates multi-step CI/CD pipelines with DAG-based Workflows.
Webhook-triggered pipelines, secret management, and git integration built in.

---

## Contents

- [Architecture](#architecture)
- [CRD Reference](#crd-reference)
- [Installation](#installation)
- [Helm Configuration](#helm-configuration)
- [Usage Patterns](#usage-patterns)
- [Webhook Events](#webhook-events)
- [RBAC & Multi-Tenancy](#rbac--multi-tenancy)
- [Production Guide](#production-guide)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Examples](examples/)
- [Architecture Blueprint](arch/blueprint.md)

---

## Architecture

```
                     ┌──────────────────────────────────────────────────┐
                     │            Manager (single binary)               │
                     │                                                  │
                     │  ┌──────────┐  ┌──────────┐  ┌──────────────┐   │
                     │  │ Runner   │  │ Workflow │  │ EventTrigger  │   │
                     │  │ Ctrl     │  │ Ctrl     │  │ Ctrl          │   │
                     │  └───┬──────┘  └───┬──────┘  └───────┬───────┘   │
                     │      │              │                  │           │
                     │      │  .Owns()     │  .Owns()         │ watches   │
                     │      ▼              ▼                  ▼           │
                     │  ┌──────────────────────────────────────────┐     │
                     │  │  Webhook Server (port 8080)               │     │
                     │  │  /webhooks/* → HMAC → validate → create   │     │
                     │  └──────────────────────────────────────────┘     │
                     └──────────────────────────────────────────────────┘
                                   │
          ┌────────────────────────┼────────────────────────────┐
          ▼                        ▼                            ▼
  ┌───────────────┐       ┌───────────────┐           ┌────────────────┐
  │  batch/v1.Job │       │  Runner CR    │           │  Workflow CR   │
  │  ──→ Pod      │       │  spec.image   │           │  spec.steps[]  │
  │               │       │  spec.gitRepo │           │  spec.jobs[]   │
  └───────────────┘       └───────────────┘           └────────────────┘
```

**Ownership chain:** `Workflow ──owns──▶ Runner ──owns──▶ Job ──owns──▶ Pod`

See [`arch/blueprint.md`](arch/blueprint.md) for reconciliation flow charts,
pod layouts, state machines, and design decisions.

---

## CRD Reference

### Runner — single disposable job

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.image` | `string` | yes | OCI image to run |
| `spec.command` | `[]string` | no | Overrides ENTRYPOINT |
| `spec.args` | `[]string` | no | Overrides CMD |
| `spec.env` | `[]corev1.EnvVar` | no | Environment variables |
| `spec.envFrom` | `[]corev1.EnvFromSource` | no | Bulk env from Secret/ConfigMap |
| `spec.volumes` | `[]corev1.Volume` | no | Extra volumes |
| `spec.mounts` | `[]corev1.VolumeMount` | no | Extra volume mounts |
| `spec.resources` | `corev1.ResourceRequirements` | no | CPU/memory requests & limits |
| `spec.timeoutAfter` | `metav1.Duration` | no | Max runtime (e.g. `30m`, `1h`) |
| `spec.gitRepo` | `GitRepo` | no | Clone repo before executing |

**GitRepo fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `url` | yes | HTTPS or SSH clone URL |
| `revision` | no | Branch, tag, or commit SHA (defaults to remote HEAD) |
| `path` | no | Subdirectory within checkout as working dir (e.g. `terraform/prod`) |
| `auth.secretRef.name` | no | Secret name: SSH key at `ssh-privatekey`, or HTTPS at `username`+`password` |

**Status phases:** `Pending` → `Running` → `Succeeded` / `Failed` / `Unknown`

### Workflow — DAG of Runners

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.timeout` | `metav1.Duration` | no | Workflow-level timeout |
| `spec.steps` | `[]WorkflowStep` | no | Flat step list (backward compatible) |
| `spec.jobs` | `[]JobSpec` | no | Job grouping (ignored when steps is set) |

**WorkflowStep fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Unique step name |
| `image` | `string` | no | OCI image (inline; alternative to runnerRef) |
| `command` | `[]string` | no | Overrides ENTRYPOINT |
| `args` | `[]string` | no | Overrides CMD |
| `env` | `[]corev1.EnvVar` | no | Step-level env vars |
| `gitRepo` | `GitRepo` | no | Clone repo before step |
| `dependsOn` | `[]string` | no | Steps that must complete first |
| `when` | `string` | no | Gate: `on_success` (default), `on_failure`, `always` |
| `retry.maxRetries` | `int` | no | Max retry attempts |
| `retry.backoff.initialDelay` | `metav1.Duration` | yes* | First retry delay |
| `retry.backoff.maxDelay` | `metav1.Duration` | no | Cap on retry delay |
| `timeout` | `metav1.Duration` | no | Per-step timeout |
| `runnerRef.name` | `string` | yes* | Name of Runner template |
| `runnerRef.namespace` | `string` | no | Runner namespace (defaults to workflow's) |

**JobSpec additional fields:**

| Field | Type | Description |
|-------|------|-------------|
| `needs` | `[]string` | Jobs that must complete before this one starts |
| `sharedVolume` | `SharedVolume` | Volume shared across all steps in the job |
| `env` | `[]corev1.EnvVar` | Prepended to every step in the job |
| `gitRepo` | `GitRepo` | Applied to every step that doesn't set its own |

**Status phases:** `Pending` → `Running` → `Succeeded` / `Failed` / `Unknown`
**Step phases:** `Pending` `Running` `Succeeded` `Failed` `Skipped` `Waiting`

### EventTrigger — webhook → Workflow

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.webhook.path` | `string` | yes | HTTP path (e.g. `/webhooks/github-push`); unique cluster-wide |
| `spec.webhook.secretRef.name` | `string` | no | K8s Secret with key `hmac-secret` |
| `spec.webhook.secretRef.namespace` | `string` | no | Secret namespace (defaults to trigger's) |
| `spec.webhook.allowedIPs` | `[]string` | no | CIDR allow list |
| `spec.workflowTemplate.name` | `string` | yes | Workflow CR name to instantiate |
| `spec.workflowTemplate.namespace` | `string` | no | Template namespace (defaults to trigger's) |
| `spec.parameters` | `[]ParameterMapping` | no | Map webhook JSON fields → env vars |
| `spec.rateLimit.maxPerMinute` | `int` | no | Workflow creations/minute (0 = unlimited) |
| `spec.rateLimit.maxConcurrent` | `int` | no | Concurrent Workflows (0 = unlimited) |
| `spec.allowedNamespaces` | `[]string` | no | Namespaces where Workflows may be created |

> **Rate limit in HA mode:** Rate limits are tracked in-memory per replica.
> With `manager.replicas: N`, the effective rate is `N × maxPerMinute`.
> For a shared rate limit across replicas, reduce `maxPerMinute` by a factor
> of `N`, or implement a shared counter using the EventTrigger status as a
> lease.

---

## Installation

### Users — Helm (recommended)

```bash
helm repo add runner-operator https://mohamedhabas11.github.io/runner_operator
helm install runner-operator runner-operator/runner-operator \
  --namespace runner-operator-system --create-namespace \
  --set manager.image.repository=ghcr.io/your-org/runner-operator \
  --set manager.image.tag=v0.3.0
```

**Upgrading:**

```bash
# 1. Apply any CRD schema changes first (Helm skips CRD updates)
kubectl apply --server-side -f https://raw.githubusercontent.com/mohamedhabas11/runner_operator/<tag>/config/crd/bases/

# 2. Upgrade
helm repo update runner-operator
helm upgrade runner-operator runner-operator/runner-operator
```

### Quick test — Kustomize bundle

```bash
kubectl apply -f https://raw.githubusercontent.com/mohamedhabas11/runner_operator/main/dist/install.yaml
```

> Use this for ad-hoc testing or CI. Helm is preferred for production
> (upgrade safety, rollback, values overrides).

### Contributors — Makefile

```bash
make deploy IMG=example.com/runner-operator:v0.0.1
```

---

## Helm Configuration

### Global

| Parameter | Default | Description |
|-----------|---------|-------------|
| `nameOverride` | `""` | Partially override chart name |
| `fullnameOverride` | `""` | Fully override chart name |

### Manager

| Parameter | Default | Description |
|-----------|---------|-------------|
| `manager.enabled` | `true` | Deploy the controller |
| `manager.replicas` | `1` | Replicas (use 2+ for HA) |
| `manager.image.repository` | `controller` | Image registry/repo |
| `manager.image.tag` | `""` | Defaults to Chart.appVersion |
| `manager.image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `manager.imagePullSecrets` | `[]` | Auth for private registries |
| `manager.args` | `["--leader-elect"]` | Extra CLI args (overrides binary default `false` for HA) |
| `manager.resources.limits.cpu` | `500m` | CPU limit |
| `manager.resources.limits.memory` | `128Mi` | Memory limit |
| `manager.resources.requests.cpu` | `10m` | CPU request |
| `manager.resources.requests.memory` | `64Mi` | Memory request |
| `manager.podSecurityContext` | `{runAsNonRoot: true, seccomp: RuntimeDefault}` | Pod security context |
| `manager.securityContext` | `{readOnlyRootFilesystem: true, capabilities.drop: [ALL]}` | Container security context |
| `manager.affinity` | `{}` | Pod affinity |
| `manager.nodeSelector` | `{}` | Node selector |
| `manager.tolerations` | `[]` | Node tolerations |
| `manager.strategy` | `{}` | Deployment strategy |
| `manager.priorityClassName` | `""` | Pod priority class |
| `manager.topologySpreadConstraints` | `[]` | Topology spread |
| `manager.terminationGracePeriodSeconds` | `10` | Graceful shutdown time |
| `manager.pod.labels` | `{}` | Extra pod labels |
| `manager.pod.annotations` | `{}` | Extra pod annotations |

### RBAC

| Parameter | Default | Description |
|-----------|---------|-------------|
| `rbac.namespaced` | `false` | `true` = Role/RoleBinding (single ns), `false` = ClusterRole/ClusterRoleBinding (all ns) |
| `rbac.helpers.enable` | `false` | Install admin/editor/viewer roles per CRD |

### Service Account

| Parameter | Default | Description |
|-----------|---------|-------------|
| `serviceAccount.enable` | `true` | Create ServiceAccount |
| `serviceAccount.name` | `""` | Use existing SA (when enable=false) |
| `serviceAccount.annotations` | `{}` | SA annotations |
| `serviceAccount.labels` | `{}` | SA labels |

### CRDs

| Parameter | Default | Description |
|-----------|---------|-------------|
| `crd.enable` | `true` | Install CRDs with the chart |
| `crd.keep` | `true` | Keep CRDs on uninstall |

### Metrics

| Parameter | Default | Description |
|-----------|---------|-------------|
| `metrics.enable` | `true` | Expose /metrics endpoint |
| `metrics.port` | `8080` | Metrics server port |
| `metrics.secure` | `false` | HTTPS with certs/auth (requires cert-manager or manual TLS) |

### Cert-Manager

| Parameter | Default | Description |
|-----------|---------|-------------|
| `certManager.enable` | `false` | Enable cert-manager integration for webhook TLS + metrics |

### Prometheus

| Parameter | Default | Description |
|-----------|---------|-------------|
| `prometheus.enable` | `false` | Install ServiceMonitor (requires prometheus-operator) |

---

## Usage Patterns

See [`examples/`](examples/) for complete, ready-to-apply YAML files.

| Pattern | File | Description |
|---------|------|-------------|
| Simplest runner | [`examples/runner/simple.yaml`](examples/runner/simple.yaml) | Run a command in any OCI image |
| Git clone + test | [`examples/runner/with-git.yaml`](examples/runner/with-git.yaml) | Clone repo, run go test |
| Script from repo | [`examples/runner/script-from-repo.yaml`](examples/runner/script-from-repo.yaml) | Run a deploy script from checkout |
| Env injection | [`examples/runner/with-env.yaml`](examples/runner/with-env.yaml) | Inline, Secret, and ConfigMap env |
| Private repo (SSH) | [`examples/runner/private-git.yaml`](examples/runner/private-git.yaml) | SSH key auth for git clone |
| CI pipeline (DAG) | [`examples/workflow/simple-dag.yaml`](examples/workflow/simple-dag.yaml) | Lint → test → build → deploy |
| Parallel job groups | [`examples/workflow/parallel-jobs.yaml`](examples/workflow/parallel-jobs.yaml) | Shared volumes, inherited env |
| Step timeout | [`examples/workflow/with-timeout.yaml`](examples/workflow/with-timeout.yaml) | Per-step and workflow-level timeout |
| Retry with backoff | [`examples/workflow/retry.yaml`](examples/workflow/retry.yaml) | Exponential backoff on failure |
| GitHub push trigger | [`examples/eventtrigger/github-push.yaml`](examples/eventtrigger/github-push.yaml) | Webhook → workflow with HMAC |
| Parameter mapping | [`examples/eventtrigger/parameter-mapping.yaml`](examples/eventtrigger/parameter-mapping.yaml) | Extract JSON fields to env vars |
| Cross-namespace ref | [`examples/advanced/cross-namespace-ref.yaml`](examples/advanced/cross-namespace-ref.yaml) | Runner template in another ns |
| Namespace quota | [`examples/advanced/namespace-quota.yaml`](examples/advanced/namespace-quota.yaml) | Limit concurrent workflows per ns |

The simplest runner:

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: hello
spec:
  image: alpine:3.19
  command: ["echo"]
  args: ["hello world"]
```

---

## Webhook Events

The operator runs an internal HTTP server on port **8080** alongside the
controllers in the same process. Routes register dynamically via EventTrigger
CRs — no deployment restart or config reload needed.

```
External ──▶ Ingress ──▶ Service (port 80)
                             │
                             ▼
                     Webhook Server (port 8080)
                       ├── HMAC-SHA256 validation
                       ├── JSON path parameter extraction
                       ├── Rate limiting (per-route)
                       ├── IP allow-listing
                       └── Workflow CR creation
```

- TLS terminates at Ingress (no TLS in-process)
- Supports GitHub, GitLab, Bitbucket, or any webhook source
- 202 Accepted on success, 401 on HMAC mismatch, 400 on bad payload

Deploy the ingress (customise hostname):

```bash
kubectl apply -f config/webhook/ingress.yaml
```

---

## RBAC & Multi-Tenancy

### Cluster-Scoped Mode (default)

The controller uses ClusterRole/ClusterRoleBinding, watching Runners,
Workflows, and EventTriggers across all namespaces. Single deployment
serves the entire cluster.

### Namespace-Scoped Mode

```bash
helm install runner-operator runner-operator/runner-operator \
  --set rbac.namespaced=true \
  --namespace team-a
```

Creates Role/RoleBinding in the release namespace only. Deploy one
instance per team/namespace for multi-tenant isolation.

### Convenience Roles

When `rbac.helpers.enable=true`, the chart installs admin/editor/viewer
ClusterRoles per CRD:

```bash
# Full access to all runner-operator CRDs
kubectl describe clusterrole runner-operator-admin

# Read-only access
kubectl describe clusterrole runner-operator-viewer

# Create/update but no delete
kubectl describe clusterrole runner-operator-editor
```

### Controller Internal RBAC

| Resource | Verbs |
|----------|-------|
| `runners` / `runners/status` | CRUD |
| `workflows` / `workflows/status` | CRUD |
| `eventtriggers` / `eventtriggers/status` | CRUD |
| `batch/jobs` / `batch/jobs/status` | CRUD + get |
| `pods` | get, list, watch |
| `secrets` | get, list, watch |
| `namespaces` | get, list, watch |
| `events` | create, patch |

---

## Production Guide

### High Availability

Set replicas ≥ 2 with leader election:

```bash
helm upgrade runner-operator runner-operator/runner-operator \
  --set manager.replicas=2 \
  --set manager.args[0]=--leader-elect \
  --set manager.resources.requests.cpu=100m \
  --set manager.resources.requests.memory=128Mi
```

Only the leader reconciles; standby replicas are hot standbys.

### Resource Sizing

| Workload | CPU Request | Memory Request | Notes |
|----------|-------------|----------------|-------|
| Light (< 10 CRs) | 10m | 64Mi | Default |
| Medium (10-100 CRs) | 100m | 256Mi | |
| Heavy (100+ CRs, complex DAGs) | 500m | 512Mi | Increase reconcile concurrency |

### Worker Node Sizing for Runners

Runner pods consume node resources based on their `spec.resources`. Plan
cluster capacity accordingly. The operator itself is lightweight (~10m CPU
idle).

### Security Hardening

The chart applies these defaults (Kubernetes Pod Security Standards
`restricted` profile v1.30+ compliant):

- `runAsNonRoot: true` — pod-level
- `runAsUser: 1000` — satisfies non-root for root-default images
- `seccompProfile: RuntimeDefault` — blocks ~44% of syscalls
- `allowPrivilegeEscalation: false` — no setuid binaries
- `capabilities.drop: [ALL]` — no kernel capabilities
- `readOnlyRootFilesystem: true` — container can't write to OS layer
- `backoffLimit: 0` — Kubernetes Job controller does not retry (operator owns retry)

### Namespace Isolation

Runner pods run in the same namespace as their Runner CR. Use
Kubernetes NetworkPolicies to restrict egress. See
[`examples/advanced/network-policy.yaml`](examples/advanced/network-policy.yaml)
for a working example.

> **Pod CIDR note:** The `10.0.0.0/8` block in the example assumes your cluster
> Pod/Service CIDR uses RFC 1918 space. Adjust the `except` list to
> match your cluster's actual CIDR ranges (`kubectl get nodes -o jsonpath='{.spec.podCIDR}'`).

### Upgrading

Helm upgrades are safe. The chart uses `crd.keep: true` to prevent CRD
deletion on uninstall:

```bash
helm upgrade runner-operator runner-operator/runner-operator \
  --namespace runner-operator-system
```

Kubebuilder-generated CRDs use structural schemas. Unknown fields on
existing CR objects are pruned by the API server (K8s 1.16+). If you
need backward compatibility with old fields, preserve them via
`spec.preserveUnknownFields` in a manual CRD patch.

---

## Monitoring

### Metrics (Prometheus)

Custom metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `runner_job_completed_total` | Counter | namespace, phase | Completed runner jobs |
| `runner_duration_seconds` | Histogram | namespace | Runner execution duration |
| `runner_active_count` | Gauge | namespace, phase | Currently active runners (Pending/Running) |
| `workflow_phase_transitions_total` | Counter | namespace, phase | Workflow phase changes |
| `workflow_duration_seconds` | Histogram | namespace | Workflow execution duration |
| `step_retries_total` | Counter | namespace | Step retry count |

Built-in controller-runtime metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `controller_runtime_reconcile_total` | Counter | controller, result | Reconciliation attempts |
| `controller_runtime_reconcile_errors_total` | Counter | controller | Reconciliation errors |
| `controller_runtime_reconcile_time_seconds` | Histogram | controller | Reconciliation duration |
| `workqueue_depth` | Gauge | name | Work queue depth |
| `workqueue_adds_total` | Counter | name | Items added to queue |
| `rest_client_requests_total` | Counter | code, method, host | API server requests |

Enable ServiceMonitor:

```bash
helm upgrade runner-operator runner-operator/runner-operator \
  --set prometheus.enable=true
```

### Key Alerts

```yaml
# Reconciliation errors
- alert: RunnerOperatorReconcileErrors
  expr: rate(controller_runtime_reconcile_errors_total{controller="runner"}[5m]) > 0.1
  for: 5m
  labels:
    severity: warning

# Work queue backing up
- alert: RunnerOperatorWorkQueueDepth
  expr: workqueue_depth > 100
  for: 5m
  labels:
    severity: warning

# Controller restart
- alert: RunnerOperatorRestarts
  expr: rate(kube_pod_container_status_restarts_total{namespace="runner-operator-system"}[15m]) > 0
  labels:
    severity: critical
```

### Logs

```bash
# Controller logs (requires stern; alternatively use kubectl below)
stern -n runner-operator-system -l control-plane=controller-manager

# Alternative without stern:
kubectl -n runner-operator-system logs deployment/runner-operator-controller-manager -c manager --tail=100 -f

# Runner pod logs (stdout of the user's image)
kubectl logs -l runner-operator.io/runner=<runner-name> --all-containers

# Git clone init container logs
kubectl logs -l runner-operator.io/runner=<runner-name> -c git-clone
```

Log format follows Kubernetes structured logging conventions (key-value
pairs, not string formatting).

---

## Troubleshooting

### Runner stuck in Pending

```
kubectl describe runner <name>
kubectl describe job <name>
kubectl describe pod -l runner-operator.io/runner=<name>
```

Common causes:
- No scheduler capacity (check node resources, add tolerations)
- Image pull failure (check image tag, pull secrets)
- PVC binding pending (check storage class)

### Workflow step never transitions

```
kubectl get workflow <name> -o jsonpath='{.status.stepStatuses}'
```

Check:
- `dependsOn` refers to existing step names
- `when` expression evaluates correctly
- Parent step completed successfully (Succeeded, not Failed)

### EventTrigger not firing

```
kubectl get eventtrigger <name> -o jsonpath='{.status.registered}'
```

If `registered` is false, check:
- Webhook path is unique (no two EventTriggers share a path)
- Secret `hmac-secret` exists in the trigger's namespace
- Webhook server is running (`kubectl logs -n runner-operator-system`)

### Spec drift not updating the Job

The controller only recreates the Job after the current Job finishes
(phase = Complete or Failed). In-flight jobs are not torn down — the
change takes effect on the next run.

### Leader election conflicts

```
kubectl describe lease -n runner-operator-system
```

If multiple replicas contend, check that `--leader-elect` is in args
and the Deployment has appropriate `podAntiAffinity`.

---

## Development

### Prerequisites

- Go 1.24+
- Docker
- Kind (for e2e)
- kubebuilder 4.x (for scaffolding)

### Commands

```bash
make test             # Unit tests (envtest)
make test-e2e         # Full e2e on Kind (creates + destroys cluster)
make lint-fix         # Auto-fix code style
make run              # Run locally (current kubecontext)
make deploy           # Deploy via Kustomize
make build-installer  # Generate dist/install.yaml
make manifests        # Regenerate CRDs + RBAC from markers
make generate         # Regenerate DeepCopy methods
```

---

## Design Decisions

### Why is the git clone script generated inline?

The init container command is built at runtime by the controller rather than
using a dedicated init image. This matches how Tekton, Argo Workflows, and
the kubebuilder deploy-image plugin work.

Rationale:
- `alpine/git` is ~5 MB and cached on most cluster nodes — no extra image to build or pull
- The script is simple (clone + checkout) and changes infrequently
- When you need custom logic (Git LFS, submodules, sparse checkout), swap in
  your own init image by referencing it in a custom Runner

### Why is the webhook server in the same binary as the controller?

The event webhook server runs on port 8080 inside the controller-manager
pod, not as a separate Deployment.

Rationale:
- Single binary, single Deployment, single Service — simpler operations
- The webhook does lightweight work (HMAC check + CR creation) — contention is negligible
- Scales naturally with the manager: 2 replicas = 2 webhook listeners behind a Service
- Can be extracted to a separate Deployment later if throughput demands it
  (the controller and webhook communicate only through the API server)

The webhook port is configurable via `--webhook-event-addr` (default `:8080`).
Override it when running the manager:

```bash
export IMG=your-registry/runner-operator:tag
make run ARGS="--webhook-event-addr=:9090"
```

### Why tag-driven releases?

Chart and binary are published together from a git tag (`v0.3.0`), not on
every push to main. This ensures:

- Chart version always matches the release tag
- Release notes document CRD upgrade procedures
- Users subscribe to stable releases, not every commit

## Architecture Blueprint

See [`arch/blueprint.md`](arch/blueprint.md) for:

- State machine diagrams (Runner, Workflow, Step)
- Pod layout diagram (init container, shared volume, security context)
- Controller reconciliation flow charts
- Git clone script generation
- Design decisions and rationale
- Event-driven trigger architecture
- Job grouping with shared volumes
