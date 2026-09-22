package operation

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Access states whether an operation observes or changes semantic state.
type Access string

// AccessRead and AccessMutation are the complete access vocabulary.
const (
	AccessRead     Access = "read"
	AccessMutation Access = "mutation"
)

// DeliveryClass is D120's closed command-delivery vocabulary. DeliveryNone is
// explicit metadata for reads; every mutation uses exactly one other class.
type DeliveryClass string

// DeliveryNone and the mutation delivery constants are D120's closed set.
const (
	DeliveryNone        DeliveryClass = "none"
	DeliveryAuthority   DeliveryClass = "authority"
	DeliveryClaim       DeliveryClass = "claim"
	DeliveryProvisional DeliveryClass = "provisional"
	DeliveryCapture     DeliveryClass = "capture"
	DeliveryEnvironment DeliveryClass = "environment"
)

// ContextDimension names one tier identity a request must supply.
type ContextDimension string

// ContextRepo, ContextClone, and ContextWorktree are the current tier context.
const (
	ContextRepo     ContextDimension = "repo"
	ContextClone    ContextDimension = "clone"
	ContextWorktree ContextDimension = "worktree"
)

// ClaimRequirement says whether the caller must supply an exact claim proof.
// Provisional operations establish an implicit claim but require no existing
// claim, so they use ClaimNone.
type ClaimRequirement string

// ClaimNone and ClaimExact distinguish operations with and without claim proof.
const (
	ClaimNone  ClaimRequirement = "none"
	ClaimExact ClaimRequirement = "exact"
)

// Footprint is one stable, machine-readable guard or write region. Definitions
// use the narrowest honest token; a new token is added only for a real region,
// rather than falling back to prose such as "various rows".
type Footprint string

// FootprintRepoMatterLocators and FootprintNewbornMatter classify the first
// catalogue operation; later operations add only the regions they use.
const (
	FootprintRepoMatterLocators Footprint = "repo.matter-locators"
	FootprintNewbornMatter      Footprint = "newborn-matter-subtree"
)

// BlobSpec statically names one accepted staged blob input.
type BlobSpec struct {
	Name     string
	Required bool
}

// ExternalEffect is an effect outside authority events and their projections.
type ExternalEffect string

// EffectFilesystemRead and the other effect constants are the known external
// effect categories exposed by the Step 1 census.
const (
	EffectFilesystemRead  ExternalEffect = "filesystem-read"
	EffectFilesystemWrite ExternalEffect = "filesystem-write"
	EffectGitRead         ExternalEffect = "git-read"
	EffectRuntimeLock     ExternalEffect = "runtime-lock"
	EffectTrackerRead     ExternalEffect = "tracker-read"
	EffectTrackerWrite    ExternalEffect = "tracker-write"
	EffectAgentHook       ExternalEffect = "agent-hook"
)

// Metadata is the complete static classification of one operation version.
// Slice fields distinguish omission from an intentional empty set: nil is
// incomplete metadata and fails validation; [] explicitly means "none".
type Metadata struct {
	Operation       ID
	Access          Access
	Delivery        DeliveryClass
	RequiredContext []ContextDimension
	Guards          []Footprint
	Writes          []Footprint
	BlobInputs      []BlobSpec
	Claim           ClaimRequirement
	ExternalEffects []ExternalEffect
}

// Definition binds complete metadata to its one accepted input and output DTO.
// The reflected types are internal runtime facts for the in-process boundary;
// they do not select or imply a wire encoding.
type Definition struct {
	metadata   Metadata
	inputType  reflect.Type
	outputType reflect.Type
}

var (
	semanticNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
	tokenPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)
	packagePath         = reflect.TypeOf(ID{}).PkgPath()
)

// String renders an ID for diagnostics and tests. It is not a wire encoding.
func (id ID) String() string {
	return fmt.Sprintf("%s@v%d", id.Name, id.Version)
}

// Validate reports an incomplete or malformed operation identity.
func (id ID) Validate() error {
	if !semanticNamePattern.MatchString(id.Name) {
		return fmt.Errorf("operation name %q must contain at least two lowercase dot-separated segments", id.Name)
	}
	if id.Version == 0 {
		return fmt.Errorf("operation %q has no version", id.Name)
	}
	return nil
}

