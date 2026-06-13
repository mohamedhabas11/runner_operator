# runner-operator — Architecture Blueprint

## Overview

runner-operator runs arbitrary OCI images as disposable Kubernetes Jobs.
Three CRDs — **Runner** (single job), **Workflow** (DAG of chained
Runners), and **EventTrigger** (webhook → Workflow) — define what to run;
three controllers reconcile them into batch/v1.Job, child Runner objects,
and Workflow CRs.

```
.──────────────────────────────────────────────────────────────────────────────.
│                           Kubernetes Cluster                                 │
│                                                                              │
│  ┌────────────────────┐  ┌────────────────────┐  ┌──────────────────────┐   │
│  │     Runner CR      │  │    Workflow CR     │  │   EventTrigger CR    │   │
│  │  ─────────────────  │  │  ─────────────────  │  │  ───────────────────  │   │
│  │  image: nginx      │  │  timeout: "10m"    │  │  webhook:            │   │
│  │  command: ["sh"]   │  │  steps: [...]      │  │    path: /github     │   │
│  │  gitRepo: {...}    │  │  jobs: [...]       │  │  workflowTemplate    │   │
│  └─────────┬──────────┘  └─────────┬──────────┘  └──────────┬───────────┘   │
│            │                      │                         │                │
│            │  reconcile           │  reconcile              │  reconcile     │
│            ▼                      ▼                         ▼                │
│  ┌────────────────────┐  ┌────────────────────┐  ┌──────────────────────┐   │
│  │ Runner Controller  │  │ Workflow Controlle │  │ EventTrigger Contrl │   │
│  │ ──────────────────  │  │ ──────────────────  │  │ ───────────────────  │   │
│  │ watches Runners    │  │ watches Workflows  │  │ watches EventTriggr │   │
│  │ + owned Jobs       │  │ + owned Runners    │  │ owns webhook routes  │   │
│  │ creates batch/Job  │  │ creates Runner CR  │  │ register/deregister  │   │
│  └─────────┬──────────┘  └─────────┬──────────┘  │ + creates Workflow   │   │
│            │                      │              └──────────────────────┘   │
│            │  .Owns()             │  .Owns()              │                 │
│            ▼                      ▼                       │                 │
│  ┌────────────────────┐  ┌────────────────────┐          │                 │
│  │   batch/v1.Job     │◄─│ Runner CR (per step)│          │                 │
│  │ ──────────────────  │  │ ──────────────────  │          │                 │
│  │ runner-<name>-job  │  │ runner-<wf>-<step>  │          │                 │
│  └─────────┬──────────┘  └────────────────────┘          │                 │
│            │                                              │                 │
│            │  owns Pod                                    │  SetController  │
│            ▼                                              ▼ Reference       │
│  ┌────────────────────┐                           ┌──────────────────────┐  │
│  │  Pod (ephemeral)   │                           │  Workflow CR         │  │
│  │ ──────────────────  │                           │  (created by webhook)│  │
│  │ init: git-clone    │                           └──────────────────────┘  │
│  │ main: user image   │                                                      │
│  └────────────────────┘                                                      │
'──────────────────────────────────────────────────────────────────────────────'
```

---

## CRD: Runner

Maps 1:1 to a `batch/v1.Job`.

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: my-runner
spec:
  image: alpine:3.19            # required — any OCI image
  command: ["sh", "-c"]         # optional — overrides ENTRYPOINT
  args: ["echo hello"]          # optional — overrides CMD
  env:                          # optional — extra env vars
    - name: FOO
      value: bar
  envFrom:                      # optional — load env from Secret/CM
    - secretRef:
        name: my-secret
  volumes:                      # optional — extra volumes
    - name: data
      emptyDir: {}
  mounts:                       # optional — extra volume mounts
    - name: data
      mountPath: /data
  resources:                    # optional — resource requests/limits
    requests:
      cpu: 100m
      memory: 64Mi
    limits:
      cpu: 500m
      memory: 128Mi
  gitRepo:                      # optional — clone a repo first
    url: https://github.com/org/repo.git
    revision: main
    path: sub/dir
    auth:
      secretRef:
        name: git-credentials
  timeoutAfter: 30m             # optional — ActiveDeadlineSeconds
```

### Runner Lifecycle

```
.──────────.     create Job     .──────────.   pod starts    .──────────.
│ Pending  │───────────────────▶│ Running  │───────────────▶│ Running  │
'──────────'                    '──────────'                '──────────'
                                     │  │                        │
                    Job succeeds     │  │  Job fails              │ step-two …
                                     ▼  ▼                        ▼
                               .──────────.                .──────────.
                               │ Succeeded│                │  Failed  │
                               '──────────'                '──────────'
