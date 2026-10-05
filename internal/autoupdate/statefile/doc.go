// Package statefile reads, merges and saves autoupdate's JSON state files
// under the inter-process file lock, so concurrent bentoo processes never
// lose each other's writes (story 056). It sits at the bottom of the
// autoupdate package graph and imports no other autoupdate package (story 061).
package statefile
