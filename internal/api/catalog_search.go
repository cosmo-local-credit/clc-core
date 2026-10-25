package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Request parsing for catalog search and tag filtering.
//
// Everything here turns public strings into the store's typed filter. No parsed
// value ever reaches SQL as text: the term and the tag array cross as bound
// parameters, and `tagMatch` selects a compile-time constant fragment.

var (
	errInvalidFacets     = errors.New("facets must be true or false")
	errInvalidVoucherRef = errors.New(
		"vouchers must contain chainId:address values with a positive chain ID and a valid hex address")
	errUnknownVoucherRef = errors.New(
		"vouchers contains a voucher that does not exist in the catalog")
	errInvalidPoolRef = errors.New(
		"pools must contain chainId:address values with a positive chain ID and a valid hex address")
	errUnknownPoolRef = errors.New(
		"pools contains a pool that does not exist in the catalog")
	errReportTooManyPools = errors.New("pools must contain at most 12 values")
	errUnknownPoolMatch   = errors.New(`poolMatch must be "any" or "all"`)
	// The vocabulary is named as `facets=true` rather than as /api/v2/token-tags
	// because this endpoint is public and that one is not: a signed-out caller
	// can filter but cannot read the tag list from there. The facet response is
	// the source that is always available to whoever hit this error.
	//
	// The recovery has to say "without tags", and that is not a nicety. Tag
	// validation runs as the list's validate hook, which returns before the facet
	// extras execute, so retrying the same request with facets=true added fails
	// again on the same bad value and never reaches the vocabulary. Dropping the
	// tags is the only retry that can answer the question this error raises.
	errUnknownTagValue = errors.New(
		"tags contains a value outside the tag vocabulary; " +
			"retry without tags and with facets=true to read the valid values")
)

type (
	// catalogSearchRequest is the parsed search/filter portion of a catalog
	// list request.
	catalogSearchRequest struct {
		filter store.CatalogFilter
		// voucherRefs remain stable public identities until the handler's read
		// transaction resolves every one to a database ID.
		voucherRefs  []store.VoucherRef
		voucherMatch store.TagMatch
		poolRefs     []store.PoolRef
		poolMatch    store.TagMatch
		// facets asks for the tag facet alongside the page. It is honoured on
		// the first page only: a facet describes the whole filtered set rather
		// than the page, so recomputing it per cursor page would return the
		// same answer at the same cost.
		facets bool
		nearby bool
	}

	catalogRelationshipRef struct {
		chainID int64
		address string
	}
)

// parseCatalogSearch reads `q`, `tags`, `tagMatch`, `facets`, `bbox`, and
// `nearby`. A viewport may contain one ordinary box or two boxes split at the
// antimeridian. Bounds and legacy nearby discovery are mutually exclusive.
//
// `tags` accepts repetition (`tags=Art&tags=Culture`) and comma-separated
// values (`tags=Art,Culture`) interchangeably; no token_tag value contains a
// comma. Values are trimmed, deduplicated, and sorted by the store so
// equivalent requests produce identical parameters and identical cache keys.
func parseCatalogSearch(c *echo.Context) (catalogSearchRequest, error) {
	query := c.Request().URL.Query()

	var tags []string
	for _, raw := range query["tags"] {
		for value := range strings.SplitSeq(raw, ",") {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
	}

	var match store.TagMatch
	switch strings.ToLower(strings.TrimSpace(query.Get("tagMatch"))) {
	case "", "any":
		match = store.TagMatchAny
	case "all":
		match = store.TagMatchAll
	default:
		return catalogSearchRequest{}, store.ErrCatalogUnknownTagMatch
	}

	facets := false
	if raw := strings.TrimSpace(query.Get("facets")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return catalogSearchRequest{}, errInvalidFacets
		}
		facets = parsed
	}

	filter, err := store.NewCatalogFilter(query.Get("q"), tags, match)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	filter.Bounds, err = parseMarketBoundsValues(query["bbox"])
	if err != nil {
		return catalogSearchRequest{}, err
	}

	nearby := query.Get("nearby")
	if nearby != "" && nearby != "true" && nearby != "false" {
		return catalogSearchRequest{}, errors.New("nearby must be true or false")
	}
	if nearby == "true" && len(filter.Bounds) > 0 {
		return catalogSearchRequest{}, errors.New("nearby and bbox cannot be combined")
	}
	return catalogSearchRequest{filter: filter, facets: facets, nearby: nearby == "true"}, nil
}

