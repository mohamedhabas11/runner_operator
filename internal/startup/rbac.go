package startup

import (
	"context"
	"slices"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type permission struct {
	group    string
	resource string
	verb     string
}

var expectedPerms = []permission{
	{"", "events", "create"},
	{"", "events", "patch"},
	{"", "namespaces", "get"},
	{"", "namespaces", "list"},
	{"", "namespaces", "watch"},
	{"", "secrets", "get"},
	{"", "secrets", "list"},
	{"", "secrets", "watch"},
	{"", "pods", "delete"},
	{"", "pods", "get"},
	{"", "pods", "list"},
	{"", "pods", "watch"},
	{"", "pods/log", "get"},
	{"batch", "jobs", "create"},
	{"batch", "jobs", "delete"},
	{"batch", "jobs", "get"},
	{"batch", "jobs", "list"},
	{"batch", "jobs", "patch"},
	{"batch", "jobs", "update"},
	{"batch", "jobs", "watch"},
	{"batch", "jobs/status", "get"},
	{"runners.runner-operator.io", "eventtriggers", "create"},
	{"runners.runner-operator.io", "eventtriggers", "delete"},
	{"runners.runner-operator.io", "eventtriggers", "get"},
	{"runners.runner-operator.io", "eventtriggers", "list"},
	{"runners.runner-operator.io", "eventtriggers", "patch"},
	{"runners.runner-operator.io", "eventtriggers", "update"},
	{"runners.runner-operator.io", "eventtriggers", "watch"},
	{"runners.runner-operator.io", "eventtriggers/status", "get"},
	{"runners.runner-operator.io", "eventtriggers/status", "patch"},
	{"runners.runner-operator.io", "eventtriggers/status", "update"},
	{"runners.runner-operator.io", "runners", "create"},
	{"runners.runner-operator.io", "runners", "delete"},
	{"runners.runner-operator.io", "runners", "get"},
	{"runners.runner-operator.io", "runners", "list"},
	{"runners.runner-operator.io", "runners", "patch"},
	{"runners.runner-operator.io", "runners", "update"},
	{"runners.runner-operator.io", "runners", "watch"},
	{"runners.runner-operator.io", "runners/status", "get"},
	{"runners.runner-operator.io", "runners/status", "patch"},
	{"runners.runner-operator.io", "runners/status", "update"},
	{"runners.runner-operator.io", "workflows", "create"},
	{"runners.runner-operator.io", "workflows", "delete"},
	{"runners.runner-operator.io", "workflows", "get"},
	{"runners.runner-operator.io", "workflows", "list"},
	{"runners.runner-operator.io", "workflows", "patch"},
	{"runners.runner-operator.io", "workflows", "update"},
	{"runners.runner-operator.io", "workflows", "watch"},
	{"runners.runner-operator.io", "workflows/status", "get"},
	{"runners.runner-operator.io", "workflows/status", "patch"},
	{"runners.runner-operator.io", "workflows/status", "update"},
}

// rbacKey returns a stable key for tracking permission changes.
func rbacKey(p permission) string {
	return p.group + "/" + p.resource + ":" + p.verb
}

// ruleMatches checks whether a ResourceRule covers the given permission.
// A rule covers a permission if the rule's verb set includes p.verb AND
// (p.group is in rule.APIGroups or "*") AND (p.resource is in rule.Resources or "*").
func ruleMatches(rule authorizationv1.ResourceRule, p permission) bool {
	if !hasVerb(rule.Verbs, p.verb) {
		return false
	}
	if !hasGroup(rule.APIGroups, p.group) {
		return false
	}
	if !hasResource(rule.Resources, p.resource) {
		return false
	}
	return true
}

func hasVerb(verbs []string, verb string) bool {
	return slices.Contains(verbs, verb) || slices.Contains(verbs, "*")
}

func hasGroup(groups []string, group string) bool {
	return slices.Contains(groups, group) || slices.Contains(groups, "*")
}

func hasResource(resources []string, resource string) bool {
	return slices.Contains(resources, resource) || slices.Contains(resources, "*")
}

// CheckRBAC checks all expected permissions using SelfSubjectRulesReview
// and returns the set of missing permission keys.
func CheckRBAC(ctx context.Context, cl client.Client) (missingPerms []permission, _ error) {
	logger := log.FromContext(ctx)

	ssrr := &authorizationv1.SelfSubjectRulesReview{
		Spec: authorizationv1.SelfSubjectRulesReviewSpec{},
	}
	if err := cl.Create(ctx, ssrr); err != nil {
		return nil, err
	}

	// Build a coverage map from the returned rules.
	covered := sets.New[string]()
	for _, rule := range ssrr.Status.ResourceRules {
		for _, p := range expectedPerms {
			if ruleMatches(rule, p) {
				covered.Insert(rbacKey(p))
			}
		}
	}

	for _, p := range expectedPerms {
		if !covered.Has(rbacKey(p)) {
			missingPerms = append(missingPerms, p)
			logger.WithValues("resource", p.resource, "verb", p.verb, "group", p.group).
				Info("Missing RBAC permission - may cause runtime errors")
		}
	}
	return missingPerms, nil
}

// RbacCheckRunnable periodically re-checks RBAC permissions and logs changes.
type RbacCheckRunnable struct {
	Client    client.Client
	Interval  time.Duration
	lastState map[string]bool
}

func (r *RbacCheckRunnable) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("Starting periodic RBAC check", "interval", r.Interval.String())

	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			missing, err := CheckRBAC(ctx, r.Client)
			if err != nil {
				logger.Error(err, "Periodic RBAC check failed")
				continue
			}

			currentState := make(map[string]bool, len(expectedPerms))
			for _, p := range expectedPerms {
				currentState[rbacKey(p)] = true
			}
			for _, p := range missing {
				currentState[rbacKey(p)] = false
			}

			if r.lastState != nil {
				for _, p := range expectedPerms {
					k := rbacKey(p)
					was := r.lastState[k]
					is := currentState[k]
					if was && !is {
						logger.WithValues("resource", p.resource, "verb", p.verb, "group", p.group).
							Info("RBAC permission was removed - operator restart may be required")
					} else if !was && is {
						logger.WithValues("resource", p.resource, "verb", p.verb, "group", p.group).
							Info("RBAC permission was added - operator restart may be required to use new features")
					}
				}
			}
			r.lastState = currentState
		}
	}
}