// Metadata returns a copy whose slices may be changed without altering the
// catalogue definition.
func (d Definition) Metadata() Metadata { return cloneMetadata(d.metadata) }

// InputType and OutputType expose the DTO types for Step 3's type-safe
// dispatcher registration without exposing mutable Definition internals.
func (d Definition) InputType() reflect.Type { return d.inputType }

// OutputType reports the concrete semantic result DTO type.
func (d Definition) OutputType() reflect.Type { return d.outputType }

func define[I Input, O Output](metadata Metadata) (Definition, error) {
	if err := ValidateMetadata(metadata); err != nil {
		return Definition{}, err
	}
	inputType := reflect.TypeOf((*I)(nil)).Elem()
	outputType := reflect.TypeOf((*O)(nil)).Elem()
	if err := validateTransportType(inputType, "input"); err != nil {
		return Definition{}, err
	}
	if err := validateTransportType(outputType, "output"); err != nil {
		return Definition{}, err
	}
	return Definition{metadata: cloneMetadata(metadata), inputType: inputType, outputType: outputType}, nil
}

func mustDefine[I Input, O Output](metadata Metadata) Definition {
	d, err := define[I, O](metadata)
	if err != nil {
		panic(err)
	}
	return d
}

// ValidateMetadata rejects omitted dimensions and illegal combinations.
func ValidateMetadata(m Metadata) error {
	if err := m.Operation.Validate(); err != nil {
		return err
	}
	switch m.Access {
	case AccessRead, AccessMutation:
	default:
		return fmt.Errorf("%s: access is incomplete or unknown", m.Operation)
	}
	switch m.Delivery {
	case DeliveryNone, DeliveryAuthority, DeliveryClaim, DeliveryProvisional, DeliveryCapture, DeliveryEnvironment:
	default:
		return fmt.Errorf("%s: delivery class is incomplete or unknown", m.Operation)
	}
	if m.RequiredContext == nil {
		return fmt.Errorf("%s: required context metadata is omitted", m.Operation)
	}
	if err := validateContextDimensions(m.RequiredContext); err != nil {
		return fmt.Errorf("%s: %w", m.Operation, err)
	}
	if m.Guards == nil {
		return fmt.Errorf("%s: guard footprint metadata is omitted", m.Operation)
	}
	if err := validateFootprints("guard", m.Guards); err != nil {
		return fmt.Errorf("%s: %w", m.Operation, err)
	}
	if m.Writes == nil {
		return fmt.Errorf("%s: write footprint metadata is omitted", m.Operation)
	}
	if err := validateFootprints("write", m.Writes); err != nil {
		return fmt.Errorf("%s: %w", m.Operation, err)
	}
	if m.BlobInputs == nil {
		return fmt.Errorf("%s: blob input metadata is omitted", m.Operation)
	}
	if err := validateBlobSpecs(m.BlobInputs); err != nil {
		return fmt.Errorf("%s: %w", m.Operation, err)
	}
	switch m.Claim {
	case ClaimNone, ClaimExact:
	default:
		return fmt.Errorf("%s: claim requirement is incomplete or unknown", m.Operation)
	}
	if m.ExternalEffects == nil {
		return fmt.Errorf("%s: external side-effect metadata is omitted", m.Operation)
	}
	if err := validateExternalEffects(m.ExternalEffects); err != nil {
		return fmt.Errorf("%s: %w", m.Operation, err)
	}

	if m.Access == AccessRead {
		if m.Delivery != DeliveryNone {
			return fmt.Errorf("%s: a read must use delivery class none", m.Operation)
		}
		if len(m.Writes) != 0 {
			return fmt.Errorf("%s: a read cannot declare a write footprint", m.Operation)
		}
		if m.Claim != ClaimNone {
			return fmt.Errorf("%s: a read cannot require a mutation claim", m.Operation)
		}
	} else {
		if m.Delivery == DeliveryNone {
			return fmt.Errorf("%s: a mutation must declare a delivery class", m.Operation)
		}
		if len(m.Writes) == 0 {
			return fmt.Errorf("%s: a mutation must declare at least one write footprint", m.Operation)
		}
	}
	if m.Delivery == DeliveryClaim && m.Claim != ClaimExact {
		return fmt.Errorf("%s: claim delivery requires an exact claim", m.Operation)
	}
	return nil
}