// parseMarketDiscoverySearch adds the selected-pool relationship filter used
// by voucher and offering discovery. Pools are any-match and every reference
// is resolved before a query is allowed to run.
func parseMarketDiscoverySearch(c *echo.Context) (catalogSearchRequest, error) {
	search, err := parseCatalogSearch(c)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	query := c.Request().URL.Query()
	seen := make(map[string]struct{})
	for _, raw := range query["pools"] {
		for value := range strings.SplitSeq(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			chain, address, ok := strings.Cut(value, ":")
			chainID, parseErr := strconv.ParseInt(strings.TrimSpace(chain), 10, 64)
			address = strings.TrimSpace(address)
			if !ok || parseErr != nil || chainID <= 0 || !common.IsHexAddress(address) {
				return catalogSearchRequest{}, errInvalidPoolRef
			}
			canonical := common.HexToAddress(address).Hex()
			key := strconv.FormatInt(chainID, 10) + ":" + strings.ToLower(canonical)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			search.poolRefs = append(search.poolRefs, store.PoolRef{ChainID: chainID, Address: canonical})
		}
	}
	if len(search.poolRefs) > store.CatalogMaxPools {
		return catalogSearchRequest{}, store.ErrCatalogTooManyPools
	}
	slices.SortFunc(search.poolRefs, func(left, right store.PoolRef) int {
		if left.ChainID < right.ChainID {
			return -1
		}
		if left.ChainID > right.ChainID {
			return 1
		}
		return strings.Compare(strings.ToLower(left.Address), strings.ToLower(right.Address))
	})
	return search, nil
}

// parseCatalogRelationshipSearch extends the shared text/tag parser for entity
// lists that can be related to vouchers. The market voucher catalog keeps using
// parseCatalogSearch because asking a voucher list to contain a voucher has no
// defined meaning.
func parseCatalogRelationshipSearch(c *echo.Context) (catalogSearchRequest, error) {
	search, err := parseCatalogSearch(c)
	if err != nil {
		return catalogSearchRequest{}, err
	}

	refs, err := parseCatalogRelationshipRefs(
		c, "vouchers", store.CatalogMaxVouchers, errInvalidVoucherRef, store.ErrCatalogTooManyVouchers,
	)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	for _, ref := range refs {
		search.voucherRefs = append(search.voucherRefs, store.VoucherRef{
			ChainID: ref.chainID,
			Address: ref.address,
		})
	}
	search.voucherMatch, err = relationshipMatch(
		c.Request().URL.Query().Get("voucherMatch"), store.ErrCatalogUnknownVoucherMatch,
	)
	if err != nil {
		return catalogSearchRequest{}, err
	}

	return search, nil
}

// parseReportRelationshipSearch adds direct report-pool relationships without
// changing the meaning of pools on voucher discovery.
func parseReportRelationshipSearch(c *echo.Context) (catalogSearchRequest, error) {
	search, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		return catalogSearchRequest{}, err
	}

	refs, err := parseCatalogRelationshipRefs(
		c, "pools", store.CatalogMaxVouchers, errInvalidPoolRef, errReportTooManyPools,
	)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	for _, ref := range refs {
		search.poolRefs = append(search.poolRefs, store.PoolRef{
			ChainID: ref.chainID,
			Address: ref.address,
		})
	}
	search.poolMatch, err = relationshipMatch(c.Request().URL.Query().Get("poolMatch"), errUnknownPoolMatch)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	return search, nil
}

func parseCatalogRelationshipRefs(
	c *echo.Context,
	parameter string,
	limit int,
	invalid, tooMany error,
) ([]catalogRelationshipRef, error) {
	seen := make(map[string]struct{})
	refs := make([]catalogRelationshipRef, 0)
	for _, raw := range c.Request().URL.Query()[parameter] {
		for value := range strings.SplitSeq(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			chain, address, ok := strings.Cut(value, ":")
			chainID, parseErr := strconv.ParseInt(strings.TrimSpace(chain), 10, 64)
			address = strings.TrimSpace(address)
			if !ok || parseErr != nil || chainID <= 0 || !common.IsHexAddress(address) {
				return nil, invalid
			}
			canonical := common.HexToAddress(address).Hex()
			key := strconv.FormatInt(chainID, 10) + ":" + strings.ToLower(canonical)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, catalogRelationshipRef{chainID: chainID, address: canonical})
		}
	}
	if len(refs) > limit {
		return nil, tooMany
	}
	slices.SortFunc(refs, func(left, right catalogRelationshipRef) int {
		if left.chainID < right.chainID {
			return -1
		}
		if left.chainID > right.chainID {
			return 1
		}
		return strings.Compare(strings.ToLower(left.address), strings.ToLower(right.address))
	})
	return refs, nil
}

