package store

// v11 adds the durable event vocabulary for emergency gate dismissal. The
// gate_state projection already stores the event identity and timestamp, so no
// table shape change is needed: the new terminal state is derived from the
// event type and its strict payload during reads and rebuilds.
func v11Statements() []string {
	return []string{
		seedTaxonomy(V11Taxonomy),
	}
}
