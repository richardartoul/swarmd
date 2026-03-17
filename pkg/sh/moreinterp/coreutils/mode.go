package coreutils

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type modeWho uint8

const (
	modeWhoUser modeWho = 1 << iota
	modeWhoGroup
	modeWhoOther
	modeWhoAll = modeWhoUser | modeWhoGroup | modeWhoOther
)

type modeExpression struct {
	absolute bool
	bits     uint32
	clauses  []modeClause
}

type modeClause struct {
	who   modeWho
	op    byte
	perms string
}

func parseModeExpression(mode string) (modeExpression, error) {
	if mode == "" {
		return modeExpression{}, nil
	}
	if isOctalMode(mode) {
		parsed, err := strconv.ParseUint(mode, 8, 32)
		if err != nil || parsed > 0o7777 {
			return modeExpression{}, fmt.Errorf("invalid mode %q", mode)
		}
		return modeExpression{
			absolute: true,
			bits:     uint32(parsed),
		}, nil
	}

	rawClauses := strings.Split(mode, ",")
	clauses := make([]modeClause, 0, len(rawClauses))
	for _, raw := range rawClauses {
		if raw == "" {
			return modeExpression{}, fmt.Errorf("invalid mode %q", mode)
		}
		clause, err := parseModeClause(raw)
		if err != nil {
			return modeExpression{}, fmt.Errorf("invalid mode %q", mode)
		}
		clauses = append(clauses, clause)
	}
	return modeExpression{clauses: clauses}, nil
}

func isOctalMode(mode string) bool {
	if mode == "" {
		return false
	}
	for _, r := range mode {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

func parseModeClause(raw string) (modeClause, error) {
	var clause modeClause
	i := 0
	for i < len(raw) {
		switch raw[i] {
		case 'u':
			clause.who |= modeWhoUser
		case 'g':
			clause.who |= modeWhoGroup
		case 'o':
			clause.who |= modeWhoOther
		case 'a':
			clause.who = modeWhoAll
		default:
			goto op
		}
		i++
	}
op:
	if i >= len(raw) {
		return modeClause{}, fmt.Errorf("missing operator")
	}
	switch raw[i] {
	case '+', '-', '=':
		clause.op = raw[i]
	default:
		return modeClause{}, fmt.Errorf("invalid operator")
	}
	i++
	clause.perms = raw[i:]
	for _, r := range clause.perms {
		switch r {
		case 'r', 'w', 'x', 'X', 's', 't', 'u', 'g', 'o':
		default:
			return modeClause{}, fmt.Errorf("invalid permission")
		}
	}
	return clause, nil
}

func (expr modeExpression) Apply(current os.FileMode, isDir bool) os.FileMode {
	if expr.absolute {
		return modeFromBits(current, expr.bits)
	}
	bits := modeBits(current)
	for _, clause := range expr.clauses {
		who := clause.who
		if who == 0 {
			who = modeWhoAll
		}
		setBits := clauseBits(bits, who, clause.perms, isDir)
		switch clause.op {
		case '+':
			bits |= setBits
		case '-':
			bits &^= setBits
		case '=':
			bits &^= classBits(who)
			bits |= setBits
		}
	}
	return modeFromBits(current, bits)
}

func modeBits(mode os.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

func modeFromBits(base os.FileMode, bits uint32) os.FileMode {
	mode := base &^ (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	mode |= os.FileMode(bits & 0o777)
	if bits&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if bits&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if bits&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode
}

func classBits(who modeWho) uint32 {
	var bits uint32
	if who&modeWhoUser != 0 {
		bits |= 0o4700
	}
	if who&modeWhoGroup != 0 {
		bits |= 0o2070
	}
	if who&modeWhoOther != 0 {
		bits |= 0o1007
	}
	return bits
}

func clauseBits(current uint32, who modeWho, perms string, isDir bool) uint32 {
	var bits uint32
	for _, r := range perms {
		switch r {
		case 'r':
			if who&modeWhoUser != 0 {
				bits |= 0o400
			}
			if who&modeWhoGroup != 0 {
				bits |= 0o040
			}
			if who&modeWhoOther != 0 {
				bits |= 0o004
			}
		case 'w':
			if who&modeWhoUser != 0 {
				bits |= 0o200
			}
			if who&modeWhoGroup != 0 {
				bits |= 0o020
			}
			if who&modeWhoOther != 0 {
				bits |= 0o002
			}
		case 'x':
			bits |= executeBits(who)
		case 'X':
			if isDir || current&0o111 != 0 {
				bits |= executeBits(who)
			}
		case 's':
			if who&modeWhoUser != 0 {
				bits |= 0o4000
			}
			if who&modeWhoGroup != 0 {
				bits |= 0o2000
			}
		case 't':
			if who&modeWhoOther != 0 || who == modeWhoAll {
				bits |= 0o1000
			}
		case 'u':
			bits |= copyBits(who, (current>>6)&0o7)
		case 'g':
			bits |= copyBits(who, (current>>3)&0o7)
		case 'o':
			bits |= copyBits(who, current&0o7)
		}
	}
	return bits
}

func executeBits(who modeWho) uint32 {
	var bits uint32
	if who&modeWhoUser != 0 {
		bits |= 0o100
	}
	if who&modeWhoGroup != 0 {
		bits |= 0o010
	}
	if who&modeWhoOther != 0 {
		bits |= 0o001
	}
	return bits
}

func copyBits(who modeWho, src uint32) uint32 {
	var bits uint32
	if who&modeWhoUser != 0 {
		bits |= src << 6
	}
	if who&modeWhoGroup != 0 {
		bits |= src << 3
	}
	if who&modeWhoOther != 0 {
		bits |= src
	}
	return bits
}
