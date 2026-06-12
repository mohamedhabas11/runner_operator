## operational requirments

Must:
- Run any docker image
- Load ENV VARS in runtime
- Load k8s Basic type Secrets
- Load Extra Mounts
- Track Drift in resources

Must Not:
- Leave resources it owns behind
- Leave jobs stranded, Known long jobs must define a timeout_after: "go_formatted_time"

Shall:
- Support Workflows in the form of yaml, effectivly mimicing github/gitlab workflows
- Support retry logic, and backoff
- Support chaining of Workflows with logic gates

Shall Not:
- Be complicated, relay on Libraries and use their interfaces to keep logic lean, we live with the bloated dependancies
- Contain hacky LLM readable code, Aim for a human readable pattern.
