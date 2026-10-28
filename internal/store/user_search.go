package store

import (
	"context"
	"errors"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Paged, searchable reads of the account directory for GET /api/v2/admin/users.
//
// The directory previously answered with every row and no search, which pushed
// both jobs onto the operator console: it downloaded the whole table and
// filtered it in the browser. That works until the table outgrows a response,
// and it fails silently when it does — a console that pages a truncated list
// cannot tell an operator that the account they searched for exists on a page
// nobody sent. Both jobs move here.
//
// Composed rather than added to core_queries.sql as named statements, for the
// reason catalog_search.go gives: directions (3) times status-or-not (2) times
// search-or-not (2) is twelve near-identical statements to keep in step. The
// composition rules are catalog_search.go's, unchanged and worth restating
// because they are what makes string-built SQL safe here:
//
//   - No table name, column name, operator or direction is ever derived from
//     request data. The status crosses as a bound parameter, the term crosses as
//     a bound parameter, and the page direction is a typed CatalogPage.
//   - Reducer fragments are compile-time constants.
//
// Unlike the pool and voucher lists there is no static-statement fallback for
// the unfiltered page. Those lists delegate to statements whose plans were
// measured before filtering existed and must not change; this endpoint has no
// such history, so every shape it can produce is built here and there is one
// code path to read.
//
// Plan shape follows the voucher slice's measurement (migrations/
// 011_catalog_search_indexes.sql): a search materializes its match set before
// ordering, because adding a keyset predicate to a query already ordered by
// `id DESC LIMIT n` makes the primary key look like it serves the filter, the
// ordering and the early abort at once, and the planner then demotes the search
// to a row filter over the table. That measurement was taken on 13,181 vouchers.
// It has not been repeated for users: the development directory holds six rows,
// where every shape is a sequential scan and no measurement means anything. The
// shape is therefore carried over on the strength of the voucher result, and
// wants re-measuring against a representative directory.

type (
	// UserStatus is one value of the user_status enum. Defined as a type so a
	// status reaching SQL has been through ParseUserStatus: the column is an
	// enum, and an unrecognized value fails the cast inside PostgreSQL, which
	// surfaces as a request error the caller cannot act on.
	UserStatus string

	// UserFilter is the validated operator search over the account directory.
	// Construct it through NewUserFilter so Term is normalized and Status is a
	// known enum value.
	UserFilter struct {
		// Term is normalized search text: trimmed, with internal whitespace
		// collapsed to single spaces. Empty means no search predicate. It is
		// matched against the identity and contact columns only — name, email,
		// phone and address.
		Term string
		// Status narrows to one approval state. Empty means every state.
		Status UserStatus
		// Roles narrows to any of the selected roles. Empty means every role.
		Roles []UserRole
	}
)

const (
	UserStatusPending  UserStatus = "PENDING"
	UserStatusApproved UserStatus = "APPROVED"
	UserStatusRejected UserStatus = "REJECTED"

	// userPageProjection hydrates one already-selected page of account IDs.
	//
	// The column list and the aliases are the ones every other user read returns
	// — blockchain_address as ethereum_address, and the nullable text columns
	// coalesced — so one response shape describes an account everywhere it
	// appears.
	//
	// Ordered newest-first by id, and by id rather than created_at: keyset
	// pagination needs a unique monotonic key, and two accounts created in the
	// same transaction share a created_at. id is generated always as identity, so
	// id descending is created_at descending with the ties broken.
	userPageProjection = `
SELECT u.id, u.blockchain_address AS ethereum_address, u.username, u.name,
    COALESCE(u.email, '') AS email, COALESCE(u.phone, '') AS phone,
    COALESCE(u.country, '') AS country,
    u.preferred_currency, u.bio, u.website,
    u.location_name, u.geo_lat, u.geo_lng, u.avatar_url, u.role, u.status,
    u.max_pools, u.max_tokens, u.max_catalogs, u.max_offers, u.max_wants, u.created_at, u.updated_at
FROM page
INNER JOIN users u ON u.id = page.id
ORDER BY u.id DESC;`
)

var (
	// userStatuses is the user_status enum as migrations define it. Kept as a
	// closed set here rather than read from the database, because it decides
	// whether a request is valid before any transaction opens.
	userStatuses = []UserStatus{UserStatusPending, UserStatusApproved, UserStatusRejected}

	ErrUnknownUserStatus = errors.New(`status must be "PENDING", "APPROVED" or "REJECTED"`)
)

// NewUserFilter normalizes and validates the raw request values.
//
// The term rules are the catalog's, deliberately: an operator typing into the
// console's account search and a shopper typing into the voucher catalog get the
// same minimum, the same maximum and the same error text, and the shared
// frontend normalization in @clc/core already holds a term back until it is
// searchable rather than sending one the backend will reject.
func NewUserFilter(rawTerm, rawStatus string, rawRoles ...string) (UserFilter, error) {
	filter := UserFilter{Term: NormalizeCatalogTerm(rawTerm)}

	if err := validateTermLength(filter.Term); err != nil {
		return UserFilter{}, err
	}

	status, err := ParseUserStatus(rawStatus)
	if err != nil {
		return UserFilter{}, err
	}
	filter.Status = status
	roles, err := ParseUserRoles(rawRoles)
	if err != nil {
		return UserFilter{}, err
	}
	filter.Roles = roles

	return filter, nil
}

// ParseUserStatus accepts an empty string as "every status" and rejects anything
// outside the enum. Case is folded because the value names an approval state
// rather than carrying one: `?status=pending` asks an unambiguous question.
func ParseUserStatus(raw string) (UserStatus, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	candidate := UserStatus(strings.ToUpper(trimmed))
	for _, status := range userStatuses {
		if candidate == status {
			return status, nil
		}
	}
	return "", ErrUnknownUserStatus
}

// prefixOnly reports whether this filter's term matches as a prefix rather than
// a substring.
func (f UserFilter) prefixOnly() bool {
	return termPrefixOnly(f.Term)
}

// likeTerm escapes the LIKE metacharacters in this filter's term.
func (f UserFilter) likeTerm() string {
	return escapeLikeTerm(f.Term)
}

// composeUserPage builds one page of the directory, in the same two driving
// shapes as the pool and voucher pages: a search materializes its match set so
// the trigram indexes drive, and a status-only or unfiltered query walks the
// primary key directly.
func composeUserPage(filter UserFilter, page CatalogPage, cursor, limit uint64) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT u.id\n" +
			"    FROM users u" +
			whereClause(
				userStatusPredicate(args, filter),
				userRolesPredicate(args, filter),
				cursorPredicate(args, "u.id", page, cursor),
			) +
			"\n    ORDER BY u.id " + pageOrder(page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + userPageProjection, args: args.values}
	}

	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT u.id\n" +
		"    FROM users u" +
		whereClause(
			userSearchPredicate(args, filter),
			userStatusPredicate(args, filter),
			userRolesPredicate(args, filter),
		) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(cursorPredicate(args, "m.id", page, cursor)) +
		"\n    ORDER BY m.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"
	return composed{sql: body + userPageProjection, args: args.values}
}

