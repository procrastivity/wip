package store

// v9 adds the event vocabulary for a state candidate that found the provider
// already at the requested disposition.
func v9Statements() []string {
	return []string{
		seedTaxonomy(V9Taxonomy),
	}
}
