package store

// v6 adds the provider-neutral confirmation event that retires a delegated
// backlog stub after an external item is created.
func v6Statements() []string {
	return []string{seedTaxonomy(V6Taxonomy)}
}
