// Package gitops builds Kubernetes init containers and volumes for cloning
// Git repositories. It uses the strategy pattern (via AuthStrategy) so each
// authentication method is isolated and testable.
package gitops

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// DefaultGitImage is the pinned alpine/git image used for cloning.
	DefaultGitImage = "alpine/git:2.47.2"

	// WorkspaceVolumeName is the emptyDir volume shared between the
	// git-clone init container and the runner container.
	WorkspaceVolumeName = "runner-operator-git-repo"

	// WorkspaceMountPath is where the workspace volume is mounted.
	WorkspaceMountPath = "/workspace"

	// RepoSubPath is the subdirectory inside the workspace where the repo
	// is cloned to.
	RepoSubPath = "repo"

	// SecretVolumeName is the projected secret volume name.
	SecretVolumeName = "runner-operator-git-auth"

	// SecretMountPath is where the secret is mounted (read-only).
	SecretMountPath = "/etc/git-auth"

	// SSHTmpVolumeName is the tmpfs volume for SSH credential writes.
	SSHTmpVolumeName = "runner-operator-ssh-tmp"

	// SSHTmpMountPath is where the SSH tmpfs is mounted.
	SSHTmpMountPath = "/tmp/ssh"

	// GitConfigTmpVolumeName is the tmpfs volume for git-config writes.
	GitConfigTmpVolumeName = "runner-operator-git-config-tmp"

	// GitConfigTmpMountPath is where the git-config tmpfs is mounted.
	GitConfigTmpMountPath = "/tmp/git-config"
)

// ──────────────────────────────────────────────────────────────────────────────
// AuthStrategy interface (factory product)
// ──────────────────────────────────────────────────────────────────────────────

// AuthStrategy handles git credential setup for a specific auth method.
// Each strategy knows how to:
//   - Generate shell commands to configure credentials before git clone
//   - Generate shell commands to clean up credentials after git clone
//   - Add any required volume mounts to the init container
type AuthStrategy interface {
	// SetupScript returns shell commands to run BEFORE git clone.
	SetupScript() string
	// CleanupScript returns shell commands to run AFTER git clone.
	CleanupScript() string
	// VolumeMounts returns extra mounts needed by this strategy.
	VolumeMounts() []corev1.VolumeMount
}

// ──────────────────────────────────────────────────────────────────────────────
// Factory function
// ──────────────────────────────────────────────────────────────────────────────

// NewAuthStrategy returns the right AuthStrategy based on GitAuth config.
// If auth is nil, returns noAuth (public repo).
// If auth.Type is set, uses that directly.
// If auth.Type is empty, auto-detects: SSH URLs → ssh, HTTPS URLs → token.
func NewAuthStrategy(gitRepo *v1alpha1.GitRepo) AuthStrategy {
	if gitRepo.Auth == nil {
		return &noAuthStrategy{}
	}

	authType := gitRepo.Auth.Type

	// Auto-detect when type is not explicitly set
	if authType == "" {
		if isSSHURL(gitRepo.URL) {
			authType = v1alpha1.GitAuthTypeSSH
		} else {
			// Default to token for HTTPS URLs
			authType = v1alpha1.GitAuthTypeToken
		}
	}

	switch authType {
	case v1alpha1.GitAuthTypeSSH:
		return &sshAuthStrategy{url: gitRepo.URL}
	case v1alpha1.GitAuthTypeBasicAuth:
		return &httpAuthStrategy{authType: v1alpha1.GitAuthTypeBasicAuth}
	case v1alpha1.GitAuthTypeToken:
		return &httpAuthStrategy{authType: v1alpha1.GitAuthTypeToken}
	default:
		return &noAuthStrategy{}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Public builders
// ──────────────────────────────────────────────────────────────────────────────

// BuildInitContainer creates the git-clone init container. Delegates auth-specific
// mounts to the strategy (secret volumes, tmpfs for SSH keys / git-config).
func BuildInitContainer(gitRepo *v1alpha1.GitRepo, strategy AuthStrategy) corev1.Container {
	image := DefaultGitImage
	if gitRepo.Image != "" {
		image = gitRepo.Image
	}

	script := BuildCloneScript(gitRepo, strategy)

	// Start with the workspace volume mount (always needed)
	mounts := make([]corev1.VolumeMount, 0, 1+len(strategy.VolumeMounts()))
	mounts = append(mounts, corev1.VolumeMount{Name: WorkspaceVolumeName, MountPath: WorkspaceMountPath})
	// Add any strategy-specific mounts (secret, tmpfs, etc.)
	mounts = append(mounts, strategy.VolumeMounts()...)

	return corev1.Container{
		Name:         "git-clone",
		Image:        image,
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{script},
		VolumeMounts: mounts,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(int64(1000)),
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
}

// BuildVolumes returns the Kubernetes volumes needed for git cloning.
// This always includes the workspace emptyDir. For authenticated repos
// it also includes the secret volume (and tmpfs volumes for writable areas).
func BuildVolumes(gitRepo *v1alpha1.GitRepo, strategy AuthStrategy) []corev1.Volume {
	// Workspace volume is always needed
	volumes := []corev1.Volume{
		{
			Name: WorkspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	if gitRepo.Auth == nil {
		return volumes
	}

	// Secret volume with restrictive permissions for SSH keys
	secretMode := int32(0400)
	volumes = append(volumes, corev1.Volume{
		Name: SecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  gitRepo.Auth.SecretRef.Name,
				DefaultMode: &secretMode,
			},
		},
	})

	// Add tmpfs volumes based on strategy type
	for _, mount := range strategy.VolumeMounts() {
		switch mount.Name {
		case SSHTmpVolumeName:
			volumes = append(volumes, corev1.Volume{
				Name: SSHTmpVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumMemory,
					},
				},
			})
		case GitConfigTmpVolumeName:
			volumes = append(volumes, corev1.Volume{
				Name: GitConfigTmpVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumMemory,
					},
				},
			})
		}
	}

	return volumes
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// isSSHURL detects whether a git URL uses SSH protocol.
// Matches: git@host:path, ssh://user@host/path
func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	// git@github.com:org/repo.git style
	if strings.Contains(url, "@") && strings.Contains(url, ":") && !strings.Contains(url, "://") {
		return true
	}
	return false
}