```

Transition drivers:

| Phase     | Trigger |
|-----------|---------|
| Pending   | No Job exists — controller creates one |
| Running   | `job.status.startTime` is set |
| Succeeded | `job.status.conditions[type=Complete]` is True |
| Failed    | `job.status.conditions[type=Failed]` is True |

### Spec-Drift Detection

A SHA-256 hash of `runner.spec` is stored in `.status.resourceHash` and
in a label on the Job.  On each reconcile:

```
currentHash ≠ storedHash  AND  existing Job is complete
    → delete Job, recreate with new spec
```

If the Job is still running the controller defers — no mid-flight tear-down.

---

## CRD: Workflow

DAG of named Runners.  The controller converts each step into a Runner and
tracks per-step status independently.

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
metadata:
  name: ci-pipeline
spec:
  timeout: "15m"
  steps:
    - name: lint
      image: golang:1.23
      command: ["golangci-lint"]
      args: ["run", "./..."]
      timeout: "5m"

    - name: test
      image: golang:1.23
      command: ["go"]
      args: ["test", "./..."]
      dependsOn: [lint]
      when: on_success

    - name: deploy
      image: bitnami/kubectl
      command: ["kubectl", "apply", "-f", "deploy.yaml"]
      dependsOn: [test]
      retry:
        maxRetries: 3
        backoff:
          initialDelay: 5s
          maxDelay: 30s
```

### Step Decision Tree

```
               .─────────────────────────────.
               │     evaluateStep(step)       │
               '─────────────────────────────'
                          │
            ───┬──────────┼──────────┬───
               │          │          │
               ▼          ▼          ▼
          .────────.  .────────.  .────────.
          │ stepRun │  │ stepWait│  │stepSkip│
          '────────'  '────────'  '────────'
               │
          ─────┴─────
          │          │
          ▼          ▼
     .────────┐ ┌─────────.
     │Runner   │ │ Create  │
     │ exists? │ │ Runner  │
     '────────' │         │
          │     '─────────'
     ─────┴───
     │
     ▼
.──────────────.
│ update step  │
│ status from  │
│ Runner phase │
'──────────────'
```

Dependency gates:

| `when`        | Dependencies succeeded | Dependencies failed |
|---------------|------------------------|---------------------|
| `on_success`  | **stepRun**            | stepSkip            |
| `on_failure`  | stepSkip               | **stepRun**         |
| `always`      | **stepRun**            | **stepRun**         |

Cycle detection uses DFS over the dependency graph; a cycle aborts the
workflow (no Runners created).

### Workflow Timeout

```
.───────────────.
│  Workflow CR  │
│  applied       │
'───────┬───────'
        │
        ▼
.───────────────.
│ .status.Start │
│ Time recorded  │
'───────┬───────'
        │
        │  RequeueAfter(timeout)
        ▼
.─────────────────────.
│ elapsed > spec.time? │
'──────────┬──────────'
           │
     ┌─────┴─────┐
     ▼           ▼
    YES          NO
     │           │
     ▼           ▼
.───────────.  .───────────────.
│ Mark all  │  │ RequeueAfter  │
│ steps     │  │ (remaining    │
│ Failed    │  │  duration)    │
│ Set WF    │  '───────┬───────'
│ phase to  │          │
│ Failed    │          │
'───────────'          │
                       └───▶ back to elapsed check
```

---

## Pod Layout (with `gitRepo`)

```
.──────────────────────────────────────────────────.
│  Pod: runner-<name>-job-<hash>                   │
│                                                  │
│  Init Containers:                                │
│  ┌───────────────────────────────────────────┐   │
│  │  name:     git-clone                       │   │
│  │  image:    alpine/git:latest               │   │
│  │  command:  ["/bin/sh", "-c"]               │   │
│  │  args:     git clone --depth 1 <URL> ...   │   │
│  │  secCtx:   runAsUser: 1000                │   │
│  │            runAsNonRoot: true              │   │
│  │  mounts:   /workspace  ←  emptyDir        │   │
│  │            /etc/git-auth  ←  Secret       │   │
│  │            (only if auth set)              │   │
│  └───────────────────────────────────────────┘   │
│                                                  │
│  Containers:                                     │
│  ┌───────────────────────────────────────────┐   │
│  │  name:       runner                        │   │
│  │  image:      <user-specified>              │   │
│  │  command:    <user-specified>              │   │
│  │  args:       <user-specified>              │   │
│  │  workingDir: /workspace/repo[/sub/path]    │   │
│  │  secCtx:     readOnlyRootFilesystem: true  │   │
│  │              allowPrivilegeEscalation: fals │   │
│  │              capabilities.drop: [ALL]       │   │
│  │  mounts:     /workspace  ←  emptyDir       │   │
│  └───────────────────────────────────────────┘   │
│                                                  │
│  Volumes:                                        │
│    runner-operator-git-repo  (emptyDir)          │
│    runner-operator-git-auth  (Secret, optional)  │
│                                                  │
│  Pod SecurityContext:                             │
│    runAsUser: 1000    runAsNonRoot: true         │
│    seccompProfile: RuntimeDefault                │
'──────────────────────────────────────────────────'
```

