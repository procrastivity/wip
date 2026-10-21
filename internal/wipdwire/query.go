package wipdwire

// QueryOperation identifies a capability-negotiated M2 read operation.
type QueryOperation struct {
	Name    string `cbor:"name"`
	Version uint16 `cbor:"version"`
}

// QueryRequest is the closed Step 4 read-only exchange envelope.
type QueryRequest struct {
	Schema       string         `cbor:"schema"`
	Query        QueryOperation `cbor:"query"`
	QueryPayload []byte         `cbor:"query_payload"`
	Deadline     *string        `cbor:"deadline"`
}

// ReadQuery is the existing Step 6 query envelope used by route.resolve.
// Its filter is validated against the selected query's closed filter shape.
type ReadQuery struct {
	Schema        string         `cbor:"schema"`
	DomainID      string         `cbor:"domain_id"`
	ExpectedEpoch uint64         `cbor:"expected_epoch"`
	Source        string         `cbor:"source"`
	Filter        map[string]any `cbor:"filter"`
	OverlayPolicy string         `cbor:"overlay_policy"`
	PageSize      uint16         `cbor:"page_size"`
	PageToken     *string        `cbor:"page_token"`
}
