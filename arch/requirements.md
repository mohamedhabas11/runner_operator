# Operational Requirements

## Must (Non-Negotiable)
- Execute arbitrary OCI container images as Kubernetes Jobs
- Inject environment variables from ConfigMaps, Secrets, and inline definitions at runtime
- Mount Kubernetes Secrets and ConfigMaps as volumes
- Support additional volume mounts (emptyDir, PVC, etc.)
- Detect and reconcile spec drift (hash-based validation; re-create Jobs on spec change)
- Prevent resource leaks: all Jobs and Pods must define `timeoutAfter` (Go duration format, e.g., `"30m"`)

## Must Not
- Leave owned resources behind after Runner/Workflow deletion (rely on Kubernetes ownership chains)
- Allow long-running Jobs to hang indefinitely

## Should (Strongly Desired)
- Define Workflows as YAML DAGs (similar to GitHub Actions or GitLab CI)
- Support step-level retry logic with exponential backoff
- Support chaining steps with conditional gates (`on_success`, `on_failure`, `always`)
- Implement cycle detection in Workflow dependency graphs

## Should Not
- Introduce custom frameworks or unnecessary abstraction layers
- Use opaque or auto-generated code patterns; prioritize clarity over cleverness
- Bloat the codebase; prefer Kubernetes client-go and controller-runtime standard patterns

## Non-Functional Qualities
- Security: enforce non-root containers, read-only filesystems, and capability dropping
- Observability: emit Kubernetes Events for step transitions and failures
- Debuggability: use structured logging for reconciliation decisions