Without `gitRepo` the init container and shared volume are omitted — the
pod contains only the runner container.

### Generated Git Clone Script

```
if [ -f /etc/git-auth/ssh-privatekey ]; then
  mkdir -p ~/.ssh
  cp /etc/git-auth/ssh-privatekey ~/.ssh/id_rsa
  chmod 600 ~/.ssh/id_rsa
  ssh-keyscan github.com gitlab.com bitbucket.org >> ~/.ssh/known_hosts 2>/dev/null || true
fi

git clone --depth 1 -- <URL> /workspace/repo
git -C /workspace/repo fetch origin -- <revision>
git -C /workspace/repo checkout <revision>

# If gitRepo.path is set, cd into the subdirectory for the init container
if [ -n "<path>" ]; then
  cd /workspace/repo/<path>
fi

rm -rf ~/.ssh    # only if auth was used
```

---

## Controller Reconciliation Loops

### Runner Controller

```
.──────────────────────────────────────────────.
│              Reconcile(runner)                │
'──────────────────────┬───────────────────────'
                       │
                       ▼
              .─────────────────────.
              │ Fetch Runner         │
              │ Compute specHash     │
              │ Lookup existing Job  │
              '──────────┬──────────'
                         │
                  ┌──────┴──────┐
                  ▼             ▼
            Job not found   Job found
                  │             │
                  ▼             ▼
          .──────────────┐ ┌──────────────────.
          │ buildJob()    │ │ hash match?       │
          │ Set OwnerRef  │ │  ├── no + done    │
          │ Create Job    │ │  │   delete+recre │
          │ Set Pending   │ │  ├── no + running │
          '──────────────' │  │   defer         │
                           │  └── yes           │
                           │      updateStatus  │
                           '──────────────────'
```

Watches:
- `For(&runnersv1alpha1.Runner{})` — spec changes
- `Owns(&batchv1.Job{})` — job status transitions

### Workflow Controller

```
.──────────────────────────────────────────────.
│            Reconcile(workflow)                │
'──────────────────────┬───────────────────────'
                       │
                       ▼
              .─────────────────────.
              │ Fetch Workflow       │
              │ Detect cycles        │
              │ Check timeout        │
              │ List owned Runners   │
              '──────────┬──────────'
                         │
                         ▼
              .─────────────────────.
              │  reconcileSteps()    │
              │  per step:           │
              │    evaluate          │
              │    → run / wait / skip│
              │  create Runner       │
              │  or update status    │
              '──────────┬──────────'
                         │
                         ▼
              .─────────────────────.
              │ computePhase()       │
              │ Patch status         │
              │ RequeueAfter(timeout)│
              '─────────────────────'
```

Watches:
- `For(&runnersv1alpha1.Workflow{})` — spec changes
- `Owns(&runnersv1alpha1.Runner{})` — runner status transitions

---

## Cleanup & Ownership

Kuberentes garbage-collects child resources when their owner is deleted
(controller reference chain):

```
  Runner       ── owns ──▶  batch/v1.Job  ── owns ──▶  Pod
  Workflow     ── owns ──▶  Runner (per step)
  EventTrigger ── owns ──▶  Workflow (webhook-created)
```

No finalizer logic needed.  `BackoffLimit: 0` on Jobs ensures the operator
(not the Job controller) owns retry decisions.

---

## RBAC

| Subject              | Resources                              | Verbs                       |
|----------------------|----------------------------------------|-----------------------------|
| Runner controller    | runners, runners/status                | CRUD                        |
| Runner controller    | batch/jobs, batch/jobs/status          | CRUD + get                  |
| Runner controller    | pods                                   | get, list, watch            |
| Workflow controller  | workflows, workflows/status            | CRUD                        |
| Workflow controller  | runners, runners/status                | CRUD + get                  |
| EventTrigger control | eventtriggers, eventtriggers/status    | CRUD                        |
| EventTrigger control | workflows                              | get, list, watch, create    |
| EventTrigger control | secrets                                | get, list, watch            |
| EventTrigger control | namespaces                             | get, list, watch            |
| All three controlers | events                                 | create, patch               |

