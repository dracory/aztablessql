package aztablessql

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type queryType int

const (
	qSelect queryType = iota
	qInsert
	qDelete
	qUpdate
)

// upsertMode distinguishes the three INSERT-family execution strategies.
//   - upsertNone    : plain INSERT  → client.AddEntity (fails on existing entity)
//   - upsertReplace : INSERT OR REPLACE / UPSERT INTO → UpsertEntity(Replace)
//   - upsertMerge   : INSERT OR MERGE → UpsertEntity(Merge)
type upsertMode int

const (
	upsertNone upsertMode = iota
	upsertReplace
	upsertMerge
)

type whereCond struct {
	column        string
	op            string // "=", "!=", ">", ">=", "<", "<=" (canonical; "<>" normalized to "!=")
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
	limit             int // SELECT only: LIMIT <n> (0 = no limit); ≥1 when set
	numPlaceholders   int // total, in argument order: SET placeholders then WHERE placeholders
	setPlaceholders   int
	wherePlaceholders int
	upsert            upsertMode // INSERT-family execution strategy
}

var (
	insertRe = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+([A-Za-z0-9_]+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)\s*;?\s*$`)
	// upsertRe matches INSERT OR REPLACE / INSERT OR MERGE / UPSERT INTO.
	// Group 1 is the REPLACE|MERGE keyword (empty for the bare UPSERT form,
	// which defaults to replace semantics). Groups 2/3/4 are table/cols/vals.
	upsertRe = regexp.MustCompile(`(?is)^(?:INSERT\s+OR\s+(REPLACE|MERGE)|UPSERT)\s+INTO\s+([A-Za-z0-9_]+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)\s*;?\s*$`)
	deleteRe = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*;?\s*$`)
	// selectRe captures an optional trailing `LIMIT <n>` (digits only — no
	// placeholder, see IMPLEMENTATION_PLAN item 4). The LIMIT group is
	// validated/rejected after the match (LIMIT 0 is rejected). LIMIT is
	// only accepted on SELECT because only selectRe includes the group.
	selectRe = regexp.MustCompile(`(?is)^SELECT\s+(.+?)\s+FROM\s+([A-Za-z0-9_]+)\s*(?:WHERE\s+(.+?))?\s*(?:LIMIT\s+(\d+))?\s*;?\s*$`)
	// updatePrefixRe matches the "UPDATE <table> SET " prefix. The SET and
	// WHERE clauses are split manually by findUpdateSplit, which is
	// quote-aware and handles quoted literals containing the word WHERE.
	updatePrefixRe = regexp.MustCompile(`(?is)^UPDATE\s+([A-Za-z0-9_]+)\s+SET\s+`)
	// condRe matches a single condition: col <op> ? | 'literal' | "literal".
	// The operator is one of =, !=, <>, >=, <=, >, <. "<>" is normalized to
	// "!=" at parse time so there is a single canonical form internally.
	// The single-quoted pattern ('(?:[^']|'')*') handles SQL-style doubled
	// quote escapes ('') inside the literal. Same for double quotes.
	condRe = regexp.MustCompile(`(?i)^([A-Za-z0-9_]+)\s*(=|!=|<>|>=|<=|>|<)\s*(\?|'(?:[^']|'')*'|"(?:[^"]|"")*")$`)
)

