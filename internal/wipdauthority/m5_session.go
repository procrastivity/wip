package wipdauthority

import (
	"context"
	"net"
	"net/http"
	"sort"
	"sync"

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
	mu          sync.Mutex
	negotiating bool
	negotiated  bool
	failed      bool
}

func labConnectionContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, labSessionContextKey{}, &labConnectionSession{})
}

func connectionSession(request *http.Request) *labConnectionSession {
	session, _ := request.Context().Value(labSessionContextKey{}).(*labConnectionSession)
	return session
}

func negotiateLab(payload []byte) ([]byte, []byte, string) {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"protocol_min", "protocol_max", "identity_schemas", "operations", "store_schemas", "features")
	if err != nil {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	minimum, minOK := fields["protocol_min"].([]any)
	maximum, maxOK := fields["protocol_max"].([]any)
	if !minOK || !maxOK || len(minimum) != 2 || len(maximum) != 2 {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	minMajor, minMajorOK := minimum[0].(uint64)
	minMinor, minMinorOK := minimum[1].(uint64)
	maxMajor, maxMajorOK := maximum[0].(uint64)
	maxMinor, maxMinorOK := maximum[1].(uint64)
	if !minMajorOK || !minMinorOK || !maxMajorOK || !maxMinorOK || minMajor == 0 || minMajor != maxMajor || minMajor > 65535 || minMinor > 65535 || maxMinor > 65535 ||
		minMinor > maxMinor {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	if minMajor != 1 || minMinor > 0 || maxMajor != 1 {
		return nil, nil, labProtocolProblemIncompatibleVersion
	}
	identitySchemas, ok := sortedStrings(fields["identity_schemas"])
	if !ok {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	storeSchemas, ok := sortedStrings(fields["store_schemas"])
	if !ok {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	features, ok := sortedStrings(fields["features"])
	if !ok {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	if _, ok = operationCapabilities(fields["operations"]); !ok {
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	if !containsString(identitySchemas, "wipd.command/1") || !containsString(storeSchemas, "wipd.store/1") || !containsString(features, "wipd.frame/1") {
		return nil, nil, labProtocolProblemUnsupportedExtension
	}
	serverHello, err := wipdwire.EncodeCanonical(map[string]any{
		"selected_protocol": []any{uint64(1), uint64(0)},
		"identity_schemas":  []any{"wipd.command/1"},
		"operations":        []any{},
		"store_schemas":     []any{"wipd.store/1"},
		"features":          []any{"wipd.frame/1"},
	})
	if err != nil {
		return nil, nil, labProtocolProblemInvalidCapabilities
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
		return nil, nil, labProtocolProblemInvalidCapabilities
	}
	return serverHello, parameters, ""
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

func operationCapabilities(value any) ([][]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([][]string, 0, len(values))
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
		previous = name
		result = append(result, schemas)
	}
	return result, true
}

func containsString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
