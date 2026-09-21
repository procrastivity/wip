package store

// v14 adds the one event type an operator-authored tracker proposal needs, and
// nothing else: no table changes, no data backfill, and no projectionVersion
// bump.
//
// The absent bump is the deliberate part. A bump refolds every existing store
// at its next open, and it is what v13 needed because v13's synthetic history
// gave already-present rows events to fold from. Here there is nothing to
// refold: `tracker.adhoc-proposed` is a new token, and `events.type` is a
// foreign key into `event_types`, so no store on disk can be carrying an
// instance of a type its taxonomy has never held. The fold rule that arrives
// with it (projectAdhocCandidate) is therefore a rule about events that do not
// exist yet, which is the one shape of projection change that costs an existing
// store nothing.
func v14Statements() []string {
	return []string{
		seedTaxonomy(V14Taxonomy),
	}
}
