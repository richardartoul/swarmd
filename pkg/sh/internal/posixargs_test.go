package internal

import (
	"reflect"
	"testing"
)

func TestParseOptionsInterspersed(t *testing.T) {
	t.Parallel()

	opts, operands, err := ParseOptions([]string{
		"first.txt",
		"-a",
		"--output", "result.txt",
		"second.txt",
	}, []OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
		{Canonical: "output", Names: []string{"--output"}, ValueMode: RequiredOptionValue},
	}, ParseOptionsConfig{})
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}

	wantOpts := []ParsedOption{
		{Canonical: "a", Name: "-a"},
		{Canonical: "output", Name: "--output", Value: "result.txt"},
	}
	if !reflect.DeepEqual(opts, wantOpts) {
		t.Fatalf("opts = %#v, want %#v", opts, wantOpts)
	}

	wantOperands := []string{"first.txt", "second.txt"}
	if !reflect.DeepEqual(operands, wantOperands) {
		t.Fatalf("operands = %#v, want %#v", operands, wantOperands)
	}
}

func TestParseOptionsStopAtOperand(t *testing.T) {
	t.Parallel()

	opts, operands, err := ParseOptions([]string{
		"first.txt",
		"-a",
		"second.txt",
	}, []OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
	}, ParseOptionsConfig{
		StopAtOperand: true,
	})
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("opts = %#v, want no parsed options", opts)
	}

	wantOperands := []string{"first.txt", "-a", "second.txt"}
	if !reflect.DeepEqual(operands, wantOperands) {
		t.Fatalf("operands = %#v, want %#v", operands, wantOperands)
	}
}

func TestParseOptionsStopsAtDoubleDash(t *testing.T) {
	t.Parallel()

	opts, operands, err := ParseOptions([]string{
		"-a",
		"--",
		"-a",
		"--output",
		"value.txt",
	}, []OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
		{Canonical: "output", Names: []string{"--output"}, ValueMode: RequiredOptionValue},
	}, ParseOptionsConfig{})
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}

	wantOpts := []ParsedOption{
		{Canonical: "a", Name: "-a"},
	}
	if !reflect.DeepEqual(opts, wantOpts) {
		t.Fatalf("opts = %#v, want %#v", opts, wantOpts)
	}

	wantOperands := []string{"-a", "--output", "value.txt"}
	if !reflect.DeepEqual(operands, wantOperands) {
		t.Fatalf("operands = %#v, want %#v", operands, wantOperands)
	}
}
