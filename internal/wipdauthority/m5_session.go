package wipdauthority

import (
	"context"
	"net"
	"net/http"
	"sort"
	"sync"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	labNegotiatePath      = "/wipd/v1/negotiate"
	labMaxTransferEvents  = 32
	labMaxManifestEntries = 64
	labMaxTransferBytes   = 4 << 20

	labProtocolProblemInvalidCapabilities  = "protocol.invalid-capabilities"
	labProtocolProblemIncompatibleVersion  = "protocol.incompatible-version"
	labProtocolProblemUnsupportedExtension = "protocol.unsupported-extension"
)

type labSessionContextKey struct{}

type labConnectionSession struct {
	mu              sync.Mutex
	negotiating     bool
	negotiated      bool
	failed          bool
	operations      map[operation.ID]struct{}
	commandSubmitV2 bool
}

func labConnectionContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, labSessionContextKey{}, &labConnectionSession{})
}

func connectionSession(request *http.Request) *labConnectionSession {
	session, _ := request.Context().Value(labSessionContextKey{}).(*labConnectionSession)
	return session
}

func negotiateLab(payload []byte, supported []operation.Definition) ([]byte, []byte, map[operation.ID]struct{}, string) {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"protocol_min", "protocol_max", "identity_schemas", "operations", "store_schemas", "features")
	if err != nil {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	minimum, minOK := fields["protocol_min"].([]any)
	maximum, maxOK := fields["protocol_max"].([]any)
	if !minOK || !maxOK || len(minimum) != 2 || len(maximum) != 2 {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	minMajor, minMajorOK := minimum[0].(uint64)
	minMinor, minMinorOK := minimum[1].(uint64)
	maxMajor, maxMajorOK := maximum[0].(uint64)
	maxMinor, maxMinorOK := maximum[1].(uint64)
	if !minMajorOK || !minMinorOK || !maxMajorOK || !maxMinorOK || minMajor == 0 || minMajor != maxMajor || minMajor > 65535 || minMinor > 65535 || maxMinor > 65535 ||
		minMinor > maxMinor {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	if minMajor != 1 || minMinor > 0 || maxMajor != 1 {
		return nil, nil, nil, labProtocolProblemIncompatibleVersion
	}
	identitySchemas, ok := sortedStrings(fields["identity_schemas"])
	if !ok {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	storeSchemas, ok := sortedStrings(fields["store_schemas"])
	if !ok {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	features, ok := sortedStrings(fields["features"])
	if !ok {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	clientOperations, ok := operationCapabilities(fields["operations"])
	if !ok {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	for _, capability := range clientOperations {
		for _, schema := range capability.schemas {
			if !containsString(identitySchemas, schema) {
				return nil, nil, nil, labProtocolProblemInvalidCapabilities
			}
		}
	}
	if !containsString(identitySchemas, "wipd.command/1") || !containsString(storeSchemas, "wipd.store/1") || !containsString(features, "wipd.frame/1") {
		return nil, nil, nil, labProtocolProblemUnsupportedExtension
	}
	selected := make(map[operation.ID]struct{})
	selectedVersions := make(map[string][]any)
	selectedSchemas := []string{"wipd.command/1"}
	for _, definition := range supported {
		id := definition.Metadata().Operation
		if id == operation.GateExemptionRepairV1.Metadata().Operation && !containsString(features, wipdwire.CommandSubmitV2Feature) {
			continue
		}
		client, found := findCapability(clientOperations, id.Name)
		if !found || !containsVersion(client.versions, uint64(id.Version)) || !containsString(client.schemas, "wipd.command/1") || !containsString(identitySchemas, "wipd.command/1") {
			continue
		}
		selected[id] = struct{}{}
		selectedVersions[id.Name] = append(selectedVersions[id.Name], uint64(id.Version))
	}
	selectedNames := make([]string, 0, len(selectedVersions))
	for name := range selectedVersions {
		selectedNames = append(selectedNames, name)
	}
	sort.Strings(selectedNames)
	selectedCapabilities := make([]any, 0, len(selectedNames))
	for _, name := range selectedNames {
		selectedCapabilities = append(selectedCapabilities, map[string]any{
			"name": name, "versions": selectedVersions[name], "identity_schemas": []any{"wipd.command/1"},
		})
	}
	selectedFeatures := []any{"wipd.frame/1"}
	if containsString(features, wipdwire.CommandSubmitV2Feature) {
		selectedFeatures = []any{wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}
	}
	serverHello, err := wipdwire.EncodeCanonical(map[string]any{
		"selected_protocol": []any{uint64(1), uint64(0)},
		"identity_schemas":  stringsToAny(selectedSchemas),
		"operations":        selectedCapabilities,
		"store_schemas":     []any{"wipd.store/1"},
		"features":          selectedFeatures,
	})
	if err != nil {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	parameters, err := wipdwire.EncodeCanonical(map[string]any{
		"frame_schema":             "wipd.frame/1",
		"max_frame_body":           uint64(wipdwire.FrameLimit),
		"max_chunk_data":           uint64(65_536),
		"max_stream_bytes":         uint64(labMaxTransferBytes),
		"max_concurrent_exchanges": uint64(32),
		"receive_window_bytes":     uint64(1_048_576),
	})
	if err != nil {
		return nil, nil, nil, labProtocolProblemInvalidCapabilities
	}
	return serverHello, parameters, selected, ""
}

type labOperationCapability struct {
	name     string
	versions []uint64
	schemas  []string
}

func sortedStrings(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok || text == "" || len(result) > 0 && result[len(result)-1] >= text {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func operationCapabilities(value any) ([]labOperationCapability, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]labOperationCapability, 0, len(values))
	previous := ""
	for _, item := range values {
		fields, ok := item.(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(fields, "name", "versions", "identity_schemas") {
			return nil, false
		}
		name, ok := fields["name"].(string)
		if !ok || name == "" || previous != "" && previous >= name {
			return nil, false
		}
		versions, ok := fields["versions"].([]any)
		if !ok || len(versions) == 0 {
			return nil, false
		}
		lastVersion := uint64(0)
		for _, value := range versions {
			version, valid := value.(uint64)
			if !valid || version == 0 || version > 65535 || version <= lastVersion {
				return nil, false
			}
			lastVersion = version
		}
		schemas, ok := sortedStrings(fields["identity_schemas"])
		if !ok || len(schemas) == 0 {
			return nil, false
		}
		versionsTyped := make([]uint64, len(versions))
		for i := range versions {
			versionsTyped[i] = versions[i].(uint64)
		}
		previous = name
		result = append(result, labOperationCapability{name: name, versions: versionsTyped, schemas: schemas})
	}
	return result, true
}

func findCapability(values []labOperationCapability, name string) (labOperationCapability, bool) {
	index := sort.Search(len(values), func(i int) bool { return values[i].name >= name })
	if index == len(values) || values[index].name != name {
		return labOperationCapability{}, false
	}
	return values[index], true
}

func containsVersion(values []uint64, target uint64) bool {
	index := sort.Search(len(values), func(i int) bool { return values[i] >= target })
	return index < len(values) && values[index] == target
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

func containsString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
