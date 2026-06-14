package gitops

import (
	"strings"

	v1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

// BuildCloneScript builds the complete shell script for the git-clone init
// container. It composes: auth setup → git clone → checkout revision →
// validate path → auth cleanup.
func BuildCloneScript(gitRepo *v1alpha1.GitRepo, strategy AuthStrategy) string {
	var b strings.Builder

	// Safety first: fail on any error, undefined variable, or pipe failure
	b.WriteString("set -euo pipefail\n")

	// 1. Auth setup (strategy-specific, may be empty for public repos)
	if setup := strategy.SetupScript(); setup != "" {
		b.WriteString(setup)
	}

	// 2. Clone the repository (shallow for speed)
	cloneURL := shellQuote(gitRepo.URL)
	cloneDest := WorkspaceMountPath + "/" + RepoSubPath
	b.WriteString("git clone --depth 1 -- " + cloneURL + " " + cloneDest + "\n")

	// 3. Checkout a specific revision if requested
	if gitRepo.Revision != "" {
		rev := shellQuote(gitRepo.Revision)
		b.WriteString("git -C " + cloneDest + " fetch origin -- " + rev + "\n")
		b.WriteString("git -C " + cloneDest + " checkout " + rev + "\n")
	}

	// 4. Validate path exists if specified
	if gitRepo.Path != "" {
		fullPath := cloneDest + "/" + gitRepo.Path
		b.WriteString("if [ ! -d " + shellQuote(fullPath) + " ]; then\n")
		b.WriteString("  echo \"ERROR: path " + shellQuote(gitRepo.Path) + " not found in repository\" >&2\n")
		b.WriteString("  exit 1\n")
		b.WriteString("fi\n")
	}

	// 5. Auth cleanup (remove credentials from tmpfs)
	if cleanup := strategy.CleanupScript(); cleanup != "" {
		b.WriteString(cleanup)
	}

	return b.String()
}

// shellQuote wraps a string in single quotes, escaping any embedded single
// quotes. This prevents shell injection in user-provided values like URLs,
// revisions, and paths.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
