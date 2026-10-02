package wipd

import (
	"fmt"
	"math"
	"regexp"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	identitySchemaV1         = "wipd.command/1"
	storeSchemaV1            = "wipd.store/1"
	birthReleaseFeature      = "wipd.birth-claim-release/1"
	claimAcquireFeature      = "wipd.claim-acquire/1"
	claimJournalCloseFeature = "wipd.claim-journal-close/1"
	defaultChunkSize         = uint64(65_536)
	defaultStreamMax         = uint64(8_589_934_592)
	absoluteStreamMax        = uint64(1_099_511_627_776)
	maxExchangesAbsolute     = uint64(64)
	defaultExchanges         = uint64(32)
	defaultReceiveWindow     = uint64(1_048_576)
	absoluteReceiveWindow    = uint64(16_777_216)
)

var capabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*(?:/[1-9][0-9]*)?$`)

type protocolVersion struct {
	major uint16
	minor uint16
}

type operationCapability struct {
	name            string
	versions        []uint16
	identitySchemas []string
}

type capabilityHello struct {
	protocolMin     protocolVersion
	protocolMax     protocolVersion
	identitySchemas []string
	operations      []operationCapability
	storeSchemas    []string
	features        []string
}

type serverHello struct {
	selectedProtocol protocolVersion
	identitySchemas  []string
	operations       []operationCapability
	storeSchemas     []string
	features         []string
}

type sessionParameters struct {
	frameSchema            string
	maxFrameBody           uint64
	maxChunkData           uint64
	maxStreamBytes         uint64
	maxConcurrentExchanges uint64
	receiveWindowBytes     uint64
}

func defaultSessionParameters(maxExchanges uint64) sessionParameters {
	return sessionParameters{
		frameSchema:            frameSchema,
		maxFrameBody:           uint64(maxFrameBodyLimit),
		maxChunkData:           defaultChunkSize,
		maxStreamBytes:         defaultStreamMax,
		maxConcurrentExchanges: maxExchanges,
		receiveWindowBytes:     defaultReceiveWindow,
	}
}

func (parameters sessionParameters) value() map[string]any {
	return map[string]any{
		"frame_schema":             parameters.frameSchema,
		"max_frame_body":           parameters.maxFrameBody,
		"max_chunk_data":           parameters.maxChunkData,
		"max_stream_bytes":         parameters.maxStreamBytes,
		"max_concurrent_exchanges": parameters.maxConcurrentExchanges,
		"receive_window_bytes":     parameters.receiveWindowBytes,
	}
}

func decodeClientHello(payload []byte) (capabilityHello, error) {
	value, err := decodePayload(payload)
	if err != nil {
		return capabilityHello{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "protocol_min", "protocol_max", "identity_schemas", "operations", "store_schemas", "features") {
		return capabilityHello{}, errMalformedMessage
	}

	hello := capabilityHello{}
	if hello.protocolMin, err = parseVersion(fields["protocol_min"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.protocolMax, err = parseVersion(fields["protocol_max"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.protocolMin.major == 0 || hello.protocolMin.major != hello.protocolMax.major ||
		versionLess(hello.protocolMax, hello.protocolMin) {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.identitySchemas, err = parseSortedIDs(fields["identity_schemas"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.storeSchemas, err = parseSortedIDs(fields["store_schemas"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.features, err = parseSortedIDs(fields["features"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	if hello.operations, err = parseOperationCapabilities(fields["operations"]); err != nil {
		return capabilityHello{}, errInvalidCapabilities
	}
	return hello, nil
}

func negotiateCapabilities(client capabilityHello, registry *operation.Registry, birthRelease, claimAcquire, claimJournalClose, commandSubmitV2 bool) (serverHello, sessionParameters, error) {
	const supportedMajor, supportedMinor = uint16(1), uint16(0)
	if client.protocolMin.major != supportedMajor || client.protocolMin.minor > supportedMinor ||
		client.protocolMax.major != supportedMajor || client.protocolMax.minor < supportedMinor {
		return serverHello{}, sessionParameters{}, errIncompatibleVersion
	}

	selected := protocolVersion{major: supportedMajor, minor: supportedMinor}
	serverOps := registeredOperationCapabilities(registry)
	serverFeatures := []string{frameSchema}
	if birthRelease {
		serverFeatures = append(serverFeatures, birthReleaseFeature)
	}
	if claimAcquire {
		serverFeatures = append(serverFeatures, claimAcquireFeature)
	}
	if claimJournalClose {
		serverFeatures = append(serverFeatures, claimJournalCloseFeature)
	}
	if commandSubmitV2 {
		serverFeatures = append(serverFeatures, wipdwire.CommandSubmitV2Feature)
	}
	sort.Strings(serverFeatures)
	result := serverHello{
		selectedProtocol: selected,
		identitySchemas:  intersectStrings(client.identitySchemas, []string{identitySchemaV1}),
		operations:       intersectOperations(client.operations, serverOps),
		storeSchemas:     intersectStrings(client.storeSchemas, []string{storeSchemaV1}),
		features:         intersectStrings(client.features, serverFeatures),
	}
	if !containsString(result.identitySchemas, identitySchemaV1) ||
		!containsString(result.storeSchemas, storeSchemaV1) || !containsString(result.features, frameSchema) {
		return serverHello{}, sessionParameters{}, errUnsupportedExtension
	}
	return result, defaultSessionParameters(defaultExchanges), nil
}

func registeredOperationCapabilities(registry *operation.Registry) []operationCapability {
	byName := make(map[string]*operationCapability)
	for _, definition := range registry.Definitions() {
		id := definition.Metadata().Operation
		schema, ok := identitySchemaFor(id)
		if !ok {
			continue
		}
		capability := byName[id.Name]
		if capability == nil {
			capability = &operationCapability{name: id.Name}
			byName[id.Name] = capability
		}
		capability.versions = append(capability.versions, id.Version)
		if !containsString(capability.identitySchemas, schema) {
			capability.identitySchemas = append(capability.identitySchemas, schema)
		}
	}
	capabilities := make([]operationCapability, 0, len(byName))
	for _, capability := range byName {
		sort.Slice(capability.versions, func(i, j int) bool { return capability.versions[i] < capability.versions[j] })
		sort.Strings(capability.identitySchemas)
		capabilities = append(capabilities, *capability)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].name < capabilities[j].name })
	return capabilities
}

func identitySchemaFor(id operation.ID) (string, bool) {
	switch id {
	case operation.MatterCreateV1.Metadata().Operation, operation.StepCreateV1.Metadata().Operation,
		operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
		operation.MatterFinishV1.Metadata().Operation, operation.ContentWriteOnceV1.Metadata().Operation,
		operation.FindingAppendV1.Metadata().Operation:
		return identitySchemaV1, true
	default:
		if operation.Step4Operation(id) || operation.Step5Operation(id) {
			return identitySchemaV1, true
		}
	}
	return "", false
}

func intersectOperations(client, server []operationCapability) []operationCapability {
	var intersection []operationCapability
	for _, offered := range client {
		for _, supported := range server {
			if offered.name != supported.name {
				continue
			}
			versions := intersectVersions(offered.versions, supported.versions)
			schemas := intersectStrings(offered.identitySchemas, supported.identitySchemas)
			if len(versions) != 0 && len(schemas) != 0 {
				intersection = append(intersection, operationCapability{
					name:            offered.name,
					versions:        versions,
					identitySchemas: schemas,
				})
			}
			break
		}
	}
	return intersection
}

func intersectVersions(left, right []uint16) []uint16 {
	var result []uint16
	for _, candidate := range left {
		if containsVersion(right, candidate) {
			result = append(result, candidate)
		}
	}
	return result
}

func intersectStrings(left, right []string) []string {
	var result []string
	for _, candidate := range left {
		if containsString(right, candidate) {
			result = append(result, candidate)
		}
	}
	return result
}

func encodeServerHello(hello serverHello) ([]byte, error) {
	operations := make([]any, 0, len(hello.operations))
	for _, capability := range hello.operations {
		versions := make([]any, 0, len(capability.versions))
		for _, version := range capability.versions {
			versions = append(versions, uint64(version))
		}
		operations = append(operations, map[string]any{
			"name":             capability.name,
			"versions":         versions,
			"identity_schemas": stringsToAny(capability.identitySchemas),
		})
	}
	return encodePayload(map[string]any{
		"selected_protocol": []any{uint64(hello.selectedProtocol.major), uint64(hello.selectedProtocol.minor)},
		"identity_schemas":  stringsToAny(hello.identitySchemas),
		"operations":        operations,
		"store_schemas":     stringsToAny(hello.storeSchemas),
		"features":          stringsToAny(hello.features),
	})
}

func stringsToAny(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func parseVersion(value any) (protocolVersion, error) {
	parts, ok := value.([]any)
	if !ok || len(parts) != 2 {
		return protocolVersion{}, fmt.Errorf("version must be a two-element array")
	}
	major, majorOK := parts[0].(uint64)
	minor, minorOK := parts[1].(uint64)
	if !majorOK || !minorOK || major > math.MaxUint16 || minor > math.MaxUint16 {
		return protocolVersion{}, fmt.Errorf("version component exceeds uint16")
	}
	return protocolVersion{major: uint16(major), minor: uint16(minor)}, nil
}

func parseSortedIDs(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("capability list must be an array")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || !capabilityIDPattern.MatchString(id) {
			return nil, fmt.Errorf("capability identifier is malformed")
		}
		result = append(result, id)
	}
	if !sortedUniqueStrings(result) {
		return nil, fmt.Errorf("capability list is not sorted and unique")
	}
	return result, nil
}

func parseOperationCapabilities(value any) ([]operationCapability, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("operation capabilities must be an array")
	}
	result := make([]operationCapability, 0, len(values))
	previousName := ""
	for index, value := range values {
		fields, ok := value.(map[string]any)
		if !ok || !exactFields(fields, "name", "versions", "identity_schemas") {
			return nil, fmt.Errorf("operation capability %d has an invalid closed schema", index)
		}
		name, ok := fields["name"].(string)
		if !ok {
			return nil, fmt.Errorf("operation capability %d name is not text", index)
		}
		id := operation.ID{Name: name, Version: 1}
		if err := id.Validate(); err != nil || (previousName != "" && previousName >= name) {
			return nil, fmt.Errorf("operation capability names are malformed, duplicated, or unsorted")
		}
		previousName = name
		versions, ok := fields["versions"].([]any)
		if !ok || len(versions) == 0 {
			return nil, fmt.Errorf("operation %s must have supported versions", name)
		}
		capability := operationCapability{name: name}
		for _, value := range versions {
			version, ok := value.(uint64)
			if !ok || version == 0 || version > math.MaxUint16 ||
				(len(capability.versions) != 0 && uint64(capability.versions[len(capability.versions)-1]) >= version) {
				return nil, fmt.Errorf("operation %s versions are invalid, duplicated, or unsorted", name)
			}
			capability.versions = append(capability.versions, uint16(version))
		}
		identitySchemas, err := parseSortedIDs(fields["identity_schemas"])
		if err != nil || len(identitySchemas) == 0 {
			return nil, fmt.Errorf("operation %s identity schemas are invalid", name)
		}
		capability.identitySchemas = identitySchemas
		result = append(result, capability)
	}
	return result, nil
}

func exactFields(fields map[string]any, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func sortedUniqueStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func containsString(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func containsVersion(values []uint16, value uint16) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= value })
	return index < len(values) && values[index] == value
}

func versionLess(left, right protocolVersion) bool {
	return left.major < right.major || (left.major == right.major && left.minor < right.minor)
}
