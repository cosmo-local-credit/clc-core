package store

import (
	"context"
	"testing"
)

func TestRegionCoverageUsesSpatialIndex(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Keep the fixture selective with non-overlapping tenth-degree polygons.
	// An indexed test is meaningful only once scanning the table costs more.
	_, err := tx.Exec(ctx, `
		INSERT INTO marketplace_regions (name, active, boundary)
		SELECT 'plan ' || x || ',' || y, true,
			ST_Multi(ST_MakeEnvelope(
				-135.0 + x * 0.1, -28.0 + y * 0.1,
				-135.0 + x * 0.1 + 0.05, -28.0 + y * 0.1 + 0.05, 4326
			))
		FROM generate_series(0, 99) x CROSS JOIN generate_series(0, 99) y
	`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "ANALYZE marketplace_regions"); err != nil {
		t.Fatal(err)
	}
	references, err := pg.RegionsCoveringPoint(ctx, tx, -24.7625, -130.063)
	if err != nil || len(references) != 1 || references[0].Name != "plan 49,32" {
		t.Fatalf("fractional coordinate should select exactly one region, got %+v (%v)", references, err)
	}
	assertIndexDrives(t, "covering regions", explainGenericPlan(t, tx, pg.queries.RegionalCoveringRegions), "marketplace_regions_active_boundary_idx")
	assertIndexDrives(t, "overlap check", explainGenericPlan(t, tx, pg.queries.RegionalFindOverlap), "marketplace_regions_active_boundary_idx")
}
