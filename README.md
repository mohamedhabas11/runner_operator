# runner-operator

A Kubernetes operator that executes arbitrary OCI container images as disposable Jobs
and orchestrates multi-step CI/CD pipelines with DAG-based Workflows.

## CRDs

| CRD | Purpose |
|-----|---------|
| **Runner** | Maps 1:1 to a batch/v1.Job. Runs any OCI image with env, volumes, git cloning, and resource limits. |
| **Workflow** | DAG of named Runners with dependency gates, retries, timeouts, and parallel job grouping. |
| **EventTrigger** | Registers external webhook routes that validate payloads and create Workflow CRs dynamically. |

## Quick Start

```bash
# Install CRDs and deploy the controller
make deploy IMG=example.com/runner-operator:v0.0.1

# Apply a simple Runner
kubectl apply -f - <<EOF
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: hello
  namespace: default
spec:
  image: alpine:3.19
  command: ["echo"]
  args: ["hello world"]
EOF

# Watch it complete
kubectl get runner hello -w
```

## Usage Examples

### Runner with git repo

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: build
spec:
  image: golang:1.23
  command: ["go", "test", "./..."]
  gitRepo:
    url: https://github.com/org/repo.git
    revision: main
  timeoutAfter: 30m
```

### Workflow with dependencies

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
      command: ["golangci-lint", "run", "./..."]
    - name: test
      image: golang:1.23
      command: ["go", "test", "./..."]
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

### Workflow with parallel job groups

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
metadata:
  name: ci
spec:
  jobs:
    - name: build
      steps:
        - name: compile
          image: golang:1.23
          command: ["go", "build", "-o", "app", "./cmd"]
    - name: test
      needs: [build]
      steps:
        - name: unit
          image: golang:1.23
          command: ["go", "test", "./..."]
    - name: deploy
      needs: [test]
      when: on_success
      steps:
        - name: push
          image: bitnami/kubectl
          command: ["kubectl", "set", "image", "deployment/app", "app=myapp:latest"]
```

### EventTrigger (webhook → Workflow)

```yaml
apiVersion: runners.runner-operator.io/v1alpha1
kind: EventTrigger
metadata:
  name: github-push
spec:
  webhook:
    path: /webhooks/github-push
    secretRef:
      name: github-webhook-secret
  workflowTemplate:
    name: ci-workflow
    namespace: default
  parameters:
    - name: GITHUB_REF
      source: $.ref
    - name: GITHUB_SHA
      source: $.after
```

## Installation

### Option 1: Kustomize (single-command install)

```bash
kubectl apply -f https://raw.githubusercontent.com/mohamedhabas11/runner_operator/main/dist/install.yaml
```

### Option 2: Makefile (development)

```bash
make deploy IMG=example.com/runner-operator:v0.0.1
```

### Option 3: Helm (coming soon)

## Webhook Events

The operator runs an internal HTTP server on port 8080 that receives external
webhook events. Routes are registered dynamically via EventTrigger CRs:

```
External Webhook ──▶ Ingress ──▶ Service (port 80)
                                     │
                                     ▼
                             Webhook Server (port 8080)
                               ├── HMAC validation
                               ├── parameter extraction
                               └── Workflow CR creation
```

- No TLS — terminate at Ingress
- GitHub-compatible HMAC-SHA256 signature validation
- IP allow-listing per route
- Per-route rate limiting
- Deploy `config/webhook/ingress.yaml` (customize hostname first)

## Security

- All pods run as non-root (user 1000) with `seccomp: RuntimeDefault`
- Containers have `ReadOnlyRootFilesystem: true` and `ALL` capabilities dropped
- Webhook payloads validated via HMAC-SHA256

## Development

```bash
# Prerequisites
go 1.24+, docker, kind (for e2e tests)

# Unit tests
make test

# End-to-end tests (creates a Kind cluster)
make test-e2e

# Regenerate CRDs and RBAC
make manifests generate

# Build installer bundle
make build-installer IMG=<registry>/<project>:tag
```

### Makefile targets

| Target | Description |
|--------|-------------|
| `make test` | Unit tests with envtest |
| `make test-e2e` | Full e2e suite on Kind |
| `make lint-fix` | Auto-fix code style |
| `make run` | Run locally (uses current kubecontext) |
| `make deploy` | Deploy controller to current cluster |
| `make build-installer` | Generate dist/install.yaml |

## Architecture

See [`arch/blueprint.md`](arch/blueprint.md) for detailed architecture diagrams,
reconciliation flow charts, and design decisions.