func parseQuery(query string) (*parsedQuery, error) {
	q := strings.TrimSpace(query)

	if m := upsertRe.FindStringSubmatch(q); m != nil {
		mode := upsertReplace // UPSERT INTO defaults to replace
		if strings.EqualFold(m[1], "MERGE") {
			mode = upsertMerge
		}
		cols, _, err := parseInsertColsVals("UPSERT", m[3], m[4])
		if err != nil {
			return nil, err
		}
		return &parsedQuery{kind: qInsert, table: m[2], columns: cols, numPlaceholders: len(cols), upsert: mode}, nil
	}

	if m := insertRe.FindStringSubmatch(q); m != nil {
		cols, _, err := parseInsertColsVals("INSERT", m[2], m[3])
		if err != nil {
			return nil, err
		}
		return &parsedQuery{kind: qInsert, table: m[1], columns: cols, numPlaceholders: len(cols), upsert: upsertNone}, nil
	}

	// UPDATE is parsed manually because the regex cannot be quote-aware
	// when splitting SET ... WHERE ....
	if m := updatePrefixRe.FindStringSubmatch(q); m != nil {
		return parseUpdateQuery(m[1], q[len(m[0]):])
	}

	if m := deleteRe.FindStringSubmatch(q); m != nil {
		table := m[1]
		conds, n, err := parseWhere(m[2])
		if err != nil {
			return nil, err
		}
		if err := validateDeleteWhere(conds); err != nil {
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
		if err := validateSelectWhere(conds); err != nil {
			return nil, err
		}
		pq := &parsedQuery{kind: qSelect, table: table, where: conds, numPlaceholders: n}
		if colsStr == "*" {
			pq.allColumns = true
		} else {
			pq.columns = splitAndTrim(colsStr, ",")
		}
		// m[4] is the optional LIMIT <n> capture (digits only). LIMIT 0 is
		// rejected as ambiguous (no-limit vs. zero rows). A placeholder
		// (`LIMIT ?`) does not match \d+ so the whole statement is rejected
		// by the regex — see TestParseSelectLimitRejectsPlaceholder.
		// Values exceeding math.MaxInt32 are rejected because the server-
		// side $top parameter is an int32; a silent wrap-around would ask
		// the server for the wrong (possibly negative) count.
		if m[4] != "" {
			limit, err := strconv.Atoi(m[4])
			if err != nil {
				return nil, fmt.Errorf("aztablessql: invalid LIMIT value %q", m[4])
			}
			if limit < 1 {
				return nil, fmt.Errorf("aztablessql: LIMIT must be ≥ 1, got %d", limit)
			}
			if limit > math.MaxInt32 {
				return nil, fmt.Errorf("aztablessql: LIMIT must be ≤ %d, got %d", math.MaxInt32, limit)
			}
			pq.limit = limit
		}
		return pq, nil
	}

	return nil, fmt.Errorf("aztablessql: unsupported query: %q", q)
}

// parseInsertColsVals validates the (col1, col2, ...) / (val1, val2, ...)
// pair from an INSERT-family statement. Column/value counts must match and
// every value must be a bare "?" placeholder. The returned vals slice is
// currently only used for the count check (placeholders carry no literal
// payload).
//
// label is used in error messages so that "UPSERT INTO ..." statements don't
// report "INSERT" errors.
//
// Values are split with splitOnComma (quote-aware, preserves quotes) and only
// whitespace-trimmed, so a quoted literal like '?' is NOT mistaken for a
// placeholder — it is rejected. Columns use splitAndTrim which strips
// surrounding quotes (column names are not quoted in practice).
func parseInsertColsVals(label, colsStr, valsStr string) ([]string, []string, error) {
	cols := splitAndTrim(colsStr, ",")
	rawVals := splitOnComma(valsStr)
	if len(cols) != len(rawVals) {
		return nil, nil, fmt.Errorf("aztablessql: %s column/value count mismatch", label)
	}
	vals := make([]string, 0, len(rawVals))
	for _, v := range rawVals {
		t := strings.TrimSpace(v)
		if t != "?" {
			return nil, nil, fmt.Errorf("aztablessql: %s only supports placeholder values (?), got %q", label, v)
		}
		vals = append(vals, t)
	}
	return cols, vals, nil
}

// parseUpdateQuery parses the remainder after "UPDATE <table> SET ".
// It uses a quote-aware scan to find the real WHERE keyword that separates
// the SET clause from the WHERE clause.
func parseUpdateQuery(table, rest string) (*parsedQuery, error) {
	rest = strings.TrimRight(rest, " \t\n\r;")
	// Find the unquoted WHERE that separates SET from WHERE.
	wherePos := findUnquotedKeyword(rest, "WHERE")
	if wherePos < 0 {
		return nil, fmt.Errorf("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ?")
	}
	setStr := strings.TrimSpace(rest[:wherePos])
	whereStr := strings.TrimSpace(rest[wherePos+len("WHERE"):])

	assigns, setN, err := parseSet(setStr)
	if err != nil {
		return nil, err
	}
	conds, whereN, err := parseWhere(whereStr)
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

func parseSet(setStr string) ([]setAssign, int, error) {
	parts := splitOnComma(setStr)
	var assigns []setAssign
	n := 0
	for _, p := range parts {
		m := condRe.FindStringSubmatch(strings.TrimSpace(p))
		if m == nil {
			return nil, 0, fmt.Errorf("aztablessql: unsupported SET clause: %q", p)
		}
		col, op, tok := m[1], m[2], m[3]
		if op != "=" {
			return nil, 0, fmt.Errorf("aztablessql: SET only supports =, got %q", op)
		}
		if strings.EqualFold(col, "PartitionKey") || strings.EqualFold(col, "RowKey") {
			return nil, 0, fmt.Errorf("aztablessql: cannot SET PartitionKey/RowKey")
		}
		if strings.EqualFold(col, "ETag") || strings.EqualFold(col, "Timestamp") {
			return nil, 0, fmt.Errorf("aztablessql: cannot SET read-only pseudo-column %q (ETag/Timestamp are server-managed)", col)
		}
		if tok == "?" {
			assigns = append(assigns, setAssign{column: col, isPlaceholder: true})
			n++
		} else {
			assigns = append(assigns, setAssign{column: col, value: unquoteLiteral(tok)})
		}
	}
	if len(assigns) == 0 {
		return nil, 0, fmt.Errorf("aztablessql: UPDATE requires at least one SET assignment")
	}
	return assigns, n, nil
}

// validateUpdateWhere enforces that UPDATE stays a point update on
// PartitionKey + RowKey (both with "="), with an optional ETag = ? condition
// for optimistic concurrency. The ETag condition is mapped to If-Match at
// execution time, not included in the OData filter.
func validateUpdateWhere(conds []whereCond) error {
	var hasPK, hasRK, hasETag bool
	for _, c := range conds {
		switch strings.ToLower(c.column) {
		case "partitionkey":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: UPDATE WHERE only supports =, got %q for %q", c.op, c.column)
			}
			hasPK = true
		case "rowkey":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: UPDATE WHERE only supports =, got %q for %q", c.op, c.column)
			}
			hasRK = true
		case "etag":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: ETag condition only supports =, got %q", c.op)
			}
			if hasETag {
				return fmt.Errorf("aztablessql: only one ETag condition is allowed")
			}
			hasETag = true
		default:
			return fmt.Errorf("aztablessql: UPDATE WHERE only supports PartitionKey, RowKey and ETag, got %q", c.column)
		}
	}
	if !hasPK || !hasRK {
		return fmt.Errorf("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ?")
	}
	if len(conds) != 2 && len(conds) != 3 {
		return fmt.Errorf("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ? (optionally AND ETag = ?)")
	}
	return nil
}

