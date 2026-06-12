# runner-operator — Architecture Blueprint

## Overview

runner-operator runs arbitrary OCI images as disposable Kubernetes Jobs.
Two CRDs — **Runner** (single job) and **Workflow** (DAG of chained
Runners) — define what to run; two controllers reconcile them into
batch/v1.Job and child Runner objects.

```
.──────────────────────────────────────────────────────────────────────.
│                         Kubernetes Cluster                          │
│                                                                      │
│  ┌──────────────────────┐            ┌──────────────────────┐        │
│  │      Runner CR       │            │     Workflow CR      │        │
│  │   ─────────────────   │            │   ─────────────────── │        │
│  │   image: nginx       │            │   timeout: "10m"     │        │
│  │   command: ["sh"]    │            │   steps:             │        │
│  │   args: ["..."]      │            │     - name: build    │        │
│  │   env: [...]         │            │     - name: test     │        │
│  │   gitRepo: {...}     │            │     - name: deploy   │        │
│  └──────────┬───────────┘            └──────────┬────────────┘        │
│             │                                    │                   │
│             │  reconcile                         │  reconcile         │
│             ▼                                    ▼                   │
│  ┌──────────────────────┐            ┌──────────────────────┐        │
│  │   Runner Controller  │            │  Workflow Controller  │        │
│  │  ───────────────────  │            │  ──────────────────── │        │
│  │  watches Runners     │            │  watches Workflows    │        │
│  │  + owned Jobs        │            │  + owned Runners      │        │
│  │  creates batch/v1.Job│            │  creates Runner CR    │        │
│  └──────────┬───────────┘            └──────────┬────────────┘        │
│             │                                    │                   │
│             │  .Owns()                            │  .Owns()          │
│             ▼                                    ▼                   │
│  ┌──────────────────────┐            ┌──────────────────────┐        │
│  │    batch/v1.Job      │◄───────────│  Runner CR (per step)│        │
│  │  ───────────────────  │  creates   │  ─────────────────── │        │
│  │  runner-<name>-job   │            │  runner-<wf>-<step>  │        │
│  └──────────┬───────────┘            └──────────────────────┘        │
│             │                                                        │
│             │  owns Pod                                              │
│             ▼                                                        │
│  ┌──────────────────────┐                                            │
│  │  Pod (ephemeral)     │                                            │
│  │  ───────────────────  │                                            │
│  │  init: git-clone     │   (if gitRepo is set)                     │
│  │  main: runner        │   (user image + command)                  │
│  └──────────────────────┘                                            │
'──────────────────────────────────────────────────────────────────────'
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

git clone --depth 1 <URL> /workspace/repo
git -C /workspace/repo fetch origin "<revision>"
git -C /workspace/repo checkout "<revision>"

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
  Runner     ── owns ──▶  batch/v1.Job  ── owns ──▶  Pod
  Workflow   ── owns ──▶  Runner (per step)
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
| Both controllers     | events                                 | create, patch               |

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