---

---

## New: Event-Driven Triggers

The operator can now react to external events (GitHub webhooks) and create Workflow CRs
automatically.  This is the system's equivalent of GitHub Actions `on: [push, pull_request]`.

### Architecture

```
.──────────────────────────────────────────────────────────────────────.
│                         Kubernetes Cluster                          │
│                                                                      │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                 Manager Pod (controller-manager)              │    │
│  │  ┌──────────────┐  ┌────────────────┐  ┌──────────────────┐ │    │
│  │  │ Runner       │  │ Workflow       │  │ EventTrigger     │ │    │
│  │  │ Controller   │  │ Controller     │  │ Controller       │ │    │
│  │  └──────────────┘  └────────────────┘  └──────────────────┘ │    │
│  │                                                              │    │
│  │  ┌──────────────────────────────────────────────────────┐   │    │
│  │  │ Webhook Server (port 8080)                          │   │    │
│  │  │  ┌──────────────────────┐  ┌──────────────────────┐  │   │    │
│  │  │  │ /webhooks/github     │  │ /webhooks/generic    │  │   │    │
│  │  │  │ → HMAC validation    │  │ → payload parsing    │  │   │    │
│  │  │  │ → parameter extract  │  │ → CR creation        │  │   │    │
│  │  │  └──────────────────────┘  └──────────────────────┘  │   │    │
│  │  └──────────────────────────────────────────────────────┘   │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                      │
│  ┌──────────────┐  ┌──────────────────┐  ┌──────────────────────┐  │
│  │ EventTrigger  │  │ Workflow (with   │  │ Ingress              │  │
│  │ CR            │  │ Jobs) CR         │  │ → /webhooks/*        │  │
│  │ ──────────────  │ ─────────────────  │  └──────────────────────┘  │
│  │ webhook:       │  │ jobs:            │                            │
│  │   path, secret │  │  - name: build  │                            │
│  │ workflowRef    │  │    steps: [...] │                            │
│  │ parameters     │  │  - name: test   │                            │
│  └──────────────┘  │    needs: [build] │                            │
│                    └──────────────────┘                            │
'──────────────────────────────────────────────────────────────────────'

External GitHub Webhook
  ── HTTPS ──▶ Ingress ──▶ Service ──▶ /webhooks/github ──▶ Create Workflow CR
```

### CRD: EventTrigger

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: EventTrigger
metadata:
  name: github-push-trigger
spec:
  webhook:
    path: /webhooks/github-push        # unique path on the webhook server
    secretRef:                         # HMAC secret for payload validation
      name: github-webhook-secret
  workflowTemplate:
    name: ci-workflow                  # template Workflow CR to instantiate
    namespace: default
  parameters:                          # optional: map webhook JSON → workflow env vars
    - name: GITHUB_REF
      source: $.ref                    # dot-path from webhook payload
    - name: GITHUB_REPO
      source: $.repository.full_name
    - name: GITHUB_SHA
      source: $.after
```

**Behaviour:**
- Each `EventTrigger` registers a handler at `spec.webhook.path` on the internal webhook server (port 8080)
- On receiving a POST to that path, validates the HMAC signature against the referenced Secret
- Parses the JSON payload, extracts parameters via dot-path selectors
- Creates a new Workflow CR from the template, injecting extracted values as env vars in env step
- Responds `202 Accepted` on success, `401 Unauthorized` on HMAC mismatch, `400` on bad payload

### Webhook Server

- Runs inside the manager binary on port **8080** (separate from admission webhooks on 9443)
- Port is configurable via `--webhook-event-addr` (default `:8080`)
- No TLS — TLS termination handled at the Ingress
- Routes are registered/deregistered dynamically as EventTrigger CRs are created/updated/deleted
- GitHub webhook handler: validates HMAC-SHA256 with the Secret key, parses push/pull_request/release events
- Generic handler: for any other webhooks with custom authentication

### Service & Ingress

```yaml
# Service (config/webhook/service.yaml)
apiVersion: v1
kind: Service
metadata:
  name: webhook-server
spec:
  selector:
    control-plane: controller-manager
  ports:
    - port: 80
      targetPort: 8080
      name: webhook

# Ingress (config/webhook/ingress.yaml)
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: webhook-ingress
spec:
  rules:
    - host: webhooks.example.com
      http:
        paths:
          - path: /webhooks
            pathType: Prefix
            backend:
              service:
                name: webhook-server
                port:
                  number: 80
