// Package smartmon lists the drives udisks2 exports with their SMART verdict
// and tells when that verdict may have changed, over D-Bus. It is read-only:
// it reads properties udisks2 already holds and never calls a method that
// refreshes SMART data, runs a self-test or changes a setting, so it can never
// trigger an authorization prompt. Policy (notify, clear, log) is the
// caller's: this package only reports.
package smartmon