// userSearchPredicate matches the term against the four columns that identify an
// account to an operator: name, email, phone and address.
//
// The set stops there on purpose. Country is not searched — a two-letter code is
// a classification, and folding it into free text makes "KE" answer with every
// Kenyan account alongside everyone called Kenneth; it belongs in a `country`
// parameter of its own if the console ever wants one. Role is not searched
// either: there are three roles and a search is not how anyone finds them.
//
// email and phone are nullable, and a NULL never matches a LIKE, so an account
// missing one is simply not matched on it — no COALESCE is needed and none is
// wanted, since coalescing a missing email to the empty string would let a
// two-character prefix search match every account that has none.
//
// A pasted address matches here as a substring rather than through the unique
// index on lower(blockchain_address). That keeps one driving shape instead of
// adding an is-this-an-address branch, and a 42-character term extracts enough
// trigrams to be selective; revisit only if a measurement disagrees.
func userSearchPredicate(args *sqlArgs, filter UserFilter) string {
	if filter.Term == "" {
		return ""
	}

	columns := []string{"u.name", "u.email", "u.phone", "u.blockchain_address"}
	pattern := "'%' || lower(%s) || '%'"
	if filter.prefixOnly() {
		pattern = "lower(%s) || '%'"
	}

	// Bound once and reused for every column: the term is one value, and binding
	// it four times would make the argument list vary with the column count.
	placeholder := args.bind(filter.likeTerm())

	matches := make([]string, 0, len(columns))
	for _, column := range columns {
		matches = append(matches,
			"lower("+column+") LIKE "+likePattern(pattern, placeholder)+" ESCAPE '\\'")
		placeholder = args.reuse()
	}
	return "(" + strings.Join(matches, "\n            OR ") + ")"
}

// likePattern fills the one placeholder slot in a constant pattern fragment.
// fmt.Sprintf is deliberately not used: the fragments contain literal '%'
// characters, which a format string would consume.
func likePattern(pattern, placeholder string) string {
	return strings.Replace(pattern, "%s", placeholder, 1)
}

// userStatusPredicate narrows to one approval state. The value is bound and cast
// to the enum, so an unparsed status could only ever fail inside PostgreSQL —
// ParseUserStatus is what keeps that from reaching here.
func userStatusPredicate(args *sqlArgs, filter UserFilter) string {
	if filter.Status == "" {
		return ""
	}
	return "u.status = " + args.bind(string(filter.Status)) + "::user_status"
}

func userRolesPredicate(args *sqlArgs, filter UserFilter) string {
	if len(filter.Roles) == 0 {
		return ""
	}
	// The console's Admins filter is a stable product concept rather than an
	// arbitrary pair of request strings. Keep its predicate literal so
	// PostgreSQL can prove it satisfies users_role_id_idx even for a generic
	// prepared plan. Validated single-role and other combinations remain bound.
	if len(filter.Roles) == 2 {
		hasAdmin := false
		hasSuperadmin := false
		for _, role := range filter.Roles {
			hasAdmin = hasAdmin || role == UserRoleAdmin
			hasSuperadmin = hasSuperadmin || role == UserRoleSuperadmin
		}
		if hasAdmin && hasSuperadmin {
			return "u.role IN ('ADMIN'::user_role, 'SUPERADMIN'::user_role)"
		}
	}
	values := make([]string, 0, len(filter.Roles))
	for _, role := range filter.Roles {
		values = append(values, args.bind(string(role))+"::user_role")
	}
	return "u.role IN (" + strings.Join(values, ", ") + ")"
}

// ListUsersFiltered returns one page of the account directory under the given
// filter. Every page direction applies every active predicate; none falls
// through to an unfiltered read.
func (pg *Pg) ListUsersFiltered(
	ctx context.Context,
	tx pgx.Tx,
	filter UserFilter,
	page CatalogPage,
	cursor, limit uint64,
) ([]User, error) {
	query := composeUserPage(filter, page, cursor, limit)

	var users []User
	if err := pgxscan.Select(ctx, tx, &users, query.sql, query.args...); err != nil {
		return nil, err
	}
	return users, nil
}
