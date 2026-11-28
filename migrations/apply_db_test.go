package migrations

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
	"github.com/stretchr/testify/require"
)

// Core applies every migration when it starts and exits if one fails, so a
// migration the deployed PostgreSQL cannot run puts Core into a restart loop.
// Staging and CI use the stock postgres image. It ships contrib extensions
// such as pg_trgm, but not PostGIS or other third-party extensions.
//
// Add an extension to deployedExtensions only after every deployed database
// provides it.
var (
	deployedExtensions = map[string]bool{
		"pg_trgm": true,
	}
	createExtension = regexp.MustCompile(`(?i)\bCREATE\s+EXTENSION\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z0-9_]+)"?`)
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
)

func TestMigrationsUseDeployedExtensions(t *testing.T) {
	files, err := migrate.FindMigrations(os.DirFS("."))
	require.NoError(t, err)

	for _, name := range files {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		up, _, _ := strings.Cut(string(raw), "---- create above / drop below ----")
		for _, match := range createExtension.FindAllStringSubmatch(sqlLineComment.ReplaceAllString(up, ""), -1) {
			require.Truef(t, deployedExtensions[strings.ToLower(match[1])],
				"%s creates extension %q, which deployed databases do not provide; install it on every deployed database before adding it to deployedExtensions",
				name, match[1])
		}
	}
}

func TestDeployedExtensionPatternFindsExtensions(t *testing.T) {
	sql := sqlLineComment.ReplaceAllString(`-- CREATE EXTENSION is repeated here.
CREATE EXTENSION IF NOT EXISTS postgis;
create extension "cube";`, "")
	var names []string
	for _, match := range createExtension.FindAllStringSubmatch(sql, -1) {
		names = append(names, match[1])
	}
	require.Equal(t, []string{"postgis", "cube"}, names)
}

// TestMigrationsApplyToFreshDatabaseDB runs the full sequence in a new
// database on the CORE_TEST_DSN server, as Core does on first start. Point it
// at the same PostgreSQL image as staging so a migration that staging cannot
// apply fails here instead of at deploy time.
func TestMigrationsApplyToFreshDatabaseDB(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping fresh database migration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	name := fmt.Sprintf("clc_migrations_%d", time.Now().UnixNano())
	database := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+database)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	})

	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	defer conn.Close(ctx)

	var serverVersion string
	require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&serverVersion))
	t.Logf("applying migrations on PostgreSQL %s", serverVersion)

	migrator, err := migrate.NewMigrator(ctx, conn, "schema_version")
	require.NoError(t, err)
	require.NoError(t, migrator.LoadMigrations(os.DirFS(".")))
	require.NotEmpty(t, migrator.Migrations)
	require.NoError(t, migrator.Migrate(ctx))

	version, err := migrator.GetCurrentVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(len(migrator.Migrations)), version)
}
