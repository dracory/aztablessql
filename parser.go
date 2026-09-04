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
	qUpdate
)

type whereCond struct {
	column        string
	isPlaceholder bool
	value         string // literal value, only set when !isPlaceholder
}

type setAssign struct {
	column        string
	isPlaceholder bool
	value         string // literal value, only set when !isPlaceholder
}

type parsedQuery struct {
	kind              queryType
	table             string
	columns           []string // INSERT: column names; SELECT: explicit selected columns
	allColumns        bool     // SELECT *
	set               []setAssign
	where             []whereCond
	numPlaceholders   int // total, in argument order: SET placeholders then WHERE placeholders
	setPlaceholders   int
	wherePlaceholders int
}

var (
	insertRe = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+([A-Za-z0-9_]+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)\s*;?\s*$`)
	deleteRe = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*;?\s*$`)
	selectRe = regexp.MustCompile(`(?is)^SELECT\s+(.+?)\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*;?\s*$`)
	// updateNoWhereRe matches UPDATE without a WHERE clause so we can give a
	// clearer error than the generic "unsupported query" fallback.
	updateNoWhereRe = regexp.MustCompile(`(?is)^UPDATE\s+([A-Za-z0-9_]+)\s+SET\s+(.+?)\s*;?\s*$`)
	updateRe        = regexp.MustCompile(`(?is)^UPDATE\s+([A-Za-z0-9_]+)\s+SET\s+(.+?)\s+WHERE\s+(.+?)\s*;?\s*$`)
	condRe          = regexp.MustCompile(`(?i)^([A-Za-z0-9_]+)\s*=\s*(\?|'[^']*'|"[^"]*")$`)
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

	if m := updateRe.FindStringSubmatch(q); m != nil {
		table := m[1]
		assigns, setN, err := parseSet(m[2])
		if err != nil {
			return nil, err
		}
		conds, whereN, err := parseWhere(m[3])
		if err != nil {
			return nil, err
		}
		if err := validateUpdateWhere(conds); err != nil {
			return nil, err
		}
		return &parsedQuery{
			kind:              qUpdate,
			table:             table,
			set:               assigns,
			where:             conds,
			numPlaceholders:   setN + whereN,
			setPlaceholders:   setN,
			wherePlaceholders: whereN,
		}, nil
	}

	// Check for UPDATE without WHERE before falling through to the generic
	// error, so the message is more helpful.
	if updateNoWhereRe.MatchString(q) {
		return nil, fmt.Errorf("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ?")
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

func parseSet(setStr string) ([]setAssign, int, error) {
	parts := splitOnComma(setStr)
	var assigns []setAssign
	n := 0
	for _, p := range parts {
		m := condRe.FindStringSubmatch(strings.TrimSpace(p))
		if m == nil {
			return nil, 0, fmt.Errorf("aztablessql: unsupported SET clause: %q", p)
		}
		col, tok := m[1], m[2]
		if strings.EqualFold(col, "PartitionKey") || strings.EqualFold(col, "RowKey") {
			return nil, 0, fmt.Errorf("aztablessql: cannot SET PartitionKey/RowKey")
		}
		if tok == "?" {
			assigns = append(assigns, setAssign{column: col, isPlaceholder: true})
			n++
		} else {
			assigns = append(assigns, setAssign{column: col, value: strings.Trim(tok, `'"`)})
		}
	}
	if len(assigns) == 0 {
		return nil, 0, fmt.Errorf("aztablessql: UPDATE requires at least one SET assignment")
	}
	return assigns, n, nil
}

func validateUpdateWhere(conds []whereCond) error {
	var hasPK, hasRK bool
	for _, c := range conds {
		switch strings.ToLower(c.column) {
		case "partitionkey":
			hasPK = true
		case "rowkey":
			hasRK = true
		}
	}
	if !hasPK || !hasRK || len(conds) != 2 {
		return fmt.Errorf("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ? (exactly)")
	}
	return nil
}

func parseWhere(whereStr string) ([]whereCond, int, error) {
	whereStr = strings.TrimSpace(whereStr)
	if whereStr == "" {
		return nil, 0, nil
	}
	parts := splitOnAND(whereStr)
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

// splitOnComma splits a string on commas, but respects single-quoted
// literals so that a comma inside 'Doe, Jr' does not cause a split.
func splitOnComma(s string) []string {
	return splitQuoteAware(s, ',')
}

// splitOnAND splits a string on the keyword AND (case-insensitive, surrounded
// by whitespace), but respects single-quoted literals so that AND inside
// 'A and B' does not cause a split.
func splitOnAND(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false

	s = strings.TrimSpace(s)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' {
			inQuote = !inQuote
			current.WriteByte(ch)
			continue
		}
		if !inQuote {
			// Check for " AND " or " and " (case-insensitive) surrounded by whitespace.
			if i > 0 && current.Len() > 0 && isANDAt(s, i) {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
				i += 4 // skip "AND " (the leading space was already consumed)
				// skip trailing space after AND
				for i < len(s) && s[i] == ' ' {
					i++
				}
				i-- // compensate for loop increment
				continue
			}
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, strings.TrimSpace(current.String()))
	}
	return parts
}

// isANDAt checks whether s[pos:] starts with "AND " or "AND" at end,
// case-insensitively, preceded by whitespace (already checked by caller).
func isANDAt(s string, pos int) bool {
	// Need at least "AND" (3 chars) and the char before pos must be a space
	// (guaranteed by caller context). We check for "AND" followed by space or end.
	if pos+3 > len(s) {
		return false
	}
	// Check the char before is whitespace
	if pos == 0 || s[pos-1] != ' ' {
		return false
	}
	rest := s[pos : pos+3]
	if !strings.EqualFold(rest, "AND") {
		return false
	}
	// Must be followed by whitespace or end of string
	if pos+3 == len(s) {
		return true
	}
	return s[pos+3] == ' ' || s[pos+3] == '\t'
}

// splitQuoteAware splits on a single-byte delimiter while respecting
// single-quoted literals. The delimiter inside quotes is preserved.
func splitQuoteAware(s string, delim byte) []string {
	var parts []string
	var current strings.Builder
	inQuote := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' {
			inQuote = !inQuote
			current.WriteByte(ch)
			continue
		}
		if ch == delim && !inQuote {
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, strings.TrimSpace(current.String()))
	}
	return parts
}
