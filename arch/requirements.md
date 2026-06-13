# Operational Requirements

## Must (Non-Negotiable)
- Execute arbitrary OCI container images as Kubernetes Jobs
- Inject environment variables from ConfigMaps, Secrets, and inline definitions at runtime
- Mount Kubernetes Secrets and ConfigMaps as volumes
- Support additional volume mounts (emptyDir, PVC, etc.)
- Detect and reconcile spec drift (hash-based validation; re-create Jobs on spec change)
- Prevent resource leaks: all Jobs and Pods must define `timeoutAfter` (Go duration format, e.g., `"30m"`)
- **Receive external webhook events (GitHub push, pull_request, etc.) and create Workflow CRs automatically** [NEW]
- **Support parallel job grouping with `needs` dependencies** [NEW]
- **Validate webhook payloads via HMAC signature** [NEW]

## Must Not
- Leave owned resources behind after Runner/Workflow deletion (rely on Kubernetes ownership chains)
- Allow long-running Jobs to hang indefinitely
- **Expose internal cluster details to external webhook callers** [NEW]

## Should (Strongly Desired)
- Define Workflows as YAML DAGs (similar to GitHub Actions or GitLab CI)
- Support step-level retry logic with exponential backoff
- Support chaining steps with conditional gates (`on_success`, `on_failure`, `always`)
- Implement cycle detection in Workflow dependency graphs
- **Support parameter extraction from webhook payloads (e.g., branch name → env var)** [NEW]
- **Provide Ingress manifests for webhook exposure** [NEW]
- **Support workflow template reuse (EventTrigger → template Workflow CR)** [NEW]

## Should Not
- Introduce custom frameworks or unnecessary abstraction layers
- Use opaque or auto-generated code patterns; prioritize clarity over cleverness
- Bloat the codebase; prefer Kubernetes client-go and controller-runtime standard patterns
- **Require TLS in the webhook server itself — terminate at Ingress** [NEW]

## Non-Functional Qualities
- Security: enforce non-root containers, read-only filesystems, and capability dropping
- Observability: emit Kubernetes Events for step transitions, webhook receipt, trigger firings
- Debuggability: use structured logging for reconciliation decisions
