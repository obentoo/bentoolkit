package main

import (
	"fmt"
	"strings"
)

// validateGitPathArgs rejects arguments that look like git flags.
// Only file/directory paths are accepted as positional arguments.
func validateGitPathArgs(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("invalid argument %q: only file paths are accepted as positional args", arg)
		}
	}
	return nil
}
