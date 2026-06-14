package gitops

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

func TestNewAuthStrategy_nilAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"}
	s := NewAuthStrategy(repo)
	if _, ok := s.(*noAuthStrategy); !ok {
		t.Fatalf("expected noAuthStrategy, got %T", s)
	}
}

func TestNewAuthStrategy_sshURL(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "git@github.com:org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	if _, ok := s.(*sshAuthStrategy); !ok {
		t.Fatalf("expected sshAuthStrategy, got %T", s)
	}
}

func TestNewAuthStrategy_sshURL_scheme(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "ssh://git@github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	if _, ok := s.(*sshAuthStrategy); !ok {
		t.Fatalf("expected sshAuthStrategy, got %T", s)
	}
}

func TestNewAuthStrategy_https_token(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	httpS, ok := s.(*httpAuthStrategy)
	if !ok {
		t.Fatalf("expected httpAuthStrategy, got %T", s)
	}
	if httpS.authType != v1alpha1.GitAuthTypeToken {
		t.Fatalf("expected token auth type, got %s", httpS.authType)
	}
}

func TestNewAuthStrategy_https_basicAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://gitlab.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			Type:      v1alpha1.GitAuthTypeBasicAuth,
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	httpS, ok := s.(*httpAuthStrategy)
	if !ok {
		t.Fatalf("expected httpAuthStrategy, got %T", s)
	}
	if httpS.authType != v1alpha1.GitAuthTypeBasicAuth {
		t.Fatalf("expected basicAuth auth type, got %s", httpS.authType)
	}
}

func TestNewAuthStrategy_explicitSSH(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			Type:      v1alpha1.GitAuthTypeSSH,
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	if _, ok := s.(*sshAuthStrategy); !ok {
		t.Fatalf("expected sshAuthStrategy, got %T", s)
	}
}

func TestBuildInitContainer_defaultImage(t *testing.T) {
	repo := &v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"}
	s := NewAuthStrategy(repo)
	c := BuildInitContainer(repo, s)

	if c.Name != "git-clone" {
		t.Fatalf("expected container name 'git-clone', got %q", c.Name)
	}
	if c.Image != DefaultGitImage {
		t.Fatalf("expected image %q, got %q", DefaultGitImage, c.Image)
	}
	if c.SecurityContext == nil {
		t.Fatal("expected non-nil SecurityContext")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Fatal("expected RunAsNonRoot=true")
	}
}

func TestBuildInitContainer_customImage(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL:   "https://github.com/org/repo.git",
		Image: "my-custom-git:latest",
	}
	s := NewAuthStrategy(repo)
	c := BuildInitContainer(repo, s)
	if c.Image != "my-custom-git:latest" {
		t.Fatalf("expected custom image, got %q", c.Image)
	}
}

func TestBuildInitContainer_publicRepo(t *testing.T) {
	repo := &v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"}
	s := NewAuthStrategy(repo)
	c := BuildInitContainer(repo, s)

	if len(c.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount (workspace only), got %d", len(c.VolumeMounts))
	}
	if c.VolumeMounts[0].Name != WorkspaceVolumeName {
		t.Fatalf("expected mount name %q, got %q", WorkspaceVolumeName, c.VolumeMounts[0].Name)
	}
}

func TestBuildInitContainer_sshAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "git@github.com:org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "ssh-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	c := BuildInitContainer(repo, s)

	expectedMounts := map[string]bool{
		WorkspaceVolumeName: false,
		SecretVolumeName:    false,
		SSHTmpVolumeName:    false,
	}
	for _, m := range c.VolumeMounts {
		if _, ok := expectedMounts[m.Name]; ok {
			expectedMounts[m.Name] = true
		}
	}
	for name, found := range expectedMounts {
		if !found {
			t.Fatalf("expected volume mount %q not found", name)
		}
	}
}

func TestBuildInitContainer_tokenAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			Type:      v1alpha1.GitAuthTypeToken,
			SecretRef: corev1.LocalObjectReference{Name: "token-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	c := BuildInitContainer(repo, s)

	expectedMounts := map[string]bool{
		WorkspaceVolumeName:    false,
		SecretVolumeName:       false,
		GitConfigTmpVolumeName: false,
	}
	for _, m := range c.VolumeMounts {
		if _, ok := expectedMounts[m.Name]; ok {
			expectedMounts[m.Name] = true
		}
	}
	for name, found := range expectedMounts {
		if !found {
			t.Fatalf("expected volume mount %q not found", name)
		}
	}
}

