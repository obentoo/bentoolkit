// Package config loads and saves ~/.config/bentoo/config.yaml: the overlay,
// git identity, autoupdate, validation, UI, notice and tray settings.
//
// Every default comes from a getter rather than a struct literal, so an
// absent, partial or nil block resolves the same way. Unknown keys never stop
// a load: a strict second decode reports them as warnings. Secrets are not
// read from this file; package secrets resolves them.
package config
