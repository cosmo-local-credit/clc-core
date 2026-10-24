package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Catalog search and tag filtering for the public voucher catalog and the pool
// catalog.
//
// The store loads static named SQL from core_queries.sql into a fixed Queries
// struct. Copying a whole statement for every combination of entity, page
// direction, viewport, search, and tag filter would multiply into an
// unmaintainable set of near-identical statements, so the filtered statements
// are composed here instead. The composition rules are deliberately narrow:
//
//   - Driving shapes come from a closed set chosen by (entity, direction,
//     viewport, search-or-not). Search is a driving-shape input, not a reducer,
//     because it changes which index drives the query.
//   - Reducer predicates (the tag EXISTS) are appended from compile-time
//     constant fragments.
//   - No table name, column name, operator, direction, or SQL fragment is ever
//     derived from request data. Public strings are parsed into the typed enums
//     below and every value crosses as a bound parameter.
//   - Selected tags travel as one text[] parameter, so query text does not vary
//     with how many tags were requested.
//
// The unfiltered paths keep using the existing static statements: with no
// search predicate a backward primary-key walk is the correct plan, and leaving
// the map's bulk-import path untouched keeps its measured plans.

type (
	// TagMatch decides how several requested tags combine. It applies within
	// one facet; different facets always combine with AND.
	TagMatch uint8

	// CatalogPage names one of the three cursor directions the public catalog
	// contract exposes. The previous-page direction is part of that contract
	// (`cursor > 0 and next = false`), so every filtered query must implement
	// it — falling through to an unfiltered previous-page statement would
	// silently drop the caller's filters.
	CatalogPage uint8

	// CatalogFilter is the validated search and tag filter shared by the
	// voucher and pool catalog lists. Construct it through NewCatalogFilter so
	// Term is normalized and Tags are deduplicated and ordered.
	CatalogFilter struct {
		// Term is normalized search text: trimmed, with internal whitespace
		// collapsed to single spaces. Empty means no search predicate. It is
		// matched against name and symbol only — never against report bodies
		// or offering descriptions.
		Term string
		// Tags are canonical token_tag values, deduplicated and sorted.
		// Sorting makes the argument list deterministic for a given request so
		// two equivalent requests produce byte-identical parameters.
		Tags     []string
		TagMatch TagMatch
		// VoucherIDs are database IDs resolved from the request's stable
		// (chain_id, address) references inside the handler transaction. They
		// never come directly from public input.
		VoucherIDs   []uint64
		VoucherMatch TagMatch
		// PoolIDs are resolved from stable chain/address references. A voucher
		// matches when an allowed pool_tokens row links it to any selected pool.
		PoolIDs   []uint64
		PoolMatch TagMatch
		// Owner restricts the page to rows belonging to one address: a voucher's
		// owner_address, a report's author_address. Empty means no restriction.
		Owner string
		// Nearby is resolved from the viewer profile, independently of optional search filters.
		Nearby *CatalogLocation
	}

	// VoucherRef is the stable public identity of a voucher. Numeric database
	// IDs are deliberately not part of URLs because catalog transfers may
	// remap them between environments.
	VoucherRef struct {
		ChainID int64
		Address string
	}

	// PoolRef is the stable public identity of a pool.
	PoolRef struct {
		ChainID int64
		Address string
	}

	// PoolFacet is one report-pool relationship option.
	PoolFacet struct {
		ID      uint64 `db:"id"          json:"id"`
		ChainID int64  `db:"chain_id"    json:"chainId"`
		Address string `db:"address"     json:"address"`
		Name    string `db:"name"        json:"name"`
		Symbol  string `db:"symbol"      json:"symbol"`
		Count   uint64 `db:"match_count" json:"count"`
	}

	// TagFacet is one row of a tag facet: a vocabulary value and how many
	// entities the current non-tag filters leave under it. A value with no
	// matches is still returned, with a zero count.
	TagFacet struct {
		Tag   string `db:"tag_value"   json:"tag"`
		Count uint64 `db:"match_count" json:"count"`
	}

	// VoucherFacet is one relationship-filter option and the number of entities
	// matching the current non-voucher filters that carry it.
	VoucherFacet struct {
		ID      uint64 `db:"id"          json:"id"`
		ChainID int64  `db:"chain_id"    json:"chainId"`
		Address string `db:"address"     json:"address"`
		Name    string `db:"name"        json:"name"`
		Symbol  string `db:"symbol"      json:"symbol"`
		Count   uint64 `db:"match_count" json:"count"`
	}

	// sqlArgs collects bound values and hands back the placeholder that refers
	// to each, so placeholder numbering follows append order and cannot drift
	// out of step with the argument slice.
	sqlArgs struct {
		values []any
	}

	// composed is a generated statement and its positional arguments.
	composed struct {
		sql  string
		args []any
	}
)