func TestBuildVolumes_publicRepo(t *testing.T) {
	repo := &v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"}
	s := NewAuthStrategy(repo)
	vols := BuildVolumes(repo, s)

	if len(vols) != 1 {
		t.Fatalf("expected 1 volume (workspace), got %d", len(vols))
	}
	if vols[0].Name != WorkspaceVolumeName {
		t.Fatalf("expected volume %q, got %q", WorkspaceVolumeName, vols[0].Name)
	}
}

func TestBuildVolumes_sshAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "git@github.com:org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "ssh-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	vols := BuildVolumes(repo, s)

	expectedVols := map[string]bool{
		WorkspaceVolumeName: false,
		SecretVolumeName:    false,
		SSHTmpVolumeName:    false,
	}
	for _, v := range vols {
		if _, ok := expectedVols[v.Name]; ok {
			expectedVols[v.Name] = true
		}
	}
	for name, found := range expectedVols {
		if !found {
			t.Fatalf("expected volume %q not found", name)
		}
	}
}

func TestBuildVolumes_tokenAuth(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			Type:      v1alpha1.GitAuthTypeToken,
			SecretRef: corev1.LocalObjectReference{Name: "token-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	vols := BuildVolumes(repo, s)

	expectedVols := map[string]bool{
		WorkspaceVolumeName:    false,
		SecretVolumeName:       false,
		GitConfigTmpVolumeName: false,
	}
	for _, v := range vols {
		if _, ok := expectedVols[v.Name]; ok {
			expectedVols[v.Name] = true
		}
	}
	for name, found := range expectedVols {
		if !found {
			t.Fatalf("expected volume %q not found", name)
		}
	}
}

func TestBuildVolumes_secretRefNamespace(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	s := NewAuthStrategy(repo)
	vols := BuildVolumes(repo, s)

	var foundSecret bool
	for _, v := range vols {
		if v.Name == SecretVolumeName {
			foundSecret = true
			if v.Secret == nil {
				t.Fatal("expected Secret volume source")
			}
			if v.Secret.SecretName != "my-secret" {
				t.Fatalf("expected secret name 'my-secret', got %q", v.Secret.SecretName)
			}
		}
	}
	if !foundSecret {
		t.Fatal("expected SecretVolumeName volume")
	}
}

func TestBuildCloneScript_publicRepo(t *testing.T) {
	repo := &v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"}
	s := NewAuthStrategy(repo)
	script := BuildCloneScript(repo, s)

	if !strings.Contains(script, "set -euo pipefail") {
		t.Fatal("expected set -euo pipefail")
	}
	if !strings.Contains(script, "git clone --depth 1") {
		t.Fatal("expected git clone command")
	}
	if !strings.Contains(script, "https://github.com/org/repo.git") {
		t.Fatal("expected repo URL in clone command")
	}
	if strings.Contains(script, "rm -rf") {
		t.Fatal("expected no cleanup for public repo")
	}
}

func TestBuildCloneScript_revision(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL:      "https://github.com/org/repo.git",
		Revision: "v1.0.0",
	}
	s := NewAuthStrategy(repo)
	script := BuildCloneScript(repo, s)

	if !strings.Contains(script, "git -C /workspace/repo fetch origin") {
		t.Fatal("expected fetch command for revision")
	}
	if !strings.Contains(script, "git -C /workspace/repo checkout 'v1.0.0'") {
		t.Fatal("expected checkout command for revision")
	}
}

func TestBuildCloneScript_path(t *testing.T) {
	repo := &v1alpha1.GitRepo{
		URL:  "https://github.com/org/repo.git",
		Path: "terraform/prod",
	}
	s := NewAuthStrategy(repo)
	script := BuildCloneScript(repo, s)

	if !strings.Contains(script, "terraform/prod") {
		t.Fatal("expected path validation in script")
	}
	if !strings.Contains(script, "exit 1") {
		t.Fatal("expected exit on missing path")
	}
}

