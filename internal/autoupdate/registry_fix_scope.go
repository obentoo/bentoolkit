package autoupdate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// ErrRegistryFixOutOfScope marks a registry fix that changed something besides
// the failed package's own extraction config. The agent is told to edit only
// that section, but it reads untrusted upstream pages, so the rule is enforced
// here instead of trusted: a prompt injection that rewrites another record's
// fetch_url, adds a credential header or drops a script must not survive.
var ErrRegistryFixOutOfScope = errors.New("edit outside the failed package's entry")

// registryConfigFile is the registry the fixer is allowed to edit, relative to
// the .autoupdate directory.
const registryConfigFile = "packages.toml"

// dirSnapshot holds every regular file under a directory as path → bytes and
// mode, the pre-attempt state a scope violation is rolled back to.
type dirSnapshot struct {
	root  string
	files map[string]snapshotFile
}

type snapshotFile struct {
	data []byte
	mode fs.FileMode
}

// snapshotDir reads every regular file under root. A symlink or other special
// file is recorded by mode only, so a later change of its type still shows up.
// Reads go through an os.Root, so a symlink the fix planted cannot make the
// snapshot read a file outside root.
func snapshotDir(root string) (*dirSnapshot, error) {
	s := &dirSnapshot{root: root, files: map[string]snapshotFile{}}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", root, err)
	}
	defer func() { _ = r.Close() }()
	err = fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", rel, err)
		}
		f := snapshotFile{mode: info.Mode()}
		if info.Mode().IsRegular() {
			if f.data, err = r.ReadFile(rel); err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
		}
		s.files[filepath.FromSlash(rel)] = f
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", root, err)
	}
	return s, nil
}

// changedFiles lists, in lexical order, the paths under the snapshot's root
// that were added, removed or modified since the snapshot, skipping except.
func (s *dirSnapshot) changedFiles(except string) ([]string, error) {
	now, err := snapshotDir(s.root)
	if err != nil {
		return nil, err
	}
	var changed []string
	for rel, before := range s.files {
		after, ok := now.files[rel]
		if rel != except && (!ok || after.mode != before.mode || !bytes.Equal(after.data, before.data)) {
			changed = append(changed, rel)
		}
	}
	for rel := range now.files {
		if _, ok := s.files[rel]; !ok && rel != except {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// restore puts every snapshotted regular file back, except the path named by
// except, and removes any file that appeared after the snapshot. Errors are
// joined so one failure does not hide the rest.
func (s *dirSnapshot) restore(except string) error {
	now, err := snapshotDir(s.root)
	if err != nil {
		return err
	}
	var errs []error
	for rel := range now.files {
		if _, ok := s.files[rel]; !ok && rel != except {
			if err := os.Remove(filepath.Join(s.root, rel)); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", rel, err))
			}
		}
	}
	for rel, f := range s.files {
		if rel == except || !f.mode.IsRegular() {
			continue
		}
		if cur, ok := now.files[rel]; ok && cur.mode == f.mode && bytes.Equal(cur.data, f.data) {
			continue
		}
		path := filepath.Join(s.root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			errs = append(errs, fmt.Errorf("recreate directory of %s: %w", rel, err))
			continue
		}
		if err := fileutil.WriteFileAtomic(path, f.data, f.mode.Perm()); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", rel, err))
		}
	}
	return errors.Join(errs...)
}

// checkRegistryFixScope decides whether the edit from before to after stays
// inside pkg's own entry. It compares decoded TOML, so reformatting or a
// comment line elsewhere is not a violation, while any value change is. Inside
// pkg's entry, meta (the authenticated-fetch block whose fetch_url receives the
// operator's secrets) must be unchanged, and no header may gain or change a
// ${VAR} expansion, the other path by which an environment secret leaves the
// machine.
func checkRegistryFixScope(before, after []byte, pkg string) error {
	var oldCfg, newCfg map[string]any
	if _, err := toml.Decode(string(before), &oldCfg); err != nil {
		return fmt.Errorf("decode pre-fix %s: %w", registryConfigFile, err)
	}
	// Unparseable output is not a scope violation: the re-check reports it as
	// a failing fix and the caller reverts it.
	if _, decodeErr := toml.Decode(string(after), &newCfg); decodeErr != nil {
		newCfg = nil
	}
	if newCfg == nil {
		return nil
	}

	var problems []string
	for key, oldVal := range oldCfg {
		newVal, ok := newCfg[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("removed entry %q", key))
		case key != pkg && !reflect.DeepEqual(oldVal, newVal):
			problems = append(problems, fmt.Sprintf("changed entry %q", key))
		}
	}
	for key := range newCfg {
		if _, ok := oldCfg[key]; !ok {
			problems = append(problems, fmt.Sprintf("added entry %q", key))
		}
	}

	oldEntry, _ := oldCfg[pkg].(map[string]any)
	newEntry, _ := newCfg[pkg].(map[string]any)
	if !reflect.DeepEqual(oldEntry["meta"], newEntry["meta"]) {
		problems = append(problems, "changed the entry's meta")
	}
	oldHeaders, _ := oldEntry["headers"].(map[string]any)
	newHeaders, _ := newEntry["headers"].(map[string]any)
	for name, v := range newHeaders {
		value, _ := v.(string)
		if strings.Contains(value, "${") && oldHeaders[name] != v {
			problems = append(problems, fmt.Sprintf("added or changed header %q with an environment expansion", name))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%w: %s", ErrRegistryFixOutOfScope, strings.Join(problems, "; "))
}
