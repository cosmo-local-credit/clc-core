package api

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

type (
	regionCountryName struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	regionCountryPreset struct {
		regionCountryName

		Boundary json.RawMessage `json:"boundary"`
	}
)

var (
	//go:embed data/region-countries.json.gz
	regionCountryArchive []byte

	readRegionCountries = sync.OnceValues(func() ([]regionCountryPreset, error) {
		gz, err := gzip.NewReader(bytes.NewReader(regionCountryArchive))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		var countries []regionCountryPreset
		if err := json.NewDecoder(gz).Decode(&countries); err != nil {
			return nil, err
		}
		return countries, nil
	})
)

func lookupRegionCountry(code string) (regionCountryPreset, bool, error) {
	countries, err := readRegionCountries()
	if err != nil {
		return regionCountryPreset{}, false, err
	}
	for _, country := range countries {
		if country.Code == code {
			return country, true, nil
		}
	}
	return regionCountryPreset{}, false, nil
}

func (a *API) listRegionCountries(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	countries, err := readRegionCountries()
	if err != nil {
		return err
	}
	names := make([]regionCountryName, len(countries))
	for i, country := range countries {
		names[i] = country.regionCountryName
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Result: map[string]any{"countries": names},
	})
}

func (a *API) getRegionCountry(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	country, found, err := lookupRegionCountry(strings.ToUpper(c.Param("code")))
	if err != nil {
		return err
	}
	if !found {
		return echo.NewHTTPError(http.StatusNotFound, "Country boundary unavailable")
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Result: map[string]any{"country": country},
	})
}
