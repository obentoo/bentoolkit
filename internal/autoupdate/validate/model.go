package validate

// This file holds the option model both sides of the comparison are expressed
// in (design.md D3). Everything here is data: the shapes are shared by the
// archive reader, the ebuild reader and Compare, and none of them owns it.

// Option is one build option name, on either side of the comparison.
//
// Source is the only field that is not part of the option's identity, and it is
// the one the report cannot do without: it names the archive member a declared
// option was read from, or the ebuild line a passed one was written on. A
// finding that cannot be traced back to the line that caused it makes the
// operator grep for it, which is the difference between an actionable gate and
// an annoying one.
type Option struct {
	Name       string // "aalib"
	Subproject string // "" for the root project
	Source     string // archive member or ebuild line it was read from
}

// Qualified renders the option the way an ebuild would write it: bare for the
// root project, "<subproject>:<name>" for a subproject's. It is what both the
// comparison keys on and the report prints, so the two cannot disagree about
// what an option is called.
func (o Option) Qualified() string {
	if o.Subproject == "" {
		return o.Name
	}
	return o.Subproject + ":" + o.Name
}
