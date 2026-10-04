package store

import (
	"errors"
	"math"
)

// CatalogLocation is the saved viewer location, not a client-selected radius.
type CatalogLocation struct {
	Lat float64
	Lng float64
}

func NewCatalogLocation(lat, lng *float64) (*CatalogLocation, error) {
	if lat == nil || lng == nil || math.IsNaN(*lat) || math.IsNaN(*lng) ||
		math.IsInf(*lat, 0) || math.IsInf(*lng, 0) || *lat < -90 || *lat > 90 || *lng < -180 || *lng > 180 {
		return nil, errors.New("a valid saved profile location is required")
	}
	return &CatalogLocation{Lat: *lat, Lng: *lng}, nil
}

// Haversine uses the same mean Earth radius as the App. CASE protects the
// trigonometric functions from invalid coordinates even if SQL reorders filters.
// Column expressions below are compile-time constants; values are bound.
func nearbyPredicate(args *sqlArgs, location *CatalogLocation, lat, lng string) string {
	if location == nil {
		return ""
	}
	viewerLat := args.bind(location.Lat) + "::double precision"
	viewerLng := args.bind(location.Lng) + "::double precision"
	return "CASE WHEN " + lat + " BETWEEN -90 AND 90 AND " + lng + " BETWEEN -180 AND 180 THEN " +
		"2 * 6371008.8 * asin(sqrt(least(1.0, greatest(0.0, " +
		"power(sin(radians(" + lat + " - " + viewerLat + ") / 2), 2) + " +
		"cos(radians(" + viewerLat + ")) * cos(radians(" + lat + ")) * " +
		"power(sin(radians(" + lng + " - " + viewerLng + ") / 2), 2)" +
		")))) <= 1000000 ELSE false END"
}
