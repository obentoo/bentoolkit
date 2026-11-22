package main

import (
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// displayMirrorConversion warns about the half of a mirror conversion the
// applier cannot do: the header comment and the registry `patched` text are
// prose that still describes a same-version mirror. It prints nothing for a
// bump that did not leave ::gentoo behind.
func displayMirrorConversion(result *autoupdate.ApplyResult) {
	conv := result.MirrorConversion
	if conv == nil {
		return
	}
	output.Warning.Println("    " + i18n.T("Mirror:  this bump is past ::gentoo; its header comment and registry `patched` text still describe a same-version mirror — rewrite them"))
	if conv.KeywordsNotRewritten {
		output.Warning.Println("    " + i18n.T("Mirror:  KEYWORDS not rewritten (not a single-line value) — set it to ~amd64 by hand"))
	}
}

// noteUnknownGentooTree says once per run that no local ::gentoo tree could
// be read, so no bump was checked for leaving ::gentoo behind and no mirror
// was converted. Without the line, an unsynced host would look like a run
// that found nothing to convert.
func (ar *autoupdateRun) noteUnknownGentooTree(result *autoupdate.ApplyResult) {
	if !result.GentooTreeUnknown || ar.gentooNoticeShown {
		return
	}
	ar.gentooNoticeShown = true
	output.Info.Println(i18n.T("Note: no local ::gentoo tree could be read (set BENTOO_GENTOO_REPO), so no bump was checked for leaving ::gentoo behind"))
}
