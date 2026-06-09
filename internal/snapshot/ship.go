package snapshot

import "context"

// ShipReport is the outcome of a single Shipper.Send. For the ssh shipper the
// actual byte transfer is delegated to btrbk during engine Create, so Delegated
// is true and Send only records which target was (or will be) served.
type ShipReport struct {
	Target      string // replication destination (e.g. user@host:/path)
	Snapshot    string // ID/path of the snapshot shipped
	Delegated   bool   // true when the transfer is performed by the engine (btrbk)
	Note        string // human-readable detail
	Incremental bool   // archive: true when sent with -p (incremental)
}

// Shipper replicates a snapshot to a remote target. Drivers are selected from
// ship.type via newShipper (R3.1).
type Shipper interface {
	Name() string
	Send(ctx context.Context, snap Snapshot) (ShipReport, error)
}
