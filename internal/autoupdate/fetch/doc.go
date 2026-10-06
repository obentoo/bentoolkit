// Package fetch is autoupdate's HTTP layer: the retrying client with its
// circuit breaker, per-host policies and body caps, the rate limiter, the
// response and fetch caches, header allow-listing with credential scoping
// (story 052), and authenticated distfile fetching driven by a record's
// meta.fetch_* keys, including the spec parser that --lint reuses.
//
// It imports no other autoupdate package except statefile; the registry
// imports it to validate meta.fetch_* the way the fetch parses it (story 061).
package fetch