// validateDeleteWhere enforces that DELETE stays a point delete: exactly
// PartitionKey = ? AND RowKey = ?, both with the "=" operator, with an
// optional ETag = ? condition for optimistic concurrency. Without this
// check, a predicate like `WHERE PartitionKey = 'p' AND RowKey > 'r'` would
// parse, resolve RowKey to "r", and silently delete the wrong entity.
func validateDeleteWhere(conds []whereCond) error {
	var hasPK, hasRK, hasETag bool
	for _, c := range conds {
		switch strings.ToLower(c.column) {
		case "partitionkey":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: DELETE WHERE only supports =, got %q for %q", c.op, c.column)
			}
			hasPK = true
		case "rowkey":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: DELETE WHERE only supports =, got %q for %q", c.op, c.column)
			}
			hasRK = true
		case "etag":
			if c.op != "=" {
				return fmt.Errorf("aztablessql: ETag condition only supports =, got %q", c.op)
			}
			if hasETag {
				return fmt.Errorf("aztablessql: only one ETag condition is allowed")
			}
			hasETag = true
		default:
			return fmt.Errorf("aztablessql: DELETE WHERE only supports PartitionKey, RowKey and ETag, got %q", c.column)
		}
	}
	if !hasPK || !hasRK {
		return fmt.Errorf("aztablessql: DELETE requires WHERE PartitionKey = ? AND RowKey = ?")
	}
	if len(conds) != 2 && len(conds) != 3 {
		return fmt.Errorf("aztablessql: DELETE requires WHERE PartitionKey = ? AND RowKey = ? (optionally AND ETag = ?)")
	}
	return nil
}

