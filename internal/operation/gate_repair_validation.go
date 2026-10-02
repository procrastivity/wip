package operation

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// These are structural input checks only. Owner proof, historical eligibility,
// nonce freshness and claim fencing remain authority-store responsibilities.
func validateGateExemptionRepairInput(input GateExemptionRepairInput) error {
	if validateULID("gate repair node ID", input.NodeID) != nil ||
		!validRepairText(input.Gate, 1, 256, false) ||
		input.EventCount == 0 || input.EventCount > 1<<63-1 ||
		validateULID("gate declaration high-water event ID", input.HighWaterEventID) != nil ||
		!digestPattern.MatchString(input.PrefixDigest) ||
		!validRepairIncidentRef(input.IncidentRef) ||
		!validRepairText(input.Reason, 1, 4096, true) ||
		len(input.EvidenceRefs) == 0 || len(input.EvidenceRefs) > 32 {
		return fmt.Errorf("gate.exemption.repair@v1 has invalid incident or declaration boundary")
	}
	for index, ref := range input.EvidenceRefs {
		if !digestPattern.MatchString(ref) || index > 0 && input.EvidenceRefs[index-1] >= ref {
			return fmt.Errorf("gate.exemption.repair@v1 evidence references must be sorted unique sha256 digests")
		}
	}
	return nil
}

func validRepairText(value string, minBytes, maxBytes int, allowControls bool) bool {
	if !utf8.ValidString(value) || len(value) < minBytes || len(value) > maxBytes || !norm.NFC.IsNormalString(value) {
		return false
	}
	if !allowControls {
		for _, char := range value {
			if unicode.IsControl(char) {
				return false
			}
		}
	}
	return true
}

func validRepairIncidentRef(value string) bool {
	if !validRepairText(value, 1, 2048, false) {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return false
	}
	switch {
	case strings.EqualFold(parsed.Scheme, "https"):
		return parsed.Opaque == "" && parsed.Host != "" && parsed.Hostname() != "" && parsed.User == nil
	case strings.EqualFold(parsed.Scheme, "urn"):
		return validRepairURN(value)
	default:
		return false
	}
}

func validRepairURN(value string) bool {
	if len(value) < len("urn:") || !strings.EqualFold(value[:len("urn:")], "urn:") {
		return false
	}
	nid, assignedName, found := strings.Cut(value[len("urn:"):], ":")
	if !found || len(nid) < 2 || len(nid) > 32 || assignedName == "" {
		return false
	}
	for index := 0; index < len(nid); index++ {
		char := nid[index]
		if !repairAlphaNumeric(char) && (char != '-' || index == 0 || index == len(nid)-1) {
			return false
		}
	}
	assignedEnd := strings.IndexAny(assignedName, "?#")
	nss, optional := assignedName, ""
	if assignedEnd >= 0 {
		nss, optional = assignedName[:assignedEnd], assignedName[assignedEnd:]
	}
	return validRepairURNPrefix(nss, true, false) && validRepairURNOptional(optional)
}

func validRepairURNOptional(optional string) bool {
	if optional == "" {
		return true
	}
	if strings.HasPrefix(optional, "#") {
		return validRepairURNComponent(optional[1:], true, true)
	}
	if strings.HasPrefix(optional, "?=") {
		return validRepairURNQuery(optional[2:])
	}
	if !strings.HasPrefix(optional, "?+") {
		return false
	}
	remainder := optional[2:]
	end := len(remainder)
	if index := strings.Index(remainder, "?="); index >= 0 && index < end {
		end = index
	}
	if index := strings.IndexByte(remainder, '#'); index >= 0 && index < end {
		end = index
	}
	if !validRepairURNPrefix(remainder[:end], true, true) {
		return false
	}
	trailing := remainder[end:]
	if trailing == "" {
		return true
	}
	if strings.HasPrefix(trailing, "?=") {
		return validRepairURNQuery(trailing[2:])
	}
	if strings.HasPrefix(trailing, "#") {
		return validRepairURNComponent(trailing[1:], true, true)
	}
	return false
}

func validRepairURNQuery(value string) bool {
	query, fragment, hasFragment := strings.Cut(value, "#")
	return validRepairURNPrefix(query, true, true) && (!hasFragment || validRepairURNComponent(fragment, true, true))
}

func validRepairURNPrefix(value string, allowSlash, allowQuestion bool) bool {
	if len(value) == 0 {
		return false
	}
	if value[0] == '%' {
		return len(value) >= 3 && repairHex(value[1]) && repairHex(value[2]) && validRepairURNComponent(value[3:], allowSlash, allowQuestion)
	}
	return repairPChar(value[0]) && validRepairURNComponent(value[1:], allowSlash, allowQuestion)
}

func validRepairURNComponent(value string, allowSlash, allowQuestion bool) bool {
	for index := 0; index < len(value); {
		char := value[index]
		if char == '%' {
			if index+2 >= len(value) || !repairHex(value[index+1]) || !repairHex(value[index+2]) {
				return false
			}
			index += 3
			continue
		}
		if repairPChar(char) || allowSlash && char == '/' || allowQuestion && char == '?' {
			index++
			continue
		}
		return false
	}
	return true
}

func repairPChar(char byte) bool {
	return repairAlphaNumeric(char) || strings.ContainsRune("-._~!$&'()*+,;=:@", rune(char))
}

func repairAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func repairHex(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}
