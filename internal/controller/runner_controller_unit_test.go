package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

func TestCheckSecretHasKeys_allPresent(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"ssh-privatekey": []byte("key-content"),
			"known_hosts":    []byte("host-key"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-ssh", []string{"ssh-privatekey"}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheckSecretHasKeys_missingKey(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"username": []byte("user"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"username", "password"}); err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
}

func TestCheckSecretHasKeys_emptyKeys(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{}}
	if err := checkSecretHasKeys(secret, "git-secret", []string{}); err != nil {
		t.Fatalf("expected no error for empty key list, got: %v", err)
	}
}

func TestCheckSecretHasKeys_caseSensitive(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"TOKEN": []byte("value"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-token", []string{"token"}); err == nil {
		t.Fatal("expected error for case-sensitive key mismatch (TOKEN != token)")
	}
}

func TestCheckSecretHasKeys_multipleMissing(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"irrelevant": []byte("data"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"username", "password"}); err == nil {
		t.Fatal("expected error when multiple keys are missing")
	}
}

func TestCheckSecretHasKeys_stringData(t *testing.T) {
	secret := &corev1.Secret{
		StringData: map[string]string{
			"password": "supersecret",
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"password"}); err != nil {
		t.Fatalf("expected no error for key in StringData, got: %v", err)
	}
}

func TestBuildJob_ServiceAccountName_default(t *testing.T) {
	runner := &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: runnersv1alpha1.RunnerSpec{
			Image: "busybox:latest",
		},
	}
	var r *RunnerReconciler
	job := r.buildJob(runner, "test-job", "abc123")

	if job.Spec.Template.Spec.ServiceAccountName != "" {
		t.Errorf("expected empty ServiceAccountName (use namespace default), got %q", job.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestBuildJob_ServiceAccountName_custom(t *testing.T) {
	runner := &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: runnersv1alpha1.RunnerSpec{
			Image:              "busybox:latest",
			ServiceAccountName: "custom-sa",
		},
	}
	var r *RunnerReconciler
	job := r.buildJob(runner, "test-job", "abc123")

	if job.Spec.Template.Spec.ServiceAccountName != "custom-sa" {
		t.Errorf("expected ServiceAccountName %q, got %q", "custom-sa", job.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestBuildJob_ServiceAccountName_emptyString(t *testing.T) {
	runner := &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: runnersv1alpha1.RunnerSpec{
			Image:              "busybox:latest",
			ServiceAccountName: "",
		},
	}
	var r *RunnerReconciler
	job := r.buildJob(runner, "test-job", "abc123")

	if job.Spec.Template.Spec.ServiceAccountName != "" {
		t.Errorf("expected empty ServiceAccountName for explicit empty string, got %q", job.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestBuildJob_SecurityContext_default(t *testing.T) {
	runner := &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: runnersv1alpha1.RunnerSpec{
			Image: "busybox:latest",
		},
	}
	var r *RunnerReconciler
	job := r.buildJob(runner, "test-job", "abc123")

	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("expected non-nil SecurityContext by default")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation != false {
		t.Error("expected AllowPrivilegeEscalation=false by default")
	}
	if sc.ReadOnlyRootFilesystem != nil {
		t.Error("expected ReadOnlyRootFilesystem=nil by default (opt-in via spec.securityContext)")
	}
	if sc.Capabilities == nil || sc.Capabilities.Drop == nil {
		t.Fatal("expected Capabilities.Drop by default")
	}
	foundAll := false
	for _, c := range sc.Capabilities.Drop {
		if c == "ALL" {
			foundAll = true
			break
		}
	}
	if !foundAll {
		t.Error("expected 'ALL' in Capabilities.Drop by default")
	}
}

func TestBuildJob_SecurityContext_custom(t *testing.T) {
	readOnly := true
	sc := &corev1.SecurityContext{
		ReadOnlyRootFilesystem: &readOnly,
		RunAsUser:              ptr[int64](2000),
	}
	runner := &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: runnersv1alpha1.RunnerSpec{
			Image:           "busybox:latest",
			SecurityContext: sc,
		},
	}
	var r *RunnerReconciler
	job := r.buildJob(runner, "test-job", "abc123")

	got := job.Spec.Template.Spec.Containers[0].SecurityContext
	if got == nil {
		t.Fatal("expected non-nil SecurityContext")
	}
	if got.ReadOnlyRootFilesystem == nil || *got.ReadOnlyRootFilesystem != true {
		t.Error("expected ReadOnlyRootFilesystem=true from custom SecurityContext")
	}
	if got.RunAsUser == nil || *got.RunAsUser != 2000 {
		t.Errorf("expected RunAsUser=2000, got %v", got.RunAsUser)
	}
	// Custom SecurityContext replaces defaults entirely — AllowPrivilegeEscalation should not be set
	if got.AllowPrivilegeEscalation != nil {
		t.Error("expected AllowPrivilegeEscalation=nil when custom SecurityContext is set")
	}
}

func ptr[T any](v T) *T {
	return &v
}