```

---

## New: Job Grouping (Workflow Extension)

Workflows can now group steps into **Jobs** — parallel execution units with shared volumes,
matching GitHub Actions' job model.

### Extended Workflow CRD

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
spec:
  timeout: "15m"

  # NEW: Jobs field — groups steps into parallel execution units.
  # Backward-compatible: if jobs is empty, falls back to flat steps.
  jobs:
    - name: build
      env:                                      # job-level env vars
        - name: GOOS
          value: linux
      steps:
        - name: checkout
          image: alpine/git:latest
          command: ["git", "clone", "--depth", "1", "https://...", "/workspace"]
        - name: compile
          image: golang:1.23
          command: ["go", "build", "-o", "app", "./cmd"]
          dependsOn: [checkout]

    - name: test
      needs: [build]                            # waits for "build" job to complete
      env:
        - name: DB_URL
          value: postgres://localhost/test
      steps:
        - name: unit
          image: golang:1.23
          command: ["go", "test", "./..."]
        - name: lint
          image: golang:1.23
          command: ["golangci-lint", "run"]
          # steps within a job run sequentially (as today) unless explicitly parallel

    - name: deploy
      needs: [test]                              # only after test succeeds
      when: on_success                           # job-level conditional
      steps:
        - name: push-image
          image: bitnami/kubectl:latest
          command: ["kubectl", "set", "image", "deployment/app", "app=myapp:latest"]
```

### Parallelism Model

```
Jobs without "needs" → run IN PARALLEL
Jobs with "needs"    → wait for ALL dependency Jobs to complete

Within a job:        → steps run SEQUENTIALLY (existing step DAG model)

Example:
  build ──▶ test ──▶ deploy
  push  ──┘            │
                       ▼
                  notify ──▶ archive
```

### Shared Volume Between Steps in a Job

Steps in the same job share a PVC for artifact passing:

```yaml
spec:
  jobs:
    - name: build
      sharedVolume:
        persistentVolumeClaim:         # existing PVC, or omit for emptyDir
          claimName: build-workspace
        emptyDir: {}
      steps:
        - name: compile
          image: golang:1.23
          command: ["go", "build", "-o", "/workspace/app"]
        - name: archive
          image: alpine
          command: ["tar", "-czf", "/workspace/app.tar.gz", "/workspace/app"]
          dependsOn: [compile]
```

If `sharedVolume` is set, all steps in the job mount it at `/workspace`. Steps can pass artifacts
through this volume.

### Migration Path

Existing Workflows (flat `spec.steps`) continue to work unchanged. The controller checks
`spec.jobs` first — if present, uses job-level orchestration; otherwise falls back to the
existing flat-step reconciler path.

---

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| `BackoffLimit: 0` | Operator owns retry. Job-level retries fight the controller. |
| Spec hash in label + status | Drift detection without full-spec comparison every reconcile. |
| `ReadOnlyRootFilesystem: true` on runner | Container should not write to the OS layer. |
| `alpine/git:latest` for init | ~5 MB, widely cached, only git + musl. |
| `RunAsUser: 1000` at pod level | Satisfies `RunAsNonRoot` for root-default images (busybox etc). |
| Workflow timeout via `RequeueAfter` | CR lacks cron — requeue is the standard controller-runtime pattern. |
| Status via `MergeFrom` patch | Avoids conflicts and redundant full-object writes. |
| **Webhook server integrated in manager (port 8080)** | Single binary, single deployment; lightweight processing; can extract later if needed |
| **Jobs as logical step groups with shared PVC** | Each step still creates a Runner CR (minimal change); parallel job coordination via controller; shared volume for artifacts |
| **EventTrigger as new CRD** | Declarative, Kubernetes-native; controller reconciles triggers → registers/deregisters webhook routes |
| **Dot-path JSON selectors for parameters** | Zero new dependencies; sufficient for common GitHub webhook fields; upgrade to CEL later |
| **Backward-compatible Workflow extension** | Existing flat `spec.steps` Workflows continue unchanged; `spec.jobs` is additive |
| **RunnerRef cross-namespace (`runnerRef.namespace`)** | Workflow steps can reference Runner templates in any namespace; defaults to workflow's namespace for backward compat |
| **AllowedNamespaces enforcement** | EventTrigger can restrict which namespaces its webhook may create Workflows in; enforced in both webhook server and controller (defence in depth) |
| **EventTrigger → Workflow owner reference** | Webhook-created Workflows are owned by their EventTrigger; GC cascades on trigger deletion (no orphaned Workflows) |
