// Package version holds the build-time version metadata for all AgentCell
// binaries. The values are overridden by -ldflags at release time.
package version

var (
	// Version is the semantic version of the build, e.g. "v0.1.0".
	Version = "v0.0.0-dev"
	// Commit is the short git commit hash the build was produced from.
	Commit = "unknown"
)

// String returns "version (commit)" for --version style output.
func String() string {
	return Version + " (" + Commit + ")"
}
