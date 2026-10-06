package autoupdate

// authFetchErrorChain returns every error reachable from err through Unwrap,
// both the single and the multi-error forms.
func authFetchErrorChain(err error) []error {
	var out []error
	queue := []error{err}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if e == nil {
			continue
		}
		out = append(out, e)
		switch u := e.(type) { //nolint:errorlint // this IS the Unwrap walk: each link is inspected on its own, which errors.As would skip past
		case interface{ Unwrap() []error }:
			queue = append(queue, u.Unwrap()...)
		case interface{ Unwrap() error }:
			queue = append(queue, u.Unwrap())
		}
	}
	return out
}
