package store

import "strings"

// AdminTransactionSearch is the validated free-text filter the operator uses
// across the transaction lists. It follows the account directory's term
// contract: one shared normalization and length limit means a half-typed query
// never turns into an unbounded request on one screen but a 400 on another.
type AdminTransactionSearch struct {
	// Term is normalized search text. Empty means no search predicate.
	Term string
}

// NewAdminTransactionSearch normalizes and validates an operator's query.
func NewAdminTransactionSearch(raw string) (AdminTransactionSearch, error) {
	search := AdminTransactionSearch{Term: NormalizeCatalogTerm(raw)}
	if err := validateTermLength(search.Term); err != nil {
		return AdminTransactionSearch{}, err
	}
	return search, nil
}

// prefixOnly keeps a two-character query bounded, exactly as the catalog and
// account directory do. Three or more characters may match anywhere in an
// opaque address, hash, or tracking ID.
func (s AdminTransactionSearch) prefixOnly() bool {
	return termPrefixOnly(s.Term)
}

// likeTerm escapes the LIKE metacharacters in the search text.
func (s AdminTransactionSearch) likeTerm() string {
	return escapeLikeTerm(s.Term)
}

// predicate builds the bounded, case-insensitive OR match over one resource's
// closed set of searchable columns. The columns come only from store-owned arm
// definitions; request data is bound once and is never interpolated into SQL.
func (s AdminTransactionSearch) predicate(
	columns []string,
	bind func(string) string,
	reuse func() string,
) string {
	if s.Term == "" {
		return ""
	}

	pattern := "'%' || lower(%s) || '%'"
	if s.prefixOnly() {
		pattern = "lower(%s) || '%'"
	}

	placeholder := bind(s.likeTerm())
	matches := make([]string, 0, len(columns))
	for _, column := range columns {
		matches = append(matches,
			"lower("+column+") LIKE "+likePattern(pattern, placeholder)+" ESCAPE '\\'")
		placeholder = reuse()
	}
	return "(" + strings.Join(matches, "\n            OR ") + ")"
}
