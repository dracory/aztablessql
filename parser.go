package aztablessql

import (
	"fmt"
	"regexp"
	"strings"
)

type queryType int

const (
	qSelect queryType = iota
	qInsert
	qDelete
)

type whereCond struct {
	column        string
	isPlaceholder bool
	value         string // literal value, only set when !isPlaceholder
}

type parsedQuery struct {
	kind            queryType
	table           string
	columns         []string // INSERT: column names; SELECT: explicit selected columns
	allColumns      bool     // SELECT *
	where           []whereCond
	numPlaceholders int
}

var (
	insertRe = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+([A-Za-z0-9_]+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)\s*;?\s*$`)
	deleteRe = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*;?\s*$`)
	selectRe = regexp.MustCompile(`(?is)^SELECT\s+(.+?)\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*;?\s*$`)
	condRe   = regexp.MustCompile(`(?i)^([A-Za-z0-9_]+)\s*=\s*(\?|'[^']*'|"[^"]*")$`)
	andRe    = regexp.MustCompile(`(?i)\s+AND\s+`)
)

func parseQuery(query string) (*parsedQuery, error) {
	q := strings.TrimSpace(query)

	if m := insertRe.FindStringSubmatch(q); m != nil {
		table := m[1]
		cols := splitAndTrim(m[2], ",")
		vals := splitAndTrim(m[3], ",")
		if len(cols) != len(vals) {
			return nil, fmt.Errorf("aztablessql: INSERT column/value count mismatch")
		}
		for _, v := range vals {
			if v != "?" {
				return nil, fmt.Errorf("aztablessql: INSERT only supports placeholder values (?), got %q", v)
			}
		}
		return &parsedQuery{kind: qInsert, table: table, columns: cols, numPlaceholders: len(cols)}, nil
	}

	if m := deleteRe.FindStringSubmatch(q); m != nil {
		table := m[1]
		conds, n, err := parseWhere(m[2])
		if err != nil {
			return nil, err
		}
		return &parsedQuery{kind: qDelete, table: table, where: conds, numPlaceholders: n}, nil
	}

	if m := selectRe.FindStringSubmatch(q); m != nil {
		colsStr := strings.TrimSpace(m[1])
		table := m[2]
		conds, n, err := parseWhere(m[3])
		if err != nil {
			return nil, err
		}
		pq := &parsedQuery{kind: qSelect, table: table, where: conds, numPlaceholders: n}
		if colsStr == "*" {
			pq.allColumns = true
		} else {
			pq.columns = splitAndTrim(colsStr, ",")
		}
		return pq, nil
	}

	return nil, fmt.Errorf("aztablessql: unsupported query: %q", q)
}

func parseWhere(whereStr string) ([]whereCond, int, error) {
	whereStr = strings.TrimSpace(whereStr)
	if whereStr == "" {
		return nil, 0, nil
	}
	parts := andRe.Split(whereStr, -1)
	var conds []whereCond
	n := 0
	for _, p := range parts {
		m := condRe.FindStringSubmatch(strings.TrimSpace(p))
		if m == nil {
			return nil, 0, fmt.Errorf("aztablessql: unsupported WHERE condition: %q", p)
		}
		col, tok := m[1], m[2]
		if tok == "?" {
			conds = append(conds, whereCond{column: col, isPlaceholder: true})
			n++
		} else {
			conds = append(conds, whereCond{column: col, value: strings.Trim(tok, `'"`)})
		}
	}
	return conds, n, nil
}

func splitAndTrim(s, sep string) []string {
	raw := strings.Split(s, sep)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		t := strings.Trim(strings.TrimSpace(r), `'"`)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
