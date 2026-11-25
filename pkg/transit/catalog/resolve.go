package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ErrUnknownSystem is returned, wrapped, for a system ID not in the catalog.
var ErrUnknownSystem = errors.New("unknown system")

// MissingOptionsError reports required settings, such as API keys, that a
// system's provider block leaves out.
type MissingOptionsError struct {
	System  string
	Options []string
}

func (e *MissingOptionsError) Error() string {
	return fmt.Sprintf("system %q needs %s", e.System, strings.Join(e.Options, " and "))
}

// EnvName is the environment variable that conventionally holds option for
// this system, such as LISBON_METRO_CLIENT_ID.
func (s System) EnvName(option string) string {
	return strings.ToUpper(strings.ReplaceAll(s.ID+"_"+option, "-", "_"))
}

// Resolve works out a provider block's type and options:
//   - `system = "ID"` takes the type and settings of a catalog system,
//     overlaid with the block's other keys;
//   - otherwise `type = "..."` names the type;
//   - otherwise the block's name is the type.
func Resolve(name string, settings map[string]any) (typ string, opts Options, err error) {
	user := maps.Clone(settings)
	if user == nil {
		user = map[string]any{}
	}
	sysID, hasSystem := user["system"]
	typeName, hasType := user["type"]
	delete(user, "system")
	delete(user, "type")

	switch {
	case hasSystem && hasType:
		return "", nil, errors.New("set either system or type, not both")
	case hasSystem:
		id, _ := sysID.(string)
		sys, ok := Get(id)
		if !ok {
			return "", nil, fmt.Errorf("%w %q", ErrUnknownSystem, id)
		}
		merged := sys.Merge(user)
		if missing := sys.Missing(merged); len(missing) > 0 {
			return "", nil, &MissingOptionsError{System: id, Options: missing}
		}
		return sys.Provider, merged, nil
	case hasType:
		t, _ := typeName.(string)
		if t == "" {
			return "", nil, errors.New("type must be a non-empty string")
		}
		return t, user, nil
	default:
		return name, user, nil
	}
}

// Options are one provider block's settings. They decode strictly, so a
// mistyped key is an error.
type Options map[string]any

// Decode decodes the options into v, which is usually a provider's Config.
func (o Options) Decode(v any) error {
	b, err := toml.Marshal(map[string]any(o))
	if err != nil {
		return fmt.Errorf("re-encode options: %w", err)
	}
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			// Only key names: strict.String() quotes the offending lines,
			// whose values can hold secrets read from the environment.
			keys := make([]string, len(strict.Errors))
			for i, e := range strict.Errors {
				keys[i] = strings.Join(e.Key(), ".")
			}
			return fmt.Errorf("unknown setting %s", strings.Join(keys, ", "))
		}
		return err
	}
	return nil
}
