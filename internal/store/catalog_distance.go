package store

import (
	"errors"
	"math"
	"strconv"
)

const (
	// nearbyRadiusMeters is how far "nearby" reaches from the saved location.
	nearbyRadiusMeters = 1_000_000.0
	// earthRadiusMeters is the mean Earth radius the App also uses.
	earthRadiusMeters = 6371008.8
	// nearbyBoundsMargin widens the prefilter box so float rounding can never
	// exclude a point the exact distance test would accept.
	nearbyBoundsMargin = 1.001
)

type (
	// CatalogLocation is the saved viewer location, not a client-selected radius.
	CatalogLocation struct {
		Lat float64
		Lng float64
	}
)

func NewCatalogLocation(lat, lng *float64) (*CatalogLocation, error) {
	if lat == nil || lng == nil || math.IsNaN(*lat) || math.IsNaN(*lng) ||
		math.IsInf(*lat, 0) || math.IsInf(*lng, 0) || *lat < -90 || *lat > 90 || *lng < -180 || *lng > 180 {
		return nil, errors.New("a valid saved profile location is required")
	}
	return &CatalogLocation{Lat: *lat, Lng: *lng}, nil
}

// bounds is the smallest lat/lng box that contains every point within the
// nearby radius. It exists so the GiST point index can drive the query: the
// exact distance below is an expression no index can serve. A box that would
// cross a pole or the antimeridian spans every longitude instead, which is
// still correct because the distance test refines it.
func (l CatalogLocation) bounds() MarketVoucherBounds {
	angular := nearbyRadiusMeters / earthRadiusMeters * nearbyBoundsMargin
	dLat := angular * 180 / math.Pi
	box := MarketVoucherBounds{
		MinLat: math.Max(l.Lat-dLat, -90),
		MaxLat: math.Min(l.Lat+dLat, 90),
		MinLng: -180,
		MaxLng: 180,
	}
	if l.Lat-dLat <= -90 || l.Lat+dLat >= 90 {
		return box
	}
	dLng := math.Asin(math.Sin(angular)/math.Cos(l.Lat*math.Pi/180)) * 180 / math.Pi
	if l.Lng-dLng >= -180 && l.Lng+dLng <= 180 {
		box.MinLng, box.MaxLng = l.Lng-dLng, l.Lng+dLng
	}
	return box
}

// nearbyPredicate keeps rows within the nearby radius of the viewer: an
// indexable bounding box first, then the exact Haversine distance. CASE
// protects the trigonometric functions from invalid coordinates even if SQL
// reorders filters. Column expressions are compile-time constants; values are
// bound.
func nearbyPredicate(args *sqlArgs, location *CatalogLocation, lat, lng string) string {
	if location == nil {
		return ""
	}
	box := location.bounds()
	viewerLat := args.bind(location.Lat) + "::double precision"
	viewerLng := args.bind(location.Lng) + "::double precision"
	return "(point(" + lng + ", " + lat + ") <@ box(point(" +
		args.bind(box.MinLng) + ", " + args.bind(box.MinLat) + "), point(" +
		args.bind(box.MaxLng) + ", " + args.bind(box.MaxLat) + ")) AND " +
		"CASE WHEN " + lat + " BETWEEN -90 AND 90 AND " + lng + " BETWEEN -180 AND 180 THEN " +
		"2 * " + strconv.FormatFloat(earthRadiusMeters, 'f', -1, 64) + " * asin(sqrt(least(1.0, greatest(0.0, " +
		"power(sin(radians(" + lat + " - " + viewerLat + ") / 2), 2) + " +
		"cos(radians(" + viewerLat + ")) * cos(radians(" + lat + ")) * " +
		"power(sin(radians(" + lng + " - " + viewerLng + ") / 2), 2)" +
		")))) <= " + strconv.FormatFloat(nearbyRadiusMeters, 'f', -1, 64) + " ELSE false END)"
}
