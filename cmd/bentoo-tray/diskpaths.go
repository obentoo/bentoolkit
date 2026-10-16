package main

import (
	"io/fs"
	"slices"
	"sync"
)

// diskPaths returns the paths the disk monitor watches and one warning per
// path it had to leave out. Configured paths replace the defaults; without
// them the defaults are /, PORTAGE_TMPDIR and DISTDIR, in that order.
//
// tmpdir and distdir are the two portageq questions, passed as funcs so a
// configured list never runs portageq. Both are asked at once, so startup
// waits for the slower one only. Each answers "" when it cannot, which
// warns naming the variable and keeps the remaining defaults. stat checks each
// path once; a path it cannot find warns naming the path and is dropped until
// the next start. The warnings are returned for main to log.
func diskPaths(configured []string, tmpdir, distdir func() string, stat func(string) (fs.FileInfo, error)) (paths, warnings []string) {
	candidates := slices.Clone(configured)
	if len(candidates) == 0 {
		candidates = []string{"/"}
		tmp, dist := askBoth(tmpdir, distdir)
		for _, q := range []struct{ variable, answer string }{
			{"PORTAGE_TMPDIR", tmp},
			{"DISTDIR", dist},
		} {
			if q.answer != "" {
				candidates = append(candidates, q.answer)
			} else {
				warnings = append(warnings, "portageq did not answer "+q.variable+"; it is not watched")
			}
		}
	}
	for _, path := range candidates {
		if _, err := stat(path); err != nil {
			warnings = append(warnings, "the disk path "+path+" cannot be checked and is not watched until the next start: "+err.Error())
			continue
		}
		paths = append(paths, path)
	}
	return paths, warnings
}

// askBoth runs tmpdir and distdir on two goroutines and returns both answers
// once each has returned. Each func bounds its own wait.
func askBoth(tmpdir, distdir func() string) (tmp, dist string) {
	var wg sync.WaitGroup
	wg.Go(func() { tmp = tmpdir() })
	wg.Go(func() { dist = distdir() })
	wg.Wait()
	return tmp, dist
}
