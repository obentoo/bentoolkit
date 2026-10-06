package fetch

import (
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// genVersion generates valid version strings (Gentoo format)
func genVersion() gopter.Gen {
	return gen.RegexMatch(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)
}
