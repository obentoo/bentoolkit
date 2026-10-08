// Package provider reads another repository's ebuild versions, for comparing
// the overlay against it and reviving packages from it. Provider is the
// interface; it is implemented over the GitHub and GitLab APIs, a shallow git
// clone kept in a cache, and a local tree read in place.
package provider