func TestBuildCloneScript_shellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"it's", "'it'\\''s'"},
		{"with spaces", "'with spaces'"},
	}
	for _, tc := range tests {
		got := shellQuote(tc.input)
		if got != tc.want {
			t.Fatalf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSSHAuthStrategy_SetupScript(t *testing.T) {
	s := &sshAuthStrategy{url: "git@github.com:org/repo.git"}
	script := s.SetupScript()

	if !strings.Contains(script, "ssh-keyscan 'github.com'") {
		t.Fatal("expected ssh-keyscan for github.com")
	}
	if !strings.Contains(script, "ssh-privatekey") {
		t.Fatal("expected ssh-privatekey reference")
	}
	if !strings.Contains(script, "GIT_SSH_COMMAND") {
		t.Fatal("expected GIT_SSH_COMMAND export")
	}
}

func TestSSHAuthStrategy_CleanupScript(t *testing.T) {
	s := &sshAuthStrategy{}
	script := s.CleanupScript()
	if !strings.Contains(script, "rm -rf") {
		t.Fatal("expected cleanup script")
	}
}

func TestHTTPAuthStrategy_token(t *testing.T) {
	s := &httpAuthStrategy{authType: v1alpha1.GitAuthTypeToken}
	setup := s.SetupScript()

	if !strings.Contains(setup, ".git-credentials") {
		t.Fatal("expected .git-credentials in setup")
	}
	if !strings.Contains(setup, "$(cat /etc/git-auth/token)") {
		t.Fatal("expected token substitution")
	}
	if strings.Contains(setup, "username") {
		t.Fatal("expected no username for token auth")
	}

	cleanup := s.CleanupScript()
	if !strings.Contains(cleanup, "rm -f") {
		t.Fatal("expected cleanup")
	}
}

func TestHTTPAuthStrategy_basicAuth(t *testing.T) {
	s := &httpAuthStrategy{authType: v1alpha1.GitAuthTypeBasicAuth}
	setup := s.SetupScript()

	if !strings.Contains(setup, "$(cat /etc/git-auth/username)") {
		t.Fatal("expected username substitution")
	}
	if !strings.Contains(setup, "$(cat /etc/git-auth/password)") {
		t.Fatal("expected password substitution")
	}
}

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"git@github.com:org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"https://github.com/org/repo.git", false},
		{"http://gitlab.com/org/repo.git", false},
		{"", false},
	}
	for _, tc := range tests {
		got := isSSHURL(tc.url)
		if got != tc.want {
			t.Fatalf("isSSHURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestExtractSSHHost(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git@github.com:org/repo.git", "github.com"},
		{"ssh://git@gitlab.com/org/repo.git", "gitlab.com"},
		{"ssh://git@bitbucket.org:22/org/repo.git", "bitbucket.org"},
		{"", "github.com"},
	}
	for _, tc := range tests {
		got := extractSSHHost(tc.url)
		if got != tc.want {
			t.Fatalf("extractSSHHost(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestRequiredSecretKeys_nilAuth(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{URL: "https://github.com/org/repo.git"})
	if keys != nil {
		t.Fatalf("expected nil for no auth, got %v", keys)
	}
}

func TestRequiredSecretKeys_ssh(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{
		URL: "git@github.com:org/repo.git",
		Auth: &v1alpha1.GitAuth{Type: v1alpha1.GitAuthTypeSSH,
			SecretRef: corev1.LocalObjectReference{Name: "git-ssh"}},
	})
	if len(keys) != 1 || keys[0] != "ssh-privatekey" {
		t.Fatalf("expected [ssh-privatekey], got %v", keys)
	}
}

func TestRequiredSecretKeys_autoDetect_ssh(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{
		URL: "git@github.com:org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "git-ssh"},
		},
	})
	if len(keys) != 1 || keys[0] != "ssh-privatekey" {
		t.Fatalf("expected [ssh-privatekey] via auto-detect, got %v", keys)
	}
}

func TestRequiredSecretKeys_basicAuth(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{Type: v1alpha1.GitAuthTypeBasicAuth,
			SecretRef: corev1.LocalObjectReference{Name: "git-basic"}},
	})
	if len(keys) != 2 || keys[0] != "username" || keys[1] != "password" {
		t.Fatalf("expected [username password], got %v", keys)
	}
}

func TestRequiredSecretKeys_token(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{Type: v1alpha1.GitAuthTypeToken,
			SecretRef: corev1.LocalObjectReference{Name: "git-token"}},
	})
	if len(keys) != 1 || keys[0] != "token" {
		t.Fatalf("expected [token], got %v", keys)
	}
}

func TestRequiredSecretKeys_autoDetect_https(t *testing.T) {
	keys := RequiredSecretKeys(&v1alpha1.GitRepo{
		URL: "https://github.com/org/repo.git",
		Auth: &v1alpha1.GitAuth{
			SecretRef: corev1.LocalObjectReference{Name: "git-token"},
		},
	})
	if len(keys) != 1 || keys[0] != "token" {
		t.Fatalf("expected [token] via auto-detect, got %v", keys)
	}
}
