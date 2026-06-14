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

func (s *sshAuthStrategy) SetupScript() string {
	host := extractSSHHost(s.url)

	// Copy the SSH key to writable tmpfs, set permissions, configure known_hosts.
	// If the secret includes a "known_hosts" file, use that; otherwise, fall
	// back to ssh-keyscan against the actual repo host.
	return `mkdir -p ` + SSHTmpMountPath + `/.ssh
cp ` + SecretMountPath + `/ssh-privatekey ` + SSHTmpMountPath + `/.ssh/id_rsa
chmod 600 ` + SSHTmpMountPath + `/.ssh/id_rsa
if [ -f ` + SecretMountPath + `/known_hosts ]; then
  cp ` + SecretMountPath + `/known_hosts ` + SSHTmpMountPath + `/.ssh/known_hosts
else
  ssh-keyscan ` + shellQuote(host) + ` >> ` + SSHTmpMountPath + `/.ssh/known_hosts 2>/dev/null || true
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
