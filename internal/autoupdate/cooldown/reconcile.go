package cooldown

// ReasonRegistry lifts an entry whose release line the registry took out of
// the cooldown (cooldown_days = 0) after the entry was masked.
const ReasonRegistry = "registry"

// LinePolicy is what the registry says now about one release line.
type LinePolicy struct {
	// Off is cooldown_days = 0: the line's versions land unmasked.
	Off bool
	// Days is the line's own wait, or 0 for the block's days=N.
	Days int
}

// Retime is one entry whose days= changed to follow the registry.
type Retime struct {
	Package, Version string
	// From and To are the entry's days before and after; 0 is the block's.
	From, To int
}

// Reconcile applies the registry's current policy to the entries already in
// the block, keyed by release line (the registry record key). An entry is
// masked with the policy of its day; without this, taking a record out of the
// cooldown or changing its cooldown_days only reached the NEXT candidate, and
// the masked one kept waiting the old time. An Off line's entries leave the
// block (ReasonRegistry); any other listed line's entries take its Days.
//
// The registry owns only the lines it names: an entry with no line (a manual
// `cooldown add`) and a line absent from policy (a removed or disabled record)
// are left exactly as they are, because nothing current speaks for them.
func (b *Block) Reconcile(policy map[string]LinePolicy) (lifts []Lift, retimed []Retime) {
	kept := b.Entries[:0:0]
	for _, e := range b.Entries {
		p, owned := policy[e.Line]
		switch {
		case e.Line == "" || !owned:
		case p.Off:
			lifts = append(lifts, Lift{Package: e.Package, Version: e.Version, Reason: ReasonRegistry})
			continue
		case e.Days != p.Days:
			retimed = append(retimed, Retime{Package: e.Package, Version: e.Version, From: e.Days, To: p.Days})
			e.Days = p.Days
		}
		kept = append(kept, e)
	}
	b.Entries = kept
	return lifts, retimed
}