// Values are explicit rather than iota-derived: both enums live in one const
// block, where a second iota run would continue the first one's numbering.
const (
	// TagMatchAny returns entities carrying at least one requested tag.
	TagMatchAny TagMatch = 0
	// TagMatchAll returns entities carrying every requested tag.
	TagMatchAll TagMatch = 1

	CatalogFirstPage CatalogPage = 0
	CatalogNextPage  CatalogPage = 1
	CatalogPrevPage  CatalogPage = 2

	// CatalogTermMinRunes is the shortest term that searches. One character is
	// rejected rather than silently ignored: a single character matches most of
	// the catalog, so accepting it would return a full page that looks filtered
	// but is not, and it is the one length where a trigram index cannot help.
	CatalogTermMinRunes = 2

	// CatalogTermPrefixRunes is the longest term matched as a prefix rather than
	// a substring. Two characters yield no complete trigram, so pg_trgm can only
	// use partial-match entries; a prefix keeps that bounded, whereas a
	// two-character substring both matches too much to be a useful answer and
	// makes the index work hardest to say so. Measured at 5x scale a
	// two-character prefix is still index-driven, at 0.564 ms.
	CatalogTermPrefixRunes = 2

	// CatalogTermMaxRunes bounds the term so a caller cannot force PostgreSQL
	// to extract an unbounded trigram list.
	CatalogTermMaxRunes = 64

	// CatalogMaxTags bounds the tag array. token_tag holds 44 values and no
	// useful query selects most of them.
	CatalogMaxTags = 12

	// CatalogMaxVouchers bounds one relationship filter. Beyond this the query
	// is difficult to understand on a mobile filter sheet and an all-match
	// reducer does work with no useful catalog-browsing benefit.
	CatalogMaxVouchers = 12

	// CatalogMaxPools permits a wallet to send its full verified creditable-pool
	// set while bounding the relationship reducer.
	CatalogMaxPools = 100

	// catalogStatementTimeout caps a catalog read at the database, below the
	// outer HTTP timeout, so a pathological plan cannot hold a connection
	// open indefinitely (PostgreSQL's default is unlimited). Representative
	// catalog queries are single-digit milliseconds, so this is a safety
	// ceiling and not a tuned budget; tighten it once a p95 budget is agreed.
	catalogStatementTimeout = "5s"
)

var (
	ErrCatalogTermTooShort = errors.New("q must be at least " +
		strconv.Itoa(CatalogTermMinRunes) + " characters")
	ErrCatalogTermTooLong = errors.New("q must be at most " +
		strconv.Itoa(CatalogTermMaxRunes) + " characters")
	ErrCatalogTooManyTags = errors.New("tags must contain at most " +
		strconv.Itoa(CatalogMaxTags) + " values")
	ErrCatalogUnknownTagMatch = errors.New(`tagMatch must be "any" or "all"`)
	ErrCatalogTooManyVouchers = errors.New("vouchers must contain at most " +
		strconv.Itoa(CatalogMaxVouchers) + " values")
	ErrCatalogUnknownVoucherMatch = errors.New(`voucherMatch must be "any" or "all"`)
	ErrCatalogTooManyPools        = errors.New("pools must contain at most " +
		strconv.Itoa(CatalogMaxPools) + " values")
)

