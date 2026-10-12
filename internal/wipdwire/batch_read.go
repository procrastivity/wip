package wipdwire

import "fmt"

// NamedBatchReadFeature gates the explicit authenticated named-Batch read.
const NamedBatchReadFeature = "wipd.named-batch-read/1"

// NamedBatchReadQuery identifies the one explicitly supported S9-C query.
type NamedBatchReadQuery struct {
	Name    string               `cbor:"name"`
	Version uint16               `cbor:"version"`
	Filter  NamedBatchReadFilter `cbor:"filter"`
}

// NamedBatchReadIdentity is the closed M2 response query identity. The filter
// is represented separately by the response's filter_hash.
type NamedBatchReadIdentity struct {
	Name    string `cbor:"name"`
	Version uint16 `cbor:"version"`
}

// NamedBatchReadFilter is intentionally closed to one named Batch ID.
type NamedBatchReadFilter struct {
	BatchID string `cbor:"batch_id"`
}

// LocalNamedBatchReadRequest is sent over the authenticated local IPC session.
type LocalNamedBatchReadRequest struct {
	Schema    string              `cbor:"schema"`
	Query     NamedBatchReadQuery `cbor:"query"`
	PageSize  uint16              `cbor:"page_size"`
	PageToken *string             `cbor:"page_token"`
}

// AuthorityNamedBatchReadRequest binds the same query to the authority profile.
type AuthorityNamedBatchReadRequest struct {
	Schema        string              `cbor:"schema"`
	DomainID      string              `cbor:"domain_id"`
	Epoch         uint64              `cbor:"expected_epoch"`
	Query         NamedBatchReadQuery `cbor:"query"`
	Source        string              `cbor:"source"`
	OverlayPolicy string              `cbor:"overlay_policy"`
	PageSize      uint16              `cbor:"page_size"`
	PageToken     *string             `cbor:"page_token"`
}

// NamedBatchReadSnapshot pins result pages to one authority event prefix.
type NamedBatchReadSnapshot struct {
	ID            string       `cbor:"id"`
	DomainID      string       `cbor:"domain_id"`
	Epoch         uint64       `cbor:"authority_epoch"`
	AsOf          PrefixAnchor `cbor:"as_of"`
	OverlayPolicy string       `cbor:"overlay_policy"`
	ExpiresAt     string       `cbor:"expires_at"`
}

// NamedBatchReadProvenance explains the authority-only state represented.
type NamedBatchReadProvenance struct {
	Source                string        `cbor:"source"`
	AuthorityReachability string        `cbor:"authority_reachability"`
	AuthorityAsOf         *PrefixAnchor `cbor:"authority_as_of"`
	LocalBaseAsOf         *PrefixAnchor `cbor:"local_base_as_of"`
	PendingJournalCount   uint64        `cbor:"pending_journal_count"`
	ProvisionalItemCount  uint64        `cbor:"provisional_item_count"`
	QuarantinedItemCount  uint64        `cbor:"quarantined_item_count"`
	HistoryState          string        `cbor:"history_state"`
}

// NamedBatchReadItem is one canonical query-specific result item.
type NamedBatchReadItem struct {
	Value               []byte  `cbor:"value"`
	State               string  `cbor:"state"`
	EnvironmentSequence *uint64 `cbor:"environment_sequence"`
}

// NamedBatchReadResponse uses the common M2 read-response envelope. Item
// values are canonical query-specific CBOR byte strings.
type NamedBatchReadResponse struct {
	Schema        string                   `cbor:"schema"`
	Query         NamedBatchReadIdentity   `cbor:"query"`
	FilterHash    string                   `cbor:"filter_hash"`
	Snapshot      NamedBatchReadSnapshot   `cbor:"snapshot"`
	Provenance    NamedBatchReadProvenance `cbor:"provenance"`
	Items         []NamedBatchReadItem     `cbor:"items"`
	NextPageToken *string                  `cbor:"next_page_token"`
	Complete      bool                     `cbor:"complete"`
}

// DecodeNamedBatchReadResponse enforces the closed common M2 envelope,
// including the nested {name,version} query identity.
func DecodeNamedBatchReadResponse(data []byte, response *NamedBatchReadResponse) error {
	if response == nil {
		return ErrInvalidRecord
	}
	const (
		schema        = "schema"
		query         = "query"
		filterHash    = "filter_hash"
		snapshot      = "snapshot"
		provenance    = "provenance"
		items         = "items"
		nextPageToken = "next_page_token"
		complete      = "complete"
	)
	fields, err := DecodeCanonicalMap(data, schema, query, filterHash, snapshot, provenance, items, nextPageToken, complete)
	if err != nil {
		return err
	}
	queryFields, ok := fields[query].(map[string]any)
	if !ok || !ExactMapKeys(queryFields, "name", "version") {
		return fmt.Errorf("%w: response query identity", ErrInvalidRecord)
	}
	return DecodeCanonical(data, response, schema, query, filterHash, snapshot, provenance, items, nextPageToken, complete)
}
