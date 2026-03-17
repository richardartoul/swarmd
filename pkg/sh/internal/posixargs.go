// See LICENSE for licensing information

package internal

import "strings"

// OptionValueMode controls whether an option accepts a value.
type OptionValueMode uint8

const (
	NoOptionValue OptionValueMode = iota
	RequiredOptionValue
)

// OptionSpec describes a supported option spelling.
type OptionSpec struct {
	Canonical string
	Names     []string
	ValueMode OptionValueMode
}

// ParsedOption records one parsed option occurrence.
type ParsedOption struct {
	Canonical string
	Name      string
	Value     string
}

// ParseOptionsConfig controls the generic POSIX-style option parser.
type ParseOptionsConfig struct {
	// StopAtOperand stops parsing options after the first non-option operand.
	// This matches the default POSIX utility syntax rules.
	StopAtOperand bool
}

// UnknownOptionError reports an unsupported option name.
type UnknownOptionError struct {
	Option string
}

func (e *UnknownOptionError) Error() string {
	return "unknown option: " + e.Option
}

// MissingOptionValueError reports an option that requires a value but was not
// followed by one.
type MissingOptionValueError struct {
	Option string
}

func (e *MissingOptionValueError) Error() string {
	return "missing option value: " + e.Option
}

// ParseOptions parses POSIX-style options, including short-option clusters,
// attached short values like -n5, attached long values like --name=value, and
// the end-of-options marker "--".
func ParseOptions(args []string, specs []OptionSpec, cfg ParseOptionsConfig) ([]ParsedOption, []string, error) {
	specByName := make(map[string]OptionSpec, len(specs)*2)
	for _, spec := range specs {
		for _, name := range spec.Names {
			specByName[name] = spec
		}
	}

	var (
		parsed   []ParsedOption
		operands []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			return parsed, operands, nil
		}
		if !isOptionToken(arg) {
			if cfg.StopAtOperand {
				operands = append(operands, args[i:]...)
				return parsed, operands, nil
			}
			operands = append(operands, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := strings.Cut(arg, "=")
			spec, ok := specByName[name]
			if !ok {
				return nil, nil, &UnknownOptionError{Option: name}
			}
			switch spec.ValueMode {
			case NoOptionValue:
				if hasValue {
					return nil, nil, &UnknownOptionError{Option: name}
				}
				parsed = append(parsed, ParsedOption{
					Canonical: spec.Canonical,
					Name:      name,
				})
			case RequiredOptionValue:
				if !hasValue {
					if i+1 >= len(args) {
						return nil, nil, &MissingOptionValueError{Option: name}
					}
					i++
					value = args[i]
				}
				parsed = append(parsed, ParsedOption{
					Canonical: spec.Canonical,
					Name:      name,
					Value:     value,
				})
			}
			continue
		}

		prefix := arg[:1]
		for j := 1; j < len(arg); j++ {
			name := prefix + arg[j:j+1]
			spec, ok := specByName[name]
			if !ok {
				return nil, nil, &UnknownOptionError{Option: name}
			}
			switch spec.ValueMode {
			case NoOptionValue:
				parsed = append(parsed, ParsedOption{
					Canonical: spec.Canonical,
					Name:      name,
				})
			case RequiredOptionValue:
				value := ""
				if j+1 < len(arg) {
					value = arg[j+1:]
				} else {
					if i+1 >= len(args) {
						return nil, nil, &MissingOptionValueError{Option: name}
					}
					i++
					value = args[i]
				}
				parsed = append(parsed, ParsedOption{
					Canonical: spec.Canonical,
					Name:      name,
					Value:     value,
				})
				j = len(arg)
			}
		}
	}
	return parsed, operands, nil
}

func isOptionToken(arg string) bool {
	if len(arg) < 2 {
		return false
	}
	if arg == "-" || arg == "+" {
		return false
	}
	return arg[0] == '-' || arg[0] == '+'
}