// NewCatalogFilter normalizes and validates raw request values. Tags are
// deduplicated before this returns so an all-tags query can compare a match
// count against cardinality($1) and mean "every distinct tag the caller asked
// for". Tag values are compared exactly, never case-folded: a client is expected
// to send values it was given, either from a `facets=true` response or from
// GET /api/v2/token-tags on an authenticated screen.
func NewCatalogFilter(rawTerm string, rawTags []string, match TagMatch) (CatalogFilter, error) {
	filter := CatalogFilter{
		Term:         NormalizeCatalogTerm(rawTerm),
		Tags:         normalizeCatalogTags(rawTags),
		TagMatch:     match,
		VoucherMatch: TagMatchAny,
	}

	if err := validateTermLength(filter.Term); err != nil {
		return CatalogFilter{}, err
	}
	if len(filter.Tags) > CatalogMaxTags {
		return CatalogFilter{}, ErrCatalogTooManyTags
	}
	if match != TagMatchAny && match != TagMatchAll {
		return CatalogFilter{}, ErrCatalogUnknownTagMatch
	}

	return filter, nil
}

// WithVoucherIDs returns a copy narrowed to the resolved voucher IDs. The
// handler calls this only after every public voucher reference has resolved in
// the database, so SQL receives canonical numeric keys rather than addresses.
func (f CatalogFilter) WithVoucherIDs(raw []uint64, match TagMatch) (CatalogFilter, error) {
	ids := slices.Clone(raw)
	slices.Sort(ids)
	ids = slices.Compact(ids)

	if len(ids) > CatalogMaxVouchers {
		return CatalogFilter{}, ErrCatalogTooManyVouchers
	}
	if match != TagMatchAny && match != TagMatchAll {
		return CatalogFilter{}, ErrCatalogUnknownVoucherMatch
	}

	f.VoucherIDs = ids
	f.VoucherMatch = match
	return f, nil
}

// WithPoolIDs returns a copy narrowed to vouchers allowed by at least one of
// the resolved pools. Pool discovery is intentionally any-match: credit access
// through one verified pool is sufficient.
func (f CatalogFilter) WithPoolIDs(raw []uint64) (CatalogFilter, error) {
	return f.WithPoolIDsMatch(raw, TagMatchAny)
}

// WithPoolIDsMatch narrows report relationships with explicit any/all
// semantics. Other catalog callers keep their established any-match behavior.
func (f CatalogFilter) WithPoolIDsMatch(raw []uint64, match TagMatch) (CatalogFilter, error) {
	ids := slices.Clone(raw)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > CatalogMaxPools {
		return CatalogFilter{}, ErrCatalogTooManyPools
	}
	if match != TagMatchAny && match != TagMatchAll {
		return CatalogFilter{}, ErrCatalogUnknownVoucherMatch
	}
	f.PoolIDs = ids
	f.PoolMatch = match
	return f, nil
}

// NormalizeCatalogTerm trims the term and collapses internal whitespace, so
// "  sarafu   network " and "sarafu network" are one cache key and one set of
// bound parameters. Normalization happens before the length check so padding
// cannot smuggle a one-character search past it.
func NormalizeCatalogTerm(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

// normalizeCatalogTags trims, drops blanks, deduplicates, and sorts.
func normalizeCatalogTags(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(raw))
	tags := make([]string, 0, len(raw))
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return nil
	}

	slices.Sort(tags)
	return tags
}

// Active reports whether the filter narrows anything. When it does not, callers
// use the existing static statements unchanged.
func (f CatalogFilter) Active() bool {
	return f.Term != "" || len(f.Tags) > 0 || len(f.VoucherIDs) > 0 || len(f.PoolIDs) > 0 || f.Owner != "" || f.Nearby != nil
}

// prefixOnly reports whether the term is short enough that it matches as a
// prefix rather than a substring.
func (f CatalogFilter) prefixOnly() bool {
	return termPrefixOnly(f.Term)
}

// likeTerm escapes the LIKE metacharacters in this filter's term.
func (f CatalogFilter) likeTerm() string {
	return escapeLikeTerm(f.Term)
}