func validateContextDimensions(dimensions []ContextDimension) error {
	seen := map[ContextDimension]bool{}
	for _, dimension := range dimensions {
		switch dimension {
		case ContextRepo, ContextClone, ContextWorktree:
		default:
			return fmt.Errorf("required context dimension %q is unknown", dimension)
		}
		if seen[dimension] {
			return fmt.Errorf("required context dimension %q is duplicated", dimension)
		}
		seen[dimension] = true
	}
	return nil
}

func validateFootprints(kind string, footprints []Footprint) error {
	seen := map[Footprint]bool{}
	for _, footprint := range footprints {
		if !tokenPattern.MatchString(string(footprint)) {
			return fmt.Errorf("%s footprint %q is malformed", kind, footprint)
		}
		if seen[footprint] {
			return fmt.Errorf("%s footprint %q is duplicated", kind, footprint)
		}
		seen[footprint] = true
	}
	return nil
}

func validateBlobSpecs(specs []BlobSpec) error {
	seen := map[string]bool{}
	for _, spec := range specs {
		if !tokenPattern.MatchString(spec.Name) {
			return fmt.Errorf("blob input name %q is malformed", spec.Name)
		}
		if seen[spec.Name] {
			return fmt.Errorf("blob input %q is duplicated", spec.Name)
		}
		seen[spec.Name] = true
	}
	return nil
}

func validateExternalEffects(effects []ExternalEffect) error {
	seen := map[ExternalEffect]bool{}
	for _, effect := range effects {
		switch effect {
		case EffectFilesystemRead, EffectFilesystemWrite, EffectGitRead,
			EffectRuntimeLock, EffectTrackerRead, EffectTrackerWrite, EffectAgentHook:
		default:
			return fmt.Errorf("external side effect %q is unknown", effect)
		}
		if seen[effect] {
			return fmt.Errorf("external side effect %q is duplicated", effect)
		}
		seen[effect] = true
	}
	return nil
}

func cloneMetadata(m Metadata) Metadata {
	m.RequiredContext = cloneSlice(m.RequiredContext)
	m.Guards = cloneSlice(m.Guards)
	m.Writes = cloneSlice(m.Writes)
	m.BlobInputs = cloneSlice(m.BlobInputs)
	m.ExternalEffects = cloneSlice(m.ExternalEffects)
	return m
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func validateTransportType(root reflect.Type, role string) error {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string) error
	walk = func(t reflect.Type, path string) error {
		if seen[t] {
			return nil
		}
		seen[t] = true
		if pkg := t.PkgPath(); pkg != "" && pkg != packagePath {
			return fmt.Errorf("%s %s uses transport-coupled type %s from %s", role, path, t, pkg)
		}
		switch t.Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			return nil
		case reflect.Pointer, reflect.Slice, reflect.Array:
			if role == "input" && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t.Elem().Kind() == reflect.Uint8 {
				return fmt.Errorf("input %s carries raw bytes; declare a staged BlobInput instead", path)
			}
			return walk(t.Elem(), path+" element")
		case reflect.Map:
			if t.Key().Kind() != reflect.String {
				return fmt.Errorf("%s %s has non-string map key %s", role, path, t.Key())
			}
			return walk(t.Elem(), path+" value")
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				if !field.IsExported() {
					return fmt.Errorf("%s %s.%s is not exported", role, path, field.Name)
				}
				if err := walk(field.Type, path+"."+field.Name); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("%s %s uses non-representable %s", role, path, t.Kind())
		}
	}
	return walk(root, root.Name())
}

func validActor(actor Actor) bool {
	value := string(actor)
	if value == "human" {
		return true
	}
	for _, prefix := range []string{"role:", "system:"} {
		if strings.HasPrefix(value, prefix) {
			name := strings.TrimPrefix(value, prefix)
			return name != "" && !strings.ContainsAny(name, " \t\r\n")
		}
	}
	return false
}
