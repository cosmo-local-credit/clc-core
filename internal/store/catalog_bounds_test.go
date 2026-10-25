package store

import (
	"errors"
	"testing"
)

func TestNormalizeCatalogBoundsCanonicalizesAndDeduplicates(t *testing.T) {
	raw := []CatalogBounds{
		{MinLng: 179, MinLat: -2, MaxLng: 180, MaxLat: 2},
		{MinLng: 179, MinLat: -2, MaxLng: 180, MaxLat: 2},
	}
	bounds, err := NormalizeCatalogBounds(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounds) != 1 || bounds[0].MinLng != 179 {
		t.Fatalf("canonical bounds = %#v", bounds)
	}
}

func TestNormalizeCatalogBoundsCanonicalizesAnAntimeridianSplit(t *testing.T) {
	bounds, err := NormalizeCatalogBounds([]CatalogBounds{
		{MinLng: 179, MinLat: -2, MaxLng: 180, MaxLat: 2},
		{MinLng: -180, MinLat: -2, MaxLng: -179, MaxLat: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bounds) != 2 || bounds[0].MinLng != -180 || bounds[1].MaxLng != 180 {
		t.Fatalf("canonical split = %#v", bounds)
	}
}

func TestNormalizeCatalogBoundsRejectsTwoUnrelatedBoxes(t *testing.T) {
	for _, bounds := range [][]CatalogBounds{
		{
			{MinLng: 0, MinLat: 0, MaxLng: 1, MaxLat: 1},
			{MinLng: 2, MinLat: 0, MaxLng: 3, MaxLat: 1},
		},
		{
			{MinLng: -180, MinLat: 0, MaxLng: -170, MaxLat: 1},
			{MinLng: 170, MinLat: -1, MaxLng: 180, MaxLat: 1},
		},
		{
			{MinLng: -180, MinLat: 0, MaxLng: 175, MaxLat: 1},
			{MinLng: 170, MinLat: 0, MaxLng: 180, MaxLat: 1},
		},
	} {
		if _, err := NormalizeCatalogBounds(bounds); !errors.Is(err, ErrInvalidCatalogSplit) {
			t.Fatalf("NormalizeCatalogBounds(%#v) error = %v, want ErrInvalidCatalogSplit", bounds, err)
		}
	}
}

func TestNormalizeCatalogBoundsRejectsMoreThanTwoValuesEvenWhenDuplicated(t *testing.T) {
	duplicate := CatalogBounds{MinLng: 36, MinLat: -2, MaxLng: 37, MaxLat: -1}
	_, err := NormalizeCatalogBounds([]CatalogBounds{duplicate, duplicate, duplicate})
	if !errors.Is(err, ErrTooManyCatalogBounds) {
		t.Fatalf("error = %v, want ErrTooManyCatalogBounds", err)
	}
}

func TestNormalizeCatalogBoundsRejectsInvalidCoordinates(t *testing.T) {
	for _, bounds := range [][]CatalogBounds{
		{{MinLng: -181, MinLat: 0, MaxLng: 1, MaxLat: 1}},
		{{MinLng: 2, MinLat: 0, MaxLng: 1, MaxLat: 1}},
		{{MinLng: 0, MinLat: 91, MaxLng: 1, MaxLat: 91}},
	} {
		if _, err := NormalizeCatalogBounds(bounds); !errors.Is(err, ErrInvalidCatalogBounds) {
			t.Fatalf("NormalizeCatalogBounds(%#v) error = %v", bounds, err)
		}
	}
}

func TestNormalizeCatalogBoundsRejectsMoreThanTwoDistinctBoxes(t *testing.T) {
	_, err := NormalizeCatalogBounds([]CatalogBounds{
		{MinLng: 0, MinLat: 0, MaxLng: 1, MaxLat: 1},
		{MinLng: 1, MinLat: 0, MaxLng: 2, MaxLat: 1},
		{MinLng: 2, MinLat: 0, MaxLng: 3, MaxLat: 1},
	})
	if !errors.Is(err, ErrTooManyCatalogBounds) {
		t.Fatalf("error = %v, want ErrTooManyCatalogBounds", err)
	}
}
