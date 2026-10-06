package autoupdate

import (
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
)

// genValidURL generates valid URL strings
func genValidURL() gopter.Gen {
	return gen.RegexMatch(`^https://[a-z]{3,10}\.[a-z]{2,5}/[a-z0-9/]{1,20}$`)
}

// genPackageName generates valid package names in category/package format
func genPackageName() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{3,10}-[a-z]{3,10}/[a-z][a-z0-9-]{2,15}$`)
}
