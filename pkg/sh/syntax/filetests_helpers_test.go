// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"reflect"
	"strings"
)

func lit(s string) *Lit { return &Lit{Value: s} }
func lits(strs ...string) []*Lit {
	l := make([]*Lit, 0, len(strs))
	for _, s := range strs {
		l = append(l, lit(s))
	}
	return l
}
func word(ps ...WordPart) *Word { return &Word{Parts: ps} }
func litWord(s string) *Word    { return word(lit(s)) }
func litWords(strs ...string) []*Word {
	l := make([]*Word, 0, len(strs))
	for _, s := range strs {
		l = append(l, litWord(s))
	}
	return l
}

func litAssigns(pairs ...string) []*Assign {
	l := make([]*Assign, len(pairs))
	for i, pair := range pairs {
		name, val, ok := strings.Cut(pair, "=")
		if !ok {
			l[i] = &Assign{Naked: true, Name: lit(name)}
		} else if val == "" {
			l[i] = &Assign{Name: lit(name)}
		} else {
			l[i] = &Assign{Name: lit(name), Value: litWord(val)}
		}
	}
	return l
}

func call(words ...*Word) *CallExpr    { return &CallExpr{Args: words} }
func litCall(strs ...string) *CallExpr { return call(litWords(strs...)...) }

func stmt(cmd Command) *Stmt { return &Stmt{Cmd: cmd} }
func stmts(cmds ...Command) []*Stmt {
	l := make([]*Stmt, len(cmds))
	for i, cmd := range cmds {
		l[i] = stmt(cmd)
	}
	return l
}

func litStmt(strs ...string) *Stmt { return stmt(litCall(strs...)) }
func litStmts(strs ...string) []*Stmt {
	l := make([]*Stmt, len(strs))
	for i, s := range strs {
		l[i] = litStmt(s)
	}
	return l
}

func sglQuoted(s string) *SglQuoted        { return &SglQuoted{Value: s} }
func sglDQuoted(s string) *SglQuoted       { return &SglQuoted{Dollar: true, Value: s} }
func dblQuoted(ps ...WordPart) *DblQuoted  { return &DblQuoted{Parts: ps} }
func dblDQuoted(ps ...WordPart) *DblQuoted { return &DblQuoted{Dollar: true, Parts: ps} }
func block(sts ...*Stmt) *Block            { return &Block{Stmts: sts} }
func subshell(sts ...*Stmt) *Subshell      { return &Subshell{Stmts: sts} }
func arithmExp(e ArithmExpr) *ArithmExp    { return &ArithmExp{X: e} }
func arithmExpBr(e ArithmExpr) *ArithmExp  { return &ArithmExp{Bracket: true, X: e} }
func arithmCmd(e ArithmExpr) *ArithmCmd    { return &ArithmCmd{X: e} }
func parenArit(e ArithmExpr) *ParenArithm  { return &ParenArithm{X: e} }
func parenTest(e TestExpr) *ParenTest      { return &ParenTest{X: e} }

func cmdSubst(sts ...*Stmt) *CmdSubst { return &CmdSubst{Stmts: sts} }
func litParamExp(s string) *ParamExp {
	return &ParamExp{Short: true, Param: lit(s)}
}

func letClause(exps ...ArithmExpr) *LetClause {
	return &LetClause{Exprs: exps}
}

func arrValues(words ...*Word) *ArrayExpr {
	ae := &ArrayExpr{}
	for _, w := range words {
		ae.Elems = append(ae.Elems, &ArrayElem{Value: w})
	}
	return ae
}

func fullProg(v any) *File {
	f := &File{}
	switch v := v.(type) {
	case *File:
		return v
	case []*Stmt:
		f.Stmts = v
		return f
	case *Stmt:
		f.Stmts = append(f.Stmts, v)
		return f
	case []Command:
		for _, cmd := range v {
			f.Stmts = append(f.Stmts, stmt(cmd))
		}
		return f
	case *Word:
		return fullProg(call(v))
	case WordPart:
		return fullProg(word(v))
	case Command:
		return fullProg(stmt(v))
	case nil:
	default:
		panic(reflect.TypeOf(v))
	}
	return nil
}

func flipConfirm2(langSet LangVariant) func(*fileTestCase) {
	return func(c *fileTestCase) { c.flipConfirmSet = langSet }
}

func (c *fileTestCase) setForLangs(val any, langSets ...LangVariant) {
	// The parameter is a slice to allow omitting the argument.
	switch len(langSets) {
	case 0:
		for i := range c.byLangIndex {
			c.byLangIndex[i] = val
		}
		return
	case 1:
		for lang := range langSets[0].bits() {
			c.byLangIndex[lang.index()] = val
		}
	default:
		panic("use a LangVariant bitset")
	}
}

func fileTest(in []string, opts ...func(*fileTestCase)) fileTestCase {
	c := fileTestCase{inputs: in}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func langSkip(langSets ...LangVariant) func(*fileTestCase) {
	return func(c *fileTestCase) { c.setForLangs(nil, langSets...) }
}

func langFile(wantNode any, langSets ...LangVariant) func(*fileTestCase) {
	return func(c *fileTestCase) {
		c.setForLangs(fullProg(wantNode), langSets...)
	}
}

func langErr2(wantErr string, langSets ...LangVariant) func(*fileTestCase) {
	return func(c *fileTestCase) { c.setForLangs(wantErr, langSets...) }
}