// The three rules below are the term contract itself — how long a term may be,
// when it matches as a prefix, and how its metacharacters are escaped — so they
// are package-level rather than methods on CatalogFilter. The account directory
// search (user_search.go) is a second caller with no tags and no facets, and a
// second copy of an escaping rule is how one of them ends up wrong.

// validateTermLength enforces the searchable range. An empty term is not a
// length error: it means no search predicate, which every filtered list accepts.
func validateTermLength(term string) error {
	runes := utf8.RuneCountInString(term)
	if runes == 0 {
		return nil
	}
	if runes < CatalogTermMinRunes {
		return ErrCatalogTermTooShort
	}
	if runes > CatalogTermMaxRunes {
		return ErrCatalogTermTooLong
	}
	return nil
}

// termPrefixOnly reports whether a term is short enough to match as a prefix
// rather than a substring.
func termPrefixOnly(term string) bool {
	return utf8.RuneCountInString(term) <= CatalogTermPrefixRunes
}

// escapeLikeTerm escapes the LIKE metacharacters in a normalized term so a
// caller searching for a literal "%" or "_" gets that literal and cannot widen
// the pattern. The escape character is backslash, declared explicitly by every
// emitted LIKE so the intent is visible in the SQL; the backslash itself must be
// doubled first or it would escape the character after it.
func escapeLikeTerm(term string) string {
	escaped := strings.ReplaceAll(term, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, "%", `\%`)
	return strings.ReplaceAll(escaped, "_", `\_`)
}

// mergeTagFacets combines a request-independent vocabulary with the counts the
// current filter produced.
//
// The two halves are separate queries on purpose. Sharing one statement made the
// vocabulary's junction scans dominate the planner's cost estimate and it
// abandoned the trigram index for the search half, collapsing into hash joins
// over the whole tag junction. Split, the vocabulary is a parameterless statement
// (~6.2 ms at 5x scale, and cacheable because it depends on nothing in the
// request) and the count query keeps its index: 2.25 ms for a common term,
// 0.09 ms for a no-match one. Merging here is also where the two facet rules that
// are not the database's business get applied:
//
//   - A selected tag stays present even when nothing matches it, so the control
//     that set it does not vanish from the UI.
//   - Every vocabulary value is returned, including zero counts, so a filter
//     that exists can be shown greyed out rather than silently dropped.
//
// The result is ordered by tag value: this vocabulary is 44 values at most, so
// there is no count-ordered limit to apply.
func mergeTagFacets(vocabulary []string, counts map[string]uint64, selected []string) []TagFacet {
	values := make([]string, 0, len(vocabulary)+len(selected))
	seen := make(map[string]struct{}, len(vocabulary)+len(selected))
	for _, value := range slices.Concat(vocabulary, selected) {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	slices.Sort(values)

	facets := make([]TagFacet, 0, len(values))
	for _, value := range values {
		facets = append(facets, TagFacet{Tag: value, Count: counts[value]})
	}
	return facets
}

// tagCounts runs a composed count query and collects its rows. Only tags with
// at least one match appear; mergeTagFacets supplies the zeroes.
func (pg *Pg) tagCounts(ctx context.Context, tx pgx.Tx, query composed) (map[string]uint64, error) {
	var rows []TagFacet
	if err := pgxscan.Select(ctx, tx, &rows, query.sql, query.args...); err != nil {
		return nil, err
	}

	counts := make(map[string]uint64, len(rows))
	for _, row := range rows {
		counts[row.Tag] = row.Count
	}
	return counts, nil
}

// listTagFacets assembles a facet from its two halves, skipping the vocabulary
// statement when nothing narrows the candidate set.
//
// The skip is a SQL equivalence, not an approximation. Each count query groups
// over the same join as its entity's vocabulary statement, under the same
// visibility predicates; with no term, no tags, and no viewport the grouping keys
// are therefore exactly the vocabulary, and every one of them has a non-zero
// count because a value only enters the vocabulary by being carried by something
// visible. Running both would pay a second pass over the whole tag junction to
// rediscover rows the count query already returned: measured at 5x scale the
// vocabulary is 5.4 ms against the unfiltered count query's 9.1 ms, so the skip
// removes 37% of the widest facet on the endpoint.
//
// `narrowed` is the caller's answer because only the caller knows its own
// narrowing dimensions -- vouchers have a viewport, pools do not.
func (pg *Pg) listTagFacets(
	ctx context.Context,
	tx pgx.Tx,
	counts composed,
	vocabularyStatement string,
	narrowed bool,
	selected []string,
) ([]TagFacet, error) {
	matched, err := pg.tagCounts(ctx, tx, counts)
	if err != nil {
		return nil, err
	}

	if !narrowed {
		// The count keys are the vocabulary, so they are passed as one. This still
		// goes through mergeTagFacets to keep a single implementation of the
		// ordering and assembly rules.
		return mergeTagFacets(slices.Collect(maps.Keys(matched)), matched, nil), nil
	}

	var vocabulary []string
	if err := pgxscan.Select(ctx, tx, &vocabulary, vocabularyStatement); err != nil {
		return nil, err
	}
	return mergeTagFacets(vocabulary, matched, selected), nil
}

// SetCatalogStatementTimeout applies catalogStatementTimeout to one catalog
// read transaction. set_config with is_local = true is the parameterized
// equivalent of SET LOCAL, which cannot take a bound parameter.
func (pg *Pg) SetCatalogStatementTimeout(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, pg.queries.SetCatalogStatementTimeout, catalogStatementTimeout)
	return err
}

