package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Request parsing for catalog search and tag filtering (PG_SEARCH_PLAN.md
// Slices 1 and 2).
//
// Everything here turns public strings into the store's typed filter. No parsed
// value ever reaches SQL as text: the term and the tag array cross as bound
// parameters, and `tagMatch` selects a compile-time constant fragment.

var (
	errInvalidFacets = errors.New("facets must be true or false")
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
		// facets asks for the tag facet alongside the page. It is honoured on
		// the first page only: a facet describes the whole filtered set rather
		// than the page, so recomputing it per cursor page would return the
		// same answer at the same cost.
		facets bool
	}
)

// parseCatalogSearch reads `q`, `tags`, `tagMatch`, and `facets`.
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

	return catalogSearchRequest{filter: filter, facets: facets}, nil
}

// respondCatalogSearchError maps a filter validation failure to 400. The
// messages name the offending parameter and its rule but never echo the
// caller's own value back.
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
