package setup

import (
	"path/filepath"
	"strings"
)

// Tools whose directories go into rig_env_path: rig itself, the node it runs on, and the terminal helpers
// `rig send` may need.
var envPathTools = []string{"rig", "node", "tmux", "herdr"}

// systemDirs close every rig_env_path.
var systemDirs = []string{"/usr/bin", "/bin"}

// BuildEnvPath builds the rig_env_path value from the owner's own PATH lookups. lookPath is exec.LookPath in
// production. Tools that are not found are skipped (the caller reports which). Relative results are made
// absolute; the list is deduplicated, in order, and ends with /usr/bin:/bin.
//
// It returns the value and the names of the tools that were not found.
func BuildEnvPath(lookPath func(string) (string, error), abs func(string) (string, error)) (string, []string) {
	var dirs, missing []string
	seen := map[string]bool{}
	add := func(dir string) {
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) || strings.ContainsAny(dir, ":\x00\n") || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, tool := range envPathTools {
		found, err := lookPath(tool)
		if err != nil {
			missing = append(missing, tool)
			continue
		}
		if !filepath.IsAbs(found) {
			if found, err = abs(found); err != nil {
				missing = append(missing, tool)
				continue
			}
		}
		add(filepath.Dir(found))
	}
	for _, dir := range systemDirs {
		add(dir)
	}
	return strings.Join(dirs, ":"), missing
}