// UnknownTokenTags returns the requested values that are not in the token_tag
// vocabulary. The handler rejects the request when this is non-empty: silently
// dropping an unknown tag would answer a different question than the one asked
// and would report a count for a filter that was never applied.
func (pg *Pg) UnknownTokenTags(ctx context.Context, tx pgx.Tx, tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}

	var unknown []string
	if err := pgxscan.Select(ctx, tx, &unknown, pg.queries.UnknownTokenTags, tags); err != nil {
		return nil, err
	}
	return unknown, nil
}

// ResolveVoucherRefs resolves stable public identities to the internal keys
// used by report_vouchers and pool_tokens. Callers reject a short result so a
// typo cannot silently broaden the requested relationship filter.
func (pg *Pg) ResolveVoucherRefs(
	ctx context.Context,
	tx pgx.Tx,
	refs []VoucherRef,
) ([]uint64, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	chainIDs := make([]int64, 0, len(refs))
	addresses := make([]string, 0, len(refs))
	for _, ref := range refs {
		chainIDs = append(chainIDs, ref.ChainID)
		addresses = append(addresses, ref.Address)
	}

	var ids []uint64
	if err := pgxscan.Select(ctx, tx, &ids, pg.queries.ResolveVoucherRefs, chainIDs, addresses); err != nil {
		return nil, err
	}
	return ids, nil
}

// ResolvePoolRefs resolves stable public pool identities to internal keys.
func (pg *Pg) ResolvePoolRefs(ctx context.Context, tx pgx.Tx, refs []PoolRef) ([]uint64, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	chainIDs := make([]int64, 0, len(refs))
	addresses := make([]string, 0, len(refs))
	for _, ref := range refs {
		chainIDs = append(chainIDs, ref.ChainID)
		addresses = append(addresses, ref.Address)
	}
	var ids []uint64
	if err := pgxscan.Select(ctx, tx, &ids, pg.queries.ResolvePoolRefs, chainIDs, addresses); err != nil {
		return nil, err
	}
	return ids, nil
}

