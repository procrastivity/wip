package wipdwire

// SchedulerHookResult is the authenticated Environment-to-authority second
// phase for scheduler.orchestrate@v1. CanonicalCommand is repeated so the
// authority can bind this result to the exact durable submission without
// trusting a caller-supplied command ID alone. Result is a canonical typed
// operation.SchedulerHookOutput payload.
type SchedulerHookResult struct {
	Schema           string `cbor:"schema"`
	DomainID         string `cbor:"domain_id"`
	Epoch            uint64 `cbor:"authority_epoch"`
	EnvironmentID    string `cbor:"environment_id"`
	CommandID        string `cbor:"command_id"`
	RequestHash      string `cbor:"request_hash"`
	CanonicalCommand []byte `cbor:"canonical_command"`
	Result           []byte `cbor:"result"`
}