// validateSelectWhere rejects ETag and Timestamp in SELECT WHERE clauses.
// These are read-only pseudo-columns surfaced from entity meta fields, not
// stored OData-queryable properties — the Table Storage service would reject
// a filter like `ETag eq '...'`. They are selectable as result columns
// (SELECT ETag, Timestamp FROM ...) but not usable as filter predicates.
func validateSelectWhere(conds []whereCond) error {
	for _, c := range conds {
		if strings.EqualFold(c.column, "ETag") || strings.EqualFold(c.column, "Timestamp") {
			return fmt.Errorf("aztablessql: %q is a read-only pseudo-column and cannot be used in a WHERE filter (it is not a stored, queryable property)", c.column)
		}
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
		col, op, tok := m[1], m[2], m[3]
		if op == "<>" {
			op = "!=" // canonicalize; OData has no <>
		}
		if tok == "?" {
			conds = append(conds, whereCond{column: col, op: op, isPlaceholder: true})
			n++
		} else {
			conds = append(conds, whereCond{column: col, op: op, value: unquoteLiteral(tok)})
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

// unquoteLiteral removes the outer quote character from a quoted literal
// token (e.g. 'hello' → hello, "world" → world). Only the matching outer
// quote is stripped; inner quotes of the other type are preserved.
// Doubled quotes inside the literal (SQL escaping, e.g. 'It”s') are
// collapsed to a single quote.
func unquoteLiteral(tok string) string {
	if len(tok) < 2 {
		return tok
	}
	outer := tok[0]
	if outer != '\'' && outer != '"' {
		return tok
	}
	inner := tok[1 : len(tok)-1]
	// Collapse doubled outer-quote characters: '' → ', "" → "
	inner = strings.ReplaceAll(inner, string(outer)+string(outer), string(outer))
	return inner
}

// splitOnComma splits a string on commas, but respects single- and
// double-quoted literals (including doubled-quote escapes) so that a
// comma inside 'Doe, Jr' or "Doe, Jr" does not cause a split.
func splitOnComma(s string) []string {
	return splitQuoteAware(s, ',')
}

// splitOnAND splits a string on the keyword AND (case-insensitive, surrounded
// by whitespace), but respects single- and double-quoted literals so that AND
// inside 'A and B' or "A and B" does not cause a split.
func splitOnAND(s string) []string {
	var parts []string
	var current strings.Builder
	inSingle, inDouble := false, false

	s = strings.TrimSpace(s)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' && !inDouble {
			if inSingle && i+1 < len(s) && s[i+1] == '\'' {
				// Doubled quote escape — consume both, stay in quote.
				current.WriteByte(ch)
				current.WriteByte(ch)
				i++
				continue
			}
			inSingle = !inSingle
			current.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			if inDouble && i+1 < len(s) && s[i+1] == '"' {
				current.WriteByte(ch)
				current.WriteByte(ch)
				i++
				continue
			}
			inDouble = !inDouble
			current.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if i > 0 && current.Len() > 0 && isANDAt(s, i) {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
				i += 3 // skip "AND" — i now points to last char of AND
				// skip trailing whitespace after AND
				for i+1 < len(s) && isSpaceByte(string(s[i+1])) {
					i++
				}
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

// isANDAt checks whether s[pos:] starts with "AND" (case-insensitive),
// preceded by whitespace and followed by whitespace or end of string.
func isANDAt(s string, pos int) bool {
	if pos+3 > len(s) {
		return false
	}
	if pos == 0 || !isSpaceByte(string(s[pos-1])) {
		return false
	}
	rest := s[pos : pos+3]
	if !strings.EqualFold(rest, "AND") {
		return false
	}
	if pos+3 == len(s) {
		return true
	}
	return isSpaceByte(string(s[pos+3]))
}

// isSpaceByte returns true if the byte is whitespace (space, tab, newline,
// carriage return).
func isSpaceByte(s string) bool {
	if len(s) == 0 {
		return false
	}
	return unicode.IsSpace(rune(s[0]))
}

// splitQuoteAware splits on a single-byte delimiter while respecting
// single- and double-quoted literals (including doubled-quote escapes).
// The delimiter inside quotes is preserved.
func splitQuoteAware(s string, delim byte) []string {
	var parts []string
	var current strings.Builder
	inSingle, inDouble := false, false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' && !inDouble {
			if inSingle && i+1 < len(s) && s[i+1] == '\'' {
				current.WriteByte(ch)
				current.WriteByte(ch)
				i++
				continue
			}
			inSingle = !inSingle
			current.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			if inDouble && i+1 < len(s) && s[i+1] == '"' {
				current.WriteByte(ch)
				current.WriteByte(ch)
				i++
				continue
			}
			inDouble = !inDouble
			current.WriteByte(ch)
			continue
		}
		if ch == delim && !inSingle && !inDouble {
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

// findUnquotedKeyword scans s for the keyword (case-insensitive) outside of
// single- or double-quoted literals (including doubled-quote escapes).
// Returns the byte position of the keyword, or -1 if not found.
// The keyword must be preceded by whitespace (or start of string) and
// followed by whitespace (or end of string).
func findUnquotedKeyword(s, keyword string) int {
	upper := strings.ToUpper(s)
	target := strings.ToUpper(keyword)
	tlen := len(target)
	inSingle, inDouble := false, false

	for i := 0; i < len(upper); i++ {
		ch := upper[i]
		if ch == '\'' && !inDouble {
			if inSingle && i+1 < len(upper) && upper[i+1] == '\'' {
				i++ // skip doubled quote
				continue
			}
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			if inDouble && i+1 < len(upper) && upper[i+1] == '"' {
				i++ // skip doubled quote
				continue
			}
			inDouble = !inDouble
			continue
		}
		if inSingle || inDouble {
			continue
		}
		if i+tlen <= len(upper) && upper[i:i+tlen] == target {
			beforeOK := i == 0 || unicode.IsSpace(rune(upper[i-1]))
			afterOK := i+tlen == len(upper) || unicode.IsSpace(rune(upper[i+tlen]))
			if beforeOK && afterOK {
				return i
			}
		}
	}
	return -1
}
