# clc-core

![GitHub Tag](https://img.shields.io/github/v/tag/cosmo-local-credit/clc-core)

## PostgreSQL prerequisite for regional authority

Migration 086 requires PostGIS on the PostgreSQL server that hosts the Core
database. The local and CI service images include it. Before deploying, confirm
in the target Core database that PostGIS is available and enabled:

```sql
SELECT name, default_version, installed_version
FROM pg_available_extensions
WHERE name = 'postgis';
```

If there is no row, the database operator must install PostGIS for the server's
existing PostgreSQL major version. If `installed_version` is null and the
migration user cannot create extensions, have the operator run
`CREATE EXTENSION IF NOT EXISTS postgis` in that database before deployment.
Verify that the installed extension is visible to the Core database role with
`SELECT PostGIS_Full_Version();`. Keep existing staging and production database
volumes and major versions in place. Run the isolation and overlap tests against
a separate local PostGIS database with `CORE_TEST_DSN`.

## License

[AGPL-3.0](LICENSE).