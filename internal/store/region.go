package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

type (
	RegionOverlapError struct{ Name string }

	RegionStore interface {
		ListRegions(context.Context, RegionActor, bool) ([]MarketplaceRegion, error)
		SaveRegion(context.Context, pgx.Tx, RegionActor, int64, RegionWrite) (MarketplaceRegion, error)
		SetRegionAssignment(context.Context, pgx.Tx, RegionActor, int64, string, bool, bool) (MarketplaceRegion, error)
		RegionsCoveringPoint(context.Context, pgx.Tx, float64, float64) ([]RegionReference, error)
	}

	RegionActor struct {
		ID      uint64
		Address string
	}

	RegionReference struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	RegionAssignment struct {
		Address  string  `json:"address"`
		Username *string `json:"username"`
	}

	MarketplaceRegion struct {
		RegionReference

		CountryCode    *string            `json:"countryCode"`
		Active         bool               `json:"active"`
		Revision       int64              `json:"revision"`
		Boundary       json.RawMessage    `json:"boundary"`
		PoolApproval   bool               `json:"poolApproval"`
		AssignedAdmins []RegionAssignment `json:"assignedAdmins"`
	}

	RegionWrite struct {
		Name        string
		CountryCode *string
		Boundary    json.RawMessage
		Active      bool
		Revision    int64
	}

	regionQueryer interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}
)

const (
	regionBoundaryLimit = 4 * 1024 * 1024
	regionPointLimit    = 200_000
)

var (
	ErrRegionAuthority  = errors.New("global administrator access is required")
	ErrRegionBoundary   = errors.New("invalid boundary; use a two-dimensional WGS84 Polygon or MultiPolygon, split at the antimeridian")
	ErrRegionStale      = errors.New("the region has changed; reload it before editing")
	ErrRegionInactive   = errors.New("activate this region before assigning an administrator")
	ErrRegionAssignee   = errors.New("only approved active accounts can be assigned")
	ErrRegionConversion = errors.New("a Superadmin must confirm conversion of a Global Admin")
	ErrRegionSuperadmin = errors.New("a Superadmin cannot be assigned as a Regional Admin")
)

func (e *RegionOverlapError) Error() string {
	return fmt.Sprintf("the boundary overlaps active region %q", e.Name)
}

func (pg *Pg) listRegions(ctx context.Context, db regionQueryer, actor RegionActor, global bool, id int64) ([]MarketplaceRegion, error) {
	rows, err := db.Query(ctx, pg.queries.RegionalListRegions, actor.ID, global, id, actor.Address)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (MarketplaceRegion, error) {
		var r MarketplaceRegion
		err := row.Scan(&r.ID, &r.Name, &r.CountryCode, &r.Active, &r.Revision, &r.Boundary, &r.AssignedAdmins)
		return r, err
	})
}

func (pg *Pg) ListRegions(ctx context.Context, actor RegionActor, global bool) ([]MarketplaceRegion, error) {
	return pg.listRegions(ctx, pg.db, actor, global, 0)
}

func (pg *Pg) updatedRegion(ctx context.Context, tx pgx.Tx, actor RegionActor, id int64) (MarketplaceRegion, error) {
	results, err := pg.listRegions(ctx, tx, actor, true, id)
	if err != nil {
		return MarketplaceRegion{}, err
	}
	if len(results) == 0 {
		return MarketplaceRegion{}, pgx.ErrNoRows
	}
	return results[0], nil
}

func (pg *Pg) lockRegionAuthority(ctx context.Context, tx pgx.Tx, actor RegionActor) (string, error) {
	if _, err := tx.Exec(ctx, pg.queries.RegionalAuthorityLock); err != nil {
		return "", err
	}
	var role string
	err := tx.QueryRow(ctx, pg.queries.RegionalAuthorityLockActor, actor.ID, actor.Address).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !IsAdministrativeRole(role)) {
		return "", ErrRegionAuthority
	}
	return role, err
}

func (pg *Pg) SaveRegion(ctx context.Context, tx pgx.Tx, actor RegionActor, id int64, input RegionWrite) (MarketplaceRegion, error) {
	if err := validateRegionBoundary(input.Boundary); err != nil {
		return MarketplaceRegion{}, err
	}
	if _, err := pg.lockRegionAuthority(ctx, tx, actor); err != nil {
		return MarketplaceRegion{}, err
	}

	var previousActive bool
	var previousRevision int64
	if id != 0 {
		if err := tx.QueryRow(ctx, pg.queries.RegionalLockRegion, id).Scan(&previousActive, &previousRevision); err != nil {
			return MarketplaceRegion{}, err
		}
		if input.Revision != previousRevision {
			return MarketplaceRegion{}, ErrRegionStale
		}
	}

	var valid bool
	if err := tx.QueryRow(ctx, pg.queries.RegionalCheckBoundary, input.Boundary).Scan(&valid); err != nil {
		return MarketplaceRegion{}, err
	}
	if !valid {
		return MarketplaceRegion{}, ErrRegionBoundary
	}

	if input.Active {
		var overlap string
		err := tx.QueryRow(ctx, pg.queries.RegionalFindOverlap, input.Boundary, id).Scan(&overlap)
		if err == nil {
			return MarketplaceRegion{}, &RegionOverlapError{Name: overlap}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return MarketplaceRegion{}, err
		}
	}

	event := "UPDATED"
	revision := previousRevision + 1
	if id == 0 {
		event = "CREATED"
		if err := tx.QueryRow(ctx, pg.queries.RegionalInsertRegion, input.Name, input.CountryCode, input.Active, input.Boundary).Scan(&id); err != nil {
			return MarketplaceRegion{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, pg.queries.RegionalUpdateRegion, id, input.Name, input.CountryCode, input.Active, input.Boundary); err != nil {
			return MarketplaceRegion{}, err
		}
	}
	if previousActive && !input.Active {
		if err := pg.removeInactiveRegionAssignments(ctx, tx, id, actor.ID, revision); err != nil {
			return MarketplaceRegion{}, err
		}
	}
	if _, err := tx.Exec(ctx, pg.queries.RegionalInsertEvent, id, actor.ID, nil, event, revision); err != nil {
		return MarketplaceRegion{}, err
	}
	return pg.updatedRegion(ctx, tx, actor, id)
}

func (pg *Pg) removeInactiveRegionAssignments(ctx context.Context, tx pgx.Tx, id int64, actorID uint64, revision int64) error {
	rows, err := tx.Query(ctx, pg.queries.RegionalClearAssignments, id)
	if err != nil {
		return err
	}
	assignees, err := pgx.CollectRows(rows, pgx.RowTo[uint64])
	if err != nil {
		return err
	}
	for _, subjectID := range assignees {
		if _, err := tx.Exec(ctx, pg.queries.RegionalReturnUnassignedToUser, subjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, pg.queries.RegionalInsertEvent, id, actorID, subjectID, "REMOVED", revision); err != nil {
			return err
		}
	}
	return nil
}

func (pg *Pg) SetRegionAssignment(ctx context.Context, tx pgx.Tx, actor RegionActor, id int64, address string, assigned, confirmConversion bool) (MarketplaceRegion, error) {
	actorRole, err := pg.lockRegionAuthority(ctx, tx, actor)
	if err != nil {
		return MarketplaceRegion{}, err
	}
	var regionActive bool
	var revision int64
	if err := tx.QueryRow(ctx, pg.queries.RegionalLockRegion, id).Scan(&regionActive, &revision); err != nil {
		return MarketplaceRegion{}, err
	}
	var targetID uint64
	var targetRole string
	var eligible bool
	if err := tx.QueryRow(ctx, pg.queries.RegionalAuthorityLockAssignee, address).Scan(&targetID, &targetRole, &eligible); err != nil {
		return MarketplaceRegion{}, err
	}
	event := "REMOVED"
	query := pg.queries.RegionalRemoveAssignment
	if assigned {
		if !regionActive {
			return MarketplaceRegion{}, ErrRegionInactive
		}
		if !eligible {
			return MarketplaceRegion{}, ErrRegionAssignee
		}
		if IsSuperadminRole(targetRole) {
			return MarketplaceRegion{}, ErrRegionSuperadmin
		}
		if targetRole == string(UserRoleAdmin) && (!IsSuperadminRole(actorRole) || !confirmConversion) {
			return MarketplaceRegion{}, ErrRegionConversion
		}
		event = "ASSIGNED"
		query = pg.queries.RegionalSetAssignments
	}
	command, err := tx.Exec(ctx, query, id, targetID)
	if err != nil {
		return MarketplaceRegion{}, err
	}
	if command.RowsAffected() > 0 {
		roleQuery := pg.queries.RegionalReturnUnassignedToUser
		if assigned {
			roleQuery = pg.queries.RegionalSetRole
		}
		if _, err := tx.Exec(ctx, roleQuery, targetID); err != nil {
			return MarketplaceRegion{}, err
		}
		if _, err := tx.Exec(ctx, pg.queries.RegionalInsertEvent, id, actor.ID, targetID, event, revision); err != nil {
			return MarketplaceRegion{}, err
		}
	}
	return pg.updatedRegion(ctx, tx, actor, id)
}

func (pg *Pg) RegionsCoveringPoint(ctx context.Context, tx pgx.Tx, lat, lng float64) ([]RegionReference, error) {
	if !validCoordinate(lat, lng) {
		return nil, ErrRegionBoundary
	}
	// A point on the antimeridian can be represented as either +180 or -180.
	rows, err := tx.Query(ctx, pg.queries.RegionalCoveringRegions, lng, lat)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RegionReference, error) {
		var r RegionReference
		err := row.Scan(&r.ID, &r.Name)
		return r, err
	})
}

func validCoordinate(lat, lng float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lng) && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// Accept only canonical, antimeridian-split GeoJSON geometries. PostGIS checks
// polygon topology after this bounded input validation.
func validateRegionBoundary(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > regionBoundaryLimit {
		return ErrRegionBoundary
	}
	var geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
		CRS         json.RawMessage `json:"crs"`
	}
	if json.Unmarshal(raw, &geometry) != nil || len(geometry.CRS) != 0 {
		return ErrRegionBoundary
	}

	var polygons [][][][]*float64
	switch geometry.Type {
	case "Polygon":
		var polygon [][][]*float64
		if json.Unmarshal(geometry.Coordinates, &polygon) != nil {
			return ErrRegionBoundary
		}
		polygons = [][][][]*float64{polygon}
	case "MultiPolygon":
		if json.Unmarshal(geometry.Coordinates, &polygons) != nil {
			return ErrRegionBoundary
		}
	default:
		return ErrRegionBoundary
	}

	if len(polygons) == 0 {
		return ErrRegionBoundary
	}
	points := 0
	for _, polygon := range polygons {
		if len(polygon) == 0 {
			return ErrRegionBoundary
		}
		for _, ring := range polygon {
			points += len(ring)
			if len(ring) < 4 || points > regionPointLimit {
				return ErrRegionBoundary
			}
			for i, position := range ring {
				if len(position) != 2 || position[0] == nil || position[1] == nil || !validCoordinate(*position[1], *position[0]) {
					return ErrRegionBoundary
				}
				if i > 0 && math.Abs(*position[0]-*ring[i-1][0]) > 180 {
					return ErrRegionBoundary
				}
			}
			first, last := ring[0], ring[len(ring)-1]
			if *first[0] != *last[0] || *first[1] != *last[1] {
				return ErrRegionBoundary
			}
		}
	}
	return nil
}
