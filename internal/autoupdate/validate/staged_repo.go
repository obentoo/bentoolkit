package validate

import (
	"fmt"
	"path/filepath"
	"strings"
)

// StagedRepoSection returns the name Stage gave the staged tree of atom at
// version, and the repos.conf section that registers it at stagedRoot. A build
// environment that does not share this host's repository configuration
// registers the tree with it, under the same name the host would use.
func StagedRepoSection(stagedRoot, atom, version string) (name, section string, err error) {
	_, suffixedPkg, err := splitStagedAtom(atom)
	if err != nil {
		return "", "", err
	}
	location, err := filepath.Abs(stagedRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolving the staged tree path %s: %w", stagedRoot, err)
	}
	name = stagedRepoNameAt(stagedRoot, suffixedPkg, version)
	mastersKey, mastersValue, _ := strings.Cut(stagedMasters, "=")
	section = fmt.Sprintf("[%s]\nlocation = %s\n%s = %s\n", name, location,
		strings.TrimSpace(mastersKey), strings.TrimSpace(mastersValue))
	return name, section, nil
}
