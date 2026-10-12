// Package repair holds what an operator-present repair of a failed autoupdate
// bump needs: the repair bundle written beside a kept staged tree (its schema,
// its redacted failure text, its atomic write), the lookup that finds it again
// by package, and the argv of the interactive claude session seeded with it.
// It returns errors and never logs; the caller decides what a failure means.
package repair
