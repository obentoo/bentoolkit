package autoupdate

// Most tests in this package drive the validation pipeline (manifest, fixers,
// gates, promotion) through Apply on a bump they never proved. That pipeline is
// still what --repair runs over an adopted tree, so they keep testing live code;
// the tests of the publish-only rule itself turn the switch back off.
func init() {
	testsValidateUnproven = true
}