// CountVouchersOwnedBy answers how many of the given voucher rows list owner as
// their owner_address, compared case-insensitively. A count rather than a list
// because the caller only needs to know whether every id passed; ids not in
// the table simply do not count, so an unknown id reads the same as one owned
// by someone else.
func (pg *Pg) CountVouchersOwnedBy(ctx context.Context, tx pgx.Tx, ids []uint64, owner string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int
	if err := tx.QueryRow(ctx, pg.queries.CountVouchersOwnedBy, ids, owner).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (pg *Pg) CountPoolsOwnedBy(ctx context.Context, tx pgx.Tx, ids []uint64, owner string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int
	if err := tx.QueryRow(ctx, pg.queries.CountPoolsOwnedBy, ids, owner).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (a *sqlArgs) bind(value any) string {
	a.values = append(a.values, value)
	return "$" + strconv.Itoa(len(a.values))
}

// reuse returns an already-bound placeholder. The search fragments match the
// term against both name and symbol and must not bind it twice.
func (a *sqlArgs) reuse() string {
	return "$" + strconv.Itoa(len(a.values))
}

// voucherIDsParam converts database IDs to pgx's signed PostgreSQL integer
// representation before binding an array. The IDs originated in PostgreSQL,
// so the conversion cannot overflow int64.
func voucherIDsParam(ids []uint64) []int64 {
	values := make([]int64, 0, len(ids))
	for _, id := range ids {
		values = append(values, int64(id))
	}
	return values
}

// voucherPoolPredicate narrows vouchers through the authoritative allowed
// pool-token relationship. It is shared by voucher and offering discovery.
func voucherPoolPredicate(args *sqlArgs, filter CatalogFilter, voucherID string) string {
	if len(filter.PoolIDs) == 0 {
		return ""
	}
	return `EXISTS (
            SELECT 1 FROM pool_tokens discovery_pt
            WHERE discovery_pt.voucher_id = ` + voucherID + `
                AND discovery_pt.is_allowed
                AND discovery_pt.pool_id = ANY(` + args.bind(voucherIDsParam(filter.PoolIDs)) + `::BIGINT[])
        )`
}

// searchPredicate returns the displayed name/symbol match for the filter's term,
// or "" if there is no term. Voucher and pool labels may override their
// immutable contract names. Both fragments are constant text; only the term is
// bound.
func searchPredicate(args *sqlArgs, filter CatalogFilter, table string) string {
	if filter.Term == "" {
		return ""
	}

	placeholder := args.bind(filter.likeTerm())
	name := table + ".name"
	if table == "v" || table == "p" {
		name = "COALESCE(" + table + ".display_name, " + table + ".name)"
	}
	if filter.prefixOnly() {
		return "(lower(" + name + ") LIKE lower(" + placeholder + ") || '%' ESCAPE '\\'" +
			"\n            OR lower(" + table + ".symbol) LIKE lower(" + args.reuse() + ") || '%' ESCAPE '\\')"
	}
	return "(lower(" + name + ") LIKE '%' || lower(" + placeholder + ") || '%' ESCAPE '\\'" +
		"\n            OR lower(" + table + ".symbol) LIKE '%' || lower(" + args.reuse() + ") || '%' ESCAPE '\\')"
}

// voucherTagPredicate returns the voucher tag reducer, over both sources a
// voucher's tags can come from.
//
// A voucher carries tags two ways, and a filter that reads one of them is broken
// for the other:
//
//   - **voucher_tags**, curated directly. The deploy pipeline fills it —
//     `upsert-voucher-from-deploy` copies `deployed_token_tags` into it — and the
//     admin console's `PUT /admin/vouchers/:address/profile` edits it.
//   - **offering_tags**, derived from the tags on a voucher's *available*
//     offerings.
//
// This used to read only the derived form, on the grounds that voucher_tags was
// empty and filling it "would invent a curated classification nobody curates".
// That stopped being true once deploys began populating it: a voucher published
// with tags, or re-tagged by an operator, was stored correctly, read back
// correctly on its detail endpoint, and then matched nothing here — the tag
// control saved and changed nothing anybody could search by.
//
// The union is deliberately additive rather than a switch. Every voucher that
// matched under the derived form still matches, because that branch is unchanged;
// only directly-tagged vouchers are newly found. A straight move to voucher_tags
// would have unfound every voucher tagged solely through its offerings, which is
// most of the catalog.
//
// Both branches still drive from v.id outwards rather than from a tag junction
// inwards, which is what keeps the trigram index driving a search: starting from
// the junction made every plan a hash join over the whole table.
//
// COUNT(DISTINCT t.tag_value) over UNION ALL in the all-tags form is
// load-bearing twice over. A voucher may publish several available offerings
// carrying the same tag, and may also carry that tag directly, so a plain
// COUNT(*) would reach cardinality($1) from repeats of one tag and match a
// voucher holding only one of the requested tags.
func voucherTagPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.Tags) == 0 {
		return ""
	}

	placeholder := args.bind(filter.Tags)
	if filter.TagMatch == TagMatchAll {
		return `(
            SELECT COUNT(DISTINCT t.tag_value)
            FROM (
                SELECT vt.tag_value
                FROM voucher_tags vt
                WHERE vt.voucher_id = v.id
                UNION ALL
                SELECT ot.tag_value
                FROM offerings o
                INNER JOIN offering_tags ot ON ot.offering_id = o.id
                WHERE o.voucher_id = v.id AND o.available
            ) t
            WHERE t.tag_value = ANY(` + placeholder + `::TEXT[])
        ) = cardinality(` + args.reuse() + `::TEXT[])`
	}
	return `(EXISTS (
            SELECT 1
            FROM voucher_tags vt
            WHERE vt.voucher_id = v.id
                AND vt.tag_value = ANY(` + placeholder + `::TEXT[])
        ) OR EXISTS (
            SELECT 1
            FROM offerings o
            INNER JOIN offering_tags ot ON ot.offering_id = o.id
            WHERE o.voucher_id = v.id AND o.available
                AND ot.tag_value = ANY(` + args.reuse() + `::TEXT[])
        ))`
}

