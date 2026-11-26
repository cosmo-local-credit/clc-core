# Region country presets

`region-countries.json.gz` is a local, versioned administrative baseline. CLC does not download country geometry during a policy read or write. Administrators can preview the dataset before activation and use a custom boundary where the preset is unsuitable.

- Source: [Natural Earth 1:10m Admin 0 map units, v5.1.2](https://github.com/nvkelso/natural-earth-vector/blob/v5.1.2/geojson/ne_10m_admin_0_map_units.geojson), SHA-256 `57da82be755f4afccd8f3b14251bb2752f5df1395f47d2d86f817470c4a48862`.
- [Public-domain data terms](https://www.naturalearthdata.com/about/terms-of-use/). The dataset reflects the source's default de facto boundary representation.
- Generation retains ISO-3166-1 alpha-3 codes (use `ISO_A3`, then `ISO_A3_EH` for a missing recognized code). Standard ISO country names label each code. GeoJSON units of the same code are dissolved with PostGIS `ST_UnaryUnion(ST_Collect(ST_MakeValid(...)))`, reduced to polygonal parts, and stored as WGS84 MultiPolygons. Export uses `ST_AsGeoJSON` with nine decimals and no CRS or bounding-box metadata. Sort by case-folded name and then code and gzip the compact UTF-8 JSON with a zero timestamp.
- The pinned catalog provides 248 ISO presets, including Bhutan. The US Minor Outlying Islands (`UMI`) lack a preset in this source. An unavailable preset is handled as a missing boundary, never another country's geometry.
- All 248 exported geometries were checked for validity and emptiness in PostGIS after serialization. The source boundary is not evidence of a user's current location or a legal border determination.
