package gitops

import (
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

// ──────────────────────────────────────────────────────────────────────────────
// noAuthStrategy — public repositories (no credentials needed)
// ──────────────────────────────────────────────────────────────────────────────

type noAuthStrategy struct{}

func (s *noAuthStrategy) SetupScript() string                { return "" }
func (s *noAuthStrategy) CleanupScript() string              { return "" }
func (s *noAuthStrategy) VolumeMounts() []corev1.VolumeMount { return nil }

// ──────────────────────────────────────────────────────────────────────────────
// sshAuthStrategy — SSH key-based authentication
// ──────────────────────────────────────────────────────────────────────────────

// sshAuthStrategy handles SSH key auth. It copies the private key from the
// mounted secret to a writable tmpfs, configures known_hosts, and cleans up
// after cloning.
type sshAuthStrategy struct {
	url string // the git clone URL, used to extract the SSH host
}

// pinnedKnownHosts is the default known_hosts content for well-known SCM hosts.
// Used when the user does not provide a custom known_hosts via the secret.
// These keys were verified as of 2024-03-24.
const pinnedKnownHosts = `# github.com
github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C5skZZOJwFG3PN5z
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
# gitlab.com
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
# bitbucket.org
bitbucket.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIazEu89wgQZ4bqs3d1siqIEEVqyHUVbDp8/om5OItu5
`

func (s *sshAuthStrategy) SetupScript() string {
	// Copy the SSH key to writable tmpfs, set permissions, configure known_hosts.
	// If the secret includes a "known_hosts" file, use that; otherwise, use
	// pinned keys for well-known SCM hosts (github, gitlab, bitbucket).
	return `mkdir -p ` + SSHTmpMountPath + `/.ssh
cp ` + SecretMountPath + `/ssh-privatekey ` + SSHTmpMountPath + `/.ssh/id_rsa
chmod 600 ` + SSHTmpMountPath + `/.ssh/id_rsa
if [ -f ` + SecretMountPath + `/known_hosts ]; then
  cp ` + SecretMountPath + `/known_hosts ` + SSHTmpMountPath + `/.ssh/known_hosts
else
  cat > ` + SSHTmpMountPath + `/.ssh/known_hosts << 'EOF'
` + pinnedKnownHosts + `EOF
fi
export GIT_SSH_COMMAND="ssh -i ` + SSHTmpMountPath + `/.ssh/id_rsa -o UserKnownHostsFile=` + SSHTmpMountPath + `/.ssh/known_hosts -o StrictHostKeyChecking=yes"
`
}

func (s *sshAuthStrategy) CleanupScript() string {
	return `rm -rf ` + SSHTmpMountPath + `/.ssh
`
}

func (s *sshAuthStrategy) VolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      SecretVolumeName,
			MountPath: SecretMountPath,
			ReadOnly:  true,
		},
		{
			Name:      SSHTmpVolumeName,
			MountPath: SSHTmpMountPath,
		},
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// httpAuthStrategy — HTTPS token or basic-auth authentication
// ──────────────────────────────────────────────────────────────────────────────

// httpAuthStrategy handles both "token" and "basicAuth" HTTPS auth.
// It writes a .gitconfig and .git-credentials file to a writable tmpfs.
type httpAuthStrategy struct {
	authType v1alpha1.GitAuthType
}

func (s *httpAuthStrategy) SetupScript() string {
	configDir := GitConfigTmpMountPath

	// Build the credential helper config. For "token" auth, the username is
	// left as an empty string (many hosts like GitHub accept any username
	// when a PAT is used as the password). For "basicAuth", both fields
	// come from the secret.
	var userExpr, passExpr string
	if s.authType == v1alpha1.GitAuthTypeToken {
		userExpr = `""`
		passExpr = `$(cat ` + SecretMountPath + `/token)`
	} else {
		userExpr = `$(cat ` + SecretMountPath + `/username)`
		passExpr = `$(cat ` + SecretMountPath + `/password)`
	}

	return `git config --global credential.helper 'store --file=` + configDir + `/.git-credentials'
printf 'https://%s:%s@github.com\n' ` + userExpr + ` ` + passExpr + ` > ` + configDir + `/.git-credentials
chmod 600 ` + configDir + `/.git-credentials
export HOME=` + configDir + `
`
}

func (s *httpAuthStrategy) CleanupScript() string {
	return `rm -f ` + GitConfigTmpMountPath + `/.git-credentials ` + GitConfigTmpMountPath + `/.gitconfig
`
}

func (s *httpAuthStrategy) VolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      SecretVolumeName,
			MountPath: SecretMountPath,
			ReadOnly:  true,
		},
		{
			Name:      GitConfigTmpVolumeName,
			MountPath: GitConfigTmpMountPath,
		},
	}
}

// RequiredSecretKeys returns the mandatory Secret data keys for the given
// GitRepo auth configuration. Used by the Runner controller for pre-flight
// validation before creating a Job. Returns nil if auth is nil or type is
// auto-detected (the caller should validate after NewAuthStrategy resolves it).
func RequiredSecretKeys(gitRepo *v1alpha1.GitRepo) []string {
	if gitRepo.Auth == nil {
		return nil
	}
	authType := gitRepo.Auth.Type
	if authType == "" {
		// Auto-detect: SSH URLs need ssh-privatekey, HTTPS needs token.
		if isSSHURL(gitRepo.URL) {
			authType = v1alpha1.GitAuthTypeSSH
		} else {
			authType = v1alpha1.GitAuthTypeToken
		}
	}
	switch authType {
	case v1alpha1.GitAuthTypeSSH:
		return []string{"ssh-privatekey"}
	case v1alpha1.GitAuthTypeBasicAuth:
		return []string{"username", "password"}
	case v1alpha1.GitAuthTypeToken:
		return []string{"token"}
	default:
		return nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// extractSSHHost pulls the hostname from an SSH URL.
// Supports both "git@github.com:org/repo" and "ssh://git@github.com/org/repo".
func extractSSHHost(rawURL string) string {
	// ssh://user@host/path
	if strings.HasPrefix(rawURL, "ssh://") {
		parsed, err := url.Parse(rawURL)
		if err == nil && parsed.Hostname() != "" {
			return parsed.Hostname()
		}
	}

	// git@host:path
	if _, after, ok := strings.Cut(rawURL, "@"); ok {
		rest := after
		if before, _, ok := strings.Cut(rest, ":"); ok {
			return before
		}
	}

	return "github.com" // safe fallback
}
