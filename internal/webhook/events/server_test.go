package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

func TestValidateHMAC(t *testing.T) {
	secret := []byte("my-secret-key")
	body := []byte(`{"ref":"refs/heads/main"}`)
	prefix := "sha256="

	sig := prefix + computeHMAC(secret, body)

	if !validateHMAC(secret, body, sig, prefix) {
		t.Error("Expected valid HMAC to pass")
	}

	if validateHMAC([]byte("wrong-secret"), body, sig, prefix) {
		t.Error("Expected wrong secret to fail")
	}

	if validateHMAC(secret, body, sig, "sha1=") {
		t.Error("Expected wrong prefix to fail")
	}

	if validateHMAC(secret, body, "malformed", prefix) {
		t.Error("Expected malformed signature to fail")
	}

	emptyBody := []byte{}
	emptySig := prefix + computeHMAC(secret, emptyBody)
	if !validateHMAC(secret, emptyBody, emptySig, prefix) {
		t.Error("Expected empty body HMAC to pass")
	}
}

func computeHMAC(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestExtractDotPath(t *testing.T) {
	data := map[string]any{
		"ref": "refs/heads/main",
		"repository": map[string]any{
			"full_name": "org/repo",
			"owner": map[string]any{
				"login": "octocat",
			},
		},
		"commits": []any{
			map[string]any{"id": "abc123"},
		},
		"numeric": 42.0,
	}

	tests := []struct {
		path     string
		expected string
	}{
		{"ref", "refs/heads/main"},
		{"repository.full_name", "org/repo"},
		{"repository.owner.login", "octocat"},
		{"numeric", "42"},
		{"nonexistent", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractDotPath(data, tt.path)
		if got != tt.expected {
			t.Errorf("extractDotPath(%q) = %q, want %q", tt.path, got, tt.expected)
		}
	}
}

func TestSanitizeValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal-branch-name", "normal-branch-name"},
		{"branch; rm -rf /", "branch rm -rf /"},
		{"$(cat /etc/passwd)", "cat /etc/passwd"},
		{"safe_value-1.0", "safe_value-1.0"},
		{"`reverse-ticks`", "reverse-ticks"},
		{"with|pipe&and$dollar", "withpipeanddollar"},
	}

	for _, tt := range tests {
		got := sanitizeValue(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeValue(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExtractParams(t *testing.T) {
	payload := map[string]any{
		"ref": "refs/heads/feature",
		"repository": map[string]any{
			"full_name": "myorg/myrepo",
		},
	}

	mappings := []runnersv1alpha1.ParameterMapping{
		{Name: "GITHUB_REF", Source: "$.ref", Sanitize: false},
		{Name: "GITHUB_REPO", Source: "$.repository.full_name", Sanitize: true},
		{Name: "MISSING_FIELD", Source: "$.nonexistent", Default: "default-val"},
		{Name: "MISSING_NO_DEFAULT", Source: "$.missing"},
	}

	params := extractParams(mappings, payload)

	if params["GITHUB_REF"] != "refs/heads/feature" {
		t.Errorf("GITHUB_REF = %q, want %q", params["GITHUB_REF"], "refs/heads/feature")
	}
	if params["GITHUB_REPO"] != "myorg/myrepo" {
		t.Errorf("GITHUB_REPO = %q, want %q", params["GITHUB_REPO"], "myorg/myrepo")
	}
	if params["MISSING_FIELD"] != "default-val" {
		t.Errorf("MISSING_FIELD = %q, want %q", params["MISSING_FIELD"], "default-val")
	}
	if _, ok := params["MISSING_NO_DEFAULT"]; ok {
		t.Error("MISSING_NO_DEFAULT should not be present")
	}
}

func TestExtractParamsSanitize(t *testing.T) {
	payload := map[string]any{
		"branch": "feature; rm -rf /",
	}

	mappings := []runnersv1alpha1.ParameterMapping{
		{Name: "BRANCH", Source: "$.branch", Sanitize: true},
		{Name: "BRANCH_RAW", Source: "$.branch", Sanitize: false},
	}

	params := extractParams(mappings, payload)

	if params["BRANCH"] != "feature rm -rf /" {
		t.Errorf("Sanitized BRANCH = %q, want %q", params["BRANCH"], "feature rm -rf /")
	}
	if params["BRANCH_RAW"] != "feature; rm -rf /" {
		t.Errorf("Raw BRANCH_RAW = %q, want %q", params["BRANCH_RAW"], "feature; rm -rf /")
	}
}

func TestRateCounterAllow(t *testing.T) {
	rc := newRateCounter()

	if !rc.allow(5) {
		t.Error("Expected first request to be allowed")
	}

	for i := range 4 {
		if !rc.allow(5) {
			t.Errorf("Expected request %d to be allowed", i+2)
		}
	}

	if rc.allow(5) {
		t.Error("Expected 6th request to be denied")
	}

	rc2 := newRateCounter()
	for range 100 {
		if !rc2.allow(0) {
			t.Error("Expected unlimited rate to always allow")
			break
		}
	}
}

func TestRateCounterExpiry(t *testing.T) {
	rc := newRateCounter()

	if !rc.allow(2) {
		t.Error("Expected first request to be allowed")
	}

	rc.mu.Lock()
	rc.counts = append(rc.counts, time.Now().Add(-2*time.Minute))
	rc.mu.Unlock()

	if !rc.allow(2) {
		t.Error("Expected request to be allowed after old entry expires")
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	raw := `{"action":"opened","pull_request":{"number":1,"title":"Test PR"}}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}

	if payload["action"] != "opened" {
		t.Errorf("action = %q, want %q", payload["action"], "opened")
	}

	pr := payload["pull_request"].(map[string]any)
	if pr["number"] != float64(1) {
		t.Errorf("pr.number = %v, want %v", pr["number"], 1)
	}
	if pr["title"] != "Test PR" {
		t.Errorf("pr.title = %q, want %q", pr["title"], "Test PR")
	}
}

func TestCreateWorkflowSetsOwnerReference(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := runnersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	trigger := runnersv1alpha1.EventTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-trigger",
			Namespace: "test-ns",
			UID:       "abc-123-def",
		},
		Spec: runnersv1alpha1.EventTriggerSpec{
			WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
				Name:      "test-template",
				Namespace: "test-ns",
			},
		},
	}

	template := &runnersv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-template",
			Namespace: "test-ns",
		},
		Spec: runnersv1alpha1.WorkflowSpec{
			Steps: []runnersv1alpha1.WorkflowStep{
				{Name: "step-1", Image: "busybox", Command: []string{"echo", "hello"}},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(template).
		Build()

	srv := NewServer(cl, scheme, ":0")

	params := map[string]string{"FOO": "bar"}
	if err := srv.createWorkflow(t.Context(), trigger, params); err != nil {
		t.Fatalf("createWorkflow failed: %v", err)
	}

	created := &runnersv1alpha1.WorkflowList{}
	if err := cl.List(t.Context(), created, client.InNamespace("test-ns")); err != nil {
		t.Fatal(err)
	}

	// Filter out the template (we want the dynamically created one)
	var wf *runnersv1alpha1.Workflow
	for i := range created.Items {
		if created.Items[i].GenerateName != "" {
			wf = &created.Items[i]
			break
		}
	}
	if wf == nil {
		t.Fatal("expected a generated workflow, found none")
	}
	if len(wf.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(wf.OwnerReferences))
	}

	ref := wf.OwnerReferences[0]
	if ref.Name != "test-trigger" {
		t.Errorf("owner ref name = %q, want %q", ref.Name, "test-trigger")
	}
	if ref.Kind != "EventTrigger" {
		t.Errorf("owner ref kind = %q, want %q", ref.Kind, "EventTrigger")
	}
	if !ptrToTrue(ref.Controller) {
		t.Error("expected owner ref to be controller")
	}
}

func ptrToTrue(b *bool) bool {
	return b != nil && *b
}