func relationshipMatch(raw string, unknown error) (store.TagMatch, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "any":
		return store.TagMatchAny, nil
	case "all":
		return store.TagMatchAll, nil
	default:
		return store.TagMatchAny, unknown
	}
}

// respondCatalogSearchError maps a filter validation failure to 400. The
// messages name the offending parameter and its rule but never echo the
// caller's own value back.
// addressFilterParam reads an optional address filter. Parsed per handler rather
// than in parseCatalogSearch: only the endpoints whose composer applies it accept
// one, so a caller cannot pass it somewhere it would be silently dropped.
func addressFilterParam(c *echo.Context, name string) (string, error) {
	raw := strings.TrimSpace(c.Request().URL.Query().Get(name))
	if raw == "" {
		return "", nil
	}
	if !common.IsHexAddress(raw) {
		return "", fmt.Errorf("%s must be a valid hex address", name)
	}
	return raw, nil
}

func respondCatalogSearchError(c *echo.Context, err error) error {
	if jErr := c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: err.Error(),
	}); jErr != nil {
		return jErr
	}
	return nil
}

// catalogTagFacetExtras returns the extras hook that attaches `tagFacets`, or
// nil when this request should not carry one. A facet describes the whole
// filtered set rather than the page, so it is computed for the first page only
// and the key is simply absent from cursor pages.
func catalogTagFacetExtras(
	search catalogSearchRequest,
	pagination paginationParams,
	load func(pgx.Tx) ([]store.TagFacet, error),
) func(pgx.Tx) (map[string]any, error) {
	if !search.facets || !pagination.FirstPage {
		return nil
	}

	return func(tx pgx.Tx) (map[string]any, error) {
		facets, err := load(tx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tagFacets": facets}, nil
	}
}

// rejectUnknownTags fails the request when any requested tag is outside the
// token_tag vocabulary. Dropping unknown values instead would answer a
// different question than the caller asked and would report facet counts for a
// filter that was never applied.
//
// It reports handled=true when it has already written a response.
func (a *API) rejectUnknownTags(c *echo.Context, tx pgx.Tx, tags []string) (bool, error) {
	if len(tags) == 0 {
		return false, nil
	}

	unknown, err := a.store.UnknownTokenTags(c.Request().Context(), tx, tags)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	if len(unknown) > 0 {
		return true, respondCatalogSearchError(c, errUnknownTagValue)
	}
	return false, nil
}

// resolveVoucherFilter converts the request's stable voucher identities to
// internal junction keys. A single missing reference fails the whole request;
// silently dropping it would broaden an any-match filter and make an all-match
// filter mean something different from what the caller selected.
func (a *API) resolveVoucherFilter(
	c *echo.Context,
	tx pgx.Tx,
	search *catalogSearchRequest,
) (bool, error) {
	if len(search.voucherRefs) == 0 {
		return false, nil
	}

	ids, err := a.store.ResolveVoucherRefs(c.Request().Context(), tx, search.voucherRefs)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	if len(ids) != len(search.voucherRefs) {
		return true, respondCatalogSearchError(c, errUnknownVoucherRef)
	}

	search.filter, err = search.filter.WithVoucherIDs(ids, search.voucherMatch)
	if err != nil {
		return true, respondCatalogSearchError(c, err)
	}
	return false, nil
}

// resolvePoolFilter turns stable pool references into authoritative database
// IDs. A short resolution fails the complete request rather than weakening it.
func (a *API) resolvePoolFilter(c *echo.Context, tx pgx.Tx, search *catalogSearchRequest) (bool, error) {
	if len(search.poolRefs) == 0 {
		return false, nil
	}
	ids, err := a.store.ResolvePoolRefs(c.Request().Context(), tx, search.poolRefs)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	if len(ids) != len(search.poolRefs) {
		return true, respondCatalogSearchError(c, errUnknownPoolRef)
	}
	search.filter, err = search.filter.WithPoolIDsMatch(ids, search.poolMatch)
	if err != nil {
		return true, respondCatalogSearchError(c, err)
	}
	return false, nil
}
