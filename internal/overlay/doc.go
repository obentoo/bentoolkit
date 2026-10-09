// Package overlay implements the overlay operations behind `bentoo overlay`:
// the git workflow (status, add, commit with a generated message, push,
// pull), version renames, Manifest regeneration, pruning of superseded
// ebuilds, and compare, which reads each package against a baseline
// repository, classifies how it diverges and, with a model available, reviews
// the divergence.
package overlay
