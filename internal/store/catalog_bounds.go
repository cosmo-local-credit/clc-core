package store

import (
	"errors"
	"math"
	"slices"
)

const (
	CatalogMaxBounds = 2
)

var (
	ErrInvalidCatalogBounds = errors.New("bbox must contain valid minLng,minLat,maxLng,maxLat values with min <= max")
	ErrTooManyCatalogBounds = errors.New("bbox may be repeated at most twice")
	ErrInvalidCatalogSplit  = errors.New("two bbox values must be the matching halves of an antimeridian-crossing viewport")
)

// CatalogBounds is one non-wrapping geographic rectangle. A viewport crossing
// the antimeridian is represented by two rectangles so PostgreSQL can retain
// an indexable box predicate for each half.
type (
	CatalogBounds struct {
		MinLng float64
		MinLat float64
		MaxLng float64
		MaxLat float64
	}
)

func (b CatalogBounds) Validate() error {
	for _, value := range [...]float64{b.MinLng, b.MinLat, b.MaxLng, b.MaxLat} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidCatalogBounds
		}
	}
	if b.MinLng < -180 || b.MaxLng > 180 || b.MinLat < -90 || b.MaxLat > 90 ||
		b.MinLng > b.MaxLng || b.MinLat > b.MaxLat {
		return ErrInvalidCatalogBounds
	}
	return nil
}

func NormalizeCatalogBounds(raw []CatalogBounds) ([]CatalogBounds, error) {
	if len(raw) > CatalogMaxBounds {
		return nil, ErrTooManyCatalogBounds
	}
	bounds := slices.Clone(raw)
	for _, bound := range bounds {
		if err := bound.Validate(); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(bounds, func(left, right CatalogBounds) int {
		for _, pair := range [][2]float64{{left.MinLng, right.MinLng}, {left.MinLat, right.MinLat}, {left.MaxLng, right.MaxLng}, {left.MaxLat, right.MaxLat}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	bounds = slices.Compact(bounds)
	if len(bounds) == 2 {
		left, right := bounds[0], bounds[1]
		if left.MinLng != -180 || right.MaxLng != 180 ||
			left.MinLat != right.MinLat || left.MaxLat != right.MaxLat ||
			left.MaxLng >= right.MinLng {
			return nil, ErrInvalidCatalogSplit
		}
	}
	return bounds, nil
}

func catalogBoundsPredicate(args *sqlArgs, bounds []CatalogBounds, lat, lng string) string {
	if len(bounds) == 0 {
		return ""
	}
	predicates := make([]string, 0, len(bounds))
	for _, bound := range bounds {
		predicates = append(predicates, "point("+lng+", "+lat+") <@ box(point("+
			args.bind(bound.MinLng)+", "+args.bind(bound.MinLat)+"), point("+
			args.bind(bound.MaxLng)+", "+args.bind(bound.MaxLat)+"))")
	}
	if len(predicates) == 1 {
		return predicates[0]
	}
	return "(" + predicates[0] + " OR " + predicates[1] + ")"
}

// catalogBoundsQueryArgs expands the canonical one-or-two rectangle form for
// named SQL statements. Keeping the shape fixed lets PostgreSQL prepare and
// inspect the statements while still making the second antimeridian half
// optional.
func catalogBoundsQueryArgs(bounds []CatalogBounds) (bool, float64, float64, float64, float64, bool, float64, float64, float64, float64) {
	values := [2]CatalogBounds{}
	copy(values[:], bounds)
	return len(bounds) > 0,
		values[0].MinLng, values[0].MinLat, values[0].MaxLng, values[0].MaxLat,
		len(bounds) > 1,
		values[1].MinLng, values[1].MinLat, values[1].MaxLng, values[1].MaxLat
}
