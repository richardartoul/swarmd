// See LICENSE for licensing information

package agent

import (
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func validateProgram(node syntax.Node) error {
	var validateErr error
	syntax.Walk(node, func(node syntax.Node) bool {
		if validateErr != nil {
			return false
		}

		switch node := node.(type) {
		case *syntax.Stmt:
			switch {
			case node.Background:
				validateErr = fmt.Errorf("background jobs are not allowed in agent shell steps")
				return false
			case node.Coprocess:
				validateErr = fmt.Errorf("coprocesses are not allowed in agent shell steps")
				return false
			}
		case *syntax.CallExpr:
			name, ok := literalCommandName(node)
			if !ok {
				return true
			}
			switch name {
			case "exit":
				validateErr = fmt.Errorf("exit is not allowed in agent shell steps; use Finish instead")
				return false
			case "exec":
				validateErr = fmt.Errorf("exec is not allowed in agent shell steps")
				return false
			}
			if err := validateNestedLiteralShell(node, name); err != nil {
				validateErr = err
				return false
			}
		case *syntax.ProcSubst:
			validateErr = fmt.Errorf("process substitution is not allowed in agent shell steps")
			return false
		case *syntax.CoprocClause:
			validateErr = fmt.Errorf("coprocess clauses are not allowed in agent shell steps")
			return false
		}

		return true
	})
	return validateErr
}

func literalCommandName(call *syntax.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	return literalWordValue(call.Args[0])
}

func validateNestedLiteralShell(call *syntax.CallExpr, name string) error {
	if name != "sh" || len(call.Args) < 3 {
		return nil
	}
	mode, ok := literalWordValue(call.Args[1])
	if !ok || mode != "-c" {
		return nil
	}
	src, ok := literalWordValue(call.Args[2])
	if !ok {
		return nil
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), name)
	if err != nil {
		return nil
	}
	return validateProgram(file)
}

func literalWordValue(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			value, ok := literalDoubleQuotedValue(part)
			if !ok {
				return "", false
			}
			b.WriteString(value)
		default:
			return "", false
		}
	}
	return b.String(), true
}

func literalDoubleQuotedValue(quoted *syntax.DblQuoted) (string, bool) {
	var b strings.Builder
	for _, part := range quoted.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		b.WriteString(lit.Value)
	}
	return b.String(), true
}