// poolTagPredicate returns the direct pool_tags reducer. Pools filter on their
// own curated tags, which is what the endpoint already returns per row, rather
// than on member-voucher or member-offering tags — a pool matching because
// some member voucher carries a tag is a different question, and the response
// has no field that would explain it. pool_tags' primary key gives one row per
// (pool, tag), so the all-tags form needs no DISTINCT.
func poolTagPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.Tags) == 0 {
		return ""
	}

	placeholder := args.bind(filter.Tags)
	if filter.TagMatch == TagMatchAll {
		return `(
            SELECT COUNT(*)
            FROM pool_tags pt
            WHERE pt.pool_id = p.id
                AND pt.tag_value = ANY(` + placeholder + `::TEXT[])
        ) = cardinality(` + args.reuse() + `::TEXT[])`
	}
	return `EXISTS (
            SELECT 1
            FROM pool_tags pt
            WHERE pt.pool_id = p.id
                AND pt.tag_value = ANY(` + placeholder + `::TEXT[])
        )`
}

// ownerPredicate restricts a page to one owning address. Compared case-folded
// because an address reaches the database from several sources — a checksummed
// deploy, a lowercased URL — and migration 018 indexes the same expression.
func ownerPredicate(args *sqlArgs, column string, filter CatalogFilter) string {
	if filter.Owner == "" {
		return ""
	}
	return "lower(" + column + ") = lower(" + args.bind(filter.Owner) + ")"
}

// cursorPredicate returns the keyset comparison for a page direction, over the
// given column. The operator comes from the typed CatalogPage, never from a
// request string.
func cursorPredicate(args *sqlArgs, column string, page CatalogPage, cursor uint64) string {
	switch page {
	case CatalogNextPage:
		return column + " < " + args.bind(cursor)
	case CatalogPrevPage:
		return column + " > " + args.bind(cursor)
	case CatalogFirstPage:
		return ""
	default:
		return ""
	}
}

// pageOrder is the inner ordering for a direction. A previous page collects the
// rows just above the cursor, so it scans ascending and the outer query
// restores the catalog's descending order.
func pageOrder(page CatalogPage) string {
	if page == CatalogPrevPage {
		return "ASC"
	}
	return "DESC"
}

// whereClause joins the non-empty predicates. Predicates are constant fragments
// plus placeholders, so the result contains no request data.
func whereClause(predicates ...string) string {
	kept := make([]string, 0, len(predicates))
	for _, predicate := range predicates {
		if predicate != "" {
			kept = append(kept, predicate)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "\n    WHERE " + strings.Join(kept, "\n        AND ")
}
