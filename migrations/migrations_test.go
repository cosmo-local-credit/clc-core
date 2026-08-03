package migrations

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaselineMigrationExists(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	found := false
	for _, e := range entries {
		if e.Name() == "001_init.sql" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a 001_init.sql baseline migration in migrations/")
}

func TestMigrationFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var sqlFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles = append(sqlFiles, e.Name())
		}
	}

	assert.Contains(t, sqlFiles, "001_init.sql")
	assert.Contains(t, sqlFiles, "002_unify_deploy_registry.sql")
	assert.Contains(t, sqlFiles, "003_constraint_hardening.sql")
	assert.Contains(t, sqlFiles, "004_timestamptz.sql")
	assert.Contains(t, sqlFiles, "005_rename_protocol_addresses.sql")
	assert.Contains(t, sqlFiles, "006_network_catalog.sql")
	assert.Contains(t, sqlFiles, "007_pool_token_oracle_feeds.sql")
	assert.Contains(t, sqlFiles, "008_market_voucher_indexes.sql")
	assert.Contains(t, sqlFiles, "009_report_feed_index.sql")
	assert.Contains(t, sqlFiles, "010_report_body_normalization.sql")
	assert.Contains(t, sqlFiles, "011_catalog_search_indexes.sql")
	assert.Contains(t, sqlFiles, "012_report_search_indexes.sql")
	assert.Contains(t, sqlFiles, "013_voucher_relationship_filter_indexes.sql")
	for _, name := range sqlFiles {
		assert.True(t,
			name == "001_init.sql" ||
				name == "002_unify_deploy_registry.sql" ||
				name == "003_constraint_hardening.sql" ||
				name == "004_timestamptz.sql" ||
				name == "005_rename_protocol_addresses.sql" ||
				name == "006_network_catalog.sql" ||
				name == "007_pool_token_oracle_feeds.sql" ||
				name == "008_market_voucher_indexes.sql" ||
				name == "009_report_feed_index.sql" ||
				name == "010_report_body_normalization.sql" ||
				name == "011_catalog_search_indexes.sql" ||
				name == "012_report_search_indexes.sql" ||
				name == "013_voucher_relationship_filter_indexes.sql",
			"unexpected migration file %s", name,
		)
	}
}

func TestMarketVoucherIndexesMigration(t *testing.T) {
	content, err := os.ReadFile("008_market_voucher_indexes.sql")
	require.NoError(t, err)

	s := string(content)
	// Turns the per-page lateral kind lookup into an index-only scan.
	assert.Contains(t, s, "offerings_available_voucher_type_idx")
	assert.Contains(t, s, "ON offerings (voucher_id, commodity_type)")
	assert.Contains(t, s, "WHERE available")
	// Core PostgreSQL GiST point_ops — PostGIS is deliberately not required.
	assert.Contains(t, s, "vouchers_geo_gist_idx")
	assert.Contains(t, s, "USING gist (point(geo_lng, geo_lat))")
	assert.Contains(t, s, "WHERE active AND geo_lat IS NOT NULL")
	// No PostGIS dependency: no extension, no geometry/geography types.
	assert.NotContains(t, s, "CREATE EXTENSION")
	assert.NotContains(t, s, "geography(")
	assert.NotContains(t, s, "geometry(")
	assert.NotContains(t, s, "gist_geometry_ops")
	// Re-running the migration must be a no-op.
	assert.Equal(t, 2, strings.Count(s, "CREATE INDEX IF NOT EXISTS"))
	// The primary key already drives the unbounded descending page scan.
	assert.NotContains(t, s, "ON vouchers (id")
}

func TestBaselineMigrationContainsLegacySchema(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_tokens")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_deploy_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS protocol_addresses")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS pool_deploy_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_pools")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS oracle_feeds")
	assert.Contains(t, s, "POOL_DEPLOY_STEP")
	assert.Contains(t, s, "POOL_PROXY_DEPLOY")
	assert.Contains(t, s, "TOKEN_UNIQUE_SYMBOL_INDEX")
	assert.Contains(t, s, "LIMITER")
	assert.Contains(t, s, "FEE_POLICY")
	assert.Contains(t, s, "ORACLE_QUOTER")
	assert.Contains(t, s, "RELATIVE_QUOTER")
	assert.Contains(t, s, "token_name")
	assert.Contains(t, s, "token_symbol")
	assert.Contains(t, s, "admin")
	assert.Contains(t, s, "UNIQUE (otx_id)")
	assert.Contains(t, s, "REFERENCES otx(id)")
	assert.Contains(t, s, "REFERENCES platform_contracts(id)")
	assert.Contains(t, s, "protocol_fee_controller")
	assert.Contains(t, s, "decimal_quoter")
	assert.Contains(t, s, "fees_decoupled")
	assert.Contains(t, s, "quoter_address")
	assert.Contains(t, s, "limiter_address")
	assert.Contains(t, s, "token_registry_address")
	assert.Contains(t, s, "fee_policy_address")
	assert.Contains(t, s, "SWAPPOOL")
	assert.Contains(t, s, "name       TEXT NOT NULL UNIQUE")
	assert.Contains(t, s, "address    TEXT NOT NULL")
	assert.Contains(t, s, "created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP")
	assert.Contains(t, s, "updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP")
	assert.Contains(t, s, "CREATE TRIGGER update_oracle_feeds_timestamp")
	assert.Contains(t, s, "BEFORE UPDATE ON oracle_feeds")
	assert.Contains(t, s, "update_timestamp()")
	assert.Contains(t, s, "INSERT INTO oracle_feeds (name, address) VALUES")
	assert.Contains(t, s, "('USDT', '0x5e37AF40A7A344ec9b03CCD34a250F3dA9a20B02')")
	assert.Contains(t, s, "('KES',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F')")
	assert.Contains(t, s, "ON CONFLICT (name) DO UPDATE SET")
	assert.Contains(t, s, "updated_at = CURRENT_TIMESTAMP")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS upgrade_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_tag")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_deploy_meta_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_token_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS pool_deploy_meta_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_pool_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS webauthn_credentials")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS users")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS user_deployments")
	assert.Contains(t, s, "CONSTRAINT keystore_public_key_key UNIQUE (public_key)")
	assert.Contains(t, s, "RELAY_DELEGATE")
	assert.Contains(t, s, "RELAY_EXEC")
	assert.Contains(t, s, "RELAY_DELEGATE_EXEC")
	assert.Contains(t, s, "ACCOUNT_REGISTER")
	assert.Contains(t, s, "blockchain_address")
	assert.Contains(t, s, "CONSTRAINT users_email_unique UNIQUE (email)")
	assert.Contains(t, s, "CONSTRAINT users_phone_unique UNIQUE (phone)")
	assert.Contains(t, s, "CONSTRAINT users_at_least_one_contact CHECK (email IS NOT NULL OR phone IS NOT NULL)")
	assert.Contains(t, s, "user_role")
	assert.Contains(t, s, "user_status")
	assert.Contains(t, s, "SUPERADMIN")
	// Seed superadmin is stored in canonical EIP-55 checksummed form.
	assert.Contains(t, s, "0x33a573149db22e759fB9A38Bcc461C12855c2645")
	assert.NotContains(t, s, "0x33a573149db22e759fb9a38bcc461c12855c2645")
}

func TestUnifyDeployRegistryMigration(t *testing.T) {
	content, err := os.ReadFile("002_unify_deploy_registry.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deploy_status_type")
	assert.Contains(t, s, "('PENDING')")
	assert.Contains(t, s, "('CONFIRMED')")
	assert.Contains(t, s, "('FAILED')")
	assert.Contains(t, s, "SET status = 'CONFIRMED'")
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS decimals")
	assert.Contains(t, s, "ALTER COLUMN address DROP NOT NULL")
	assert.Contains(t, s, "FROM token_deploy_meta m")
	assert.Contains(t, s, "FROM pool_deploy_meta m")
	assert.Contains(t, s, "INSERT INTO deployed_token_tags")
	assert.Contains(t, s, "INSERT INTO deployed_pool_tags")
	assert.Contains(t, s, "DROP TABLE IF EXISTS token_deploy_meta")
	assert.Contains(t, s, "DROP TABLE IF EXISTS pool_deploy_meta")
	assert.Contains(t, s, "m.pool_decimals")
}

func TestConstraintHardeningMigration(t *testing.T) {
	content, err := os.ReadFile("003_constraint_hardening.sql")
	require.NoError(t, err)

	s := string(content)
	// 1a: one mutable dispatch row per OTX.
	assert.Contains(t, s, "ALTER COLUMN otx_id SET NOT NULL")
	assert.Contains(t, s, "ADD CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id)")
	// 1b: master key FK + DB-enforced singleton.
	assert.Contains(t, s, "ADD CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id)")
	assert.Contains(t, s, "CREATE UNIQUE INDEX IF NOT EXISTS master_key_singleton_idx")
	// 1c: nonce hardening.
	assert.Contains(t, s, "ALTER COLUMN next_nonce TYPE BIGINT")
	assert.Contains(t, s, "CHECK (next_nonce >= 0)")
	assert.Contains(t, s, "ALTER COLUMN nonce TYPE BIGINT")
	assert.Contains(t, s, "CHECK (nonce >= 0)")
	// 1d: domain checks on the post-002 unified registries.
	assert.Contains(t, s, "deployed_tokens_decimals_range")
	assert.Contains(t, s, "deployed_pools_decimals_range")
	assert.Contains(t, s, "CHECK (decimals BETWEEN 0 AND 255)")
	assert.Contains(t, s, "users_quotas_nonneg")
	assert.Contains(t, s, "deployed_tokens_geo_valid")
	assert.Contains(t, s, "deployed_pools_geo_valid")
	assert.Contains(t, s, "deployed_tokens_confirmed_has_address")
	assert.Contains(t, s, "deployed_pools_confirmed_has_address")
	assert.NotContains(t, s, "token_deploy_meta")
	assert.NotContains(t, s, "pool_deploy_meta")
	// 1e: indexes duplicated by unique constraints.
	assert.Contains(t, s, "DROP INDEX IF EXISTS public_key_idx")
	assert.Contains(t, s, "DROP INDEX IF EXISTS users_blockchain_address_idx")
	assert.Contains(t, s, "DROP INDEX IF EXISTS user_deployments_tracking_id_idx")
	// 2a: seeded superadmin canonicalized to EIP-55.
	assert.Contains(t, s, "SET blockchain_address = '0x33a573149db22e759fB9A38Bcc461C12855c2645'")
	// 2b: case-insensitive uniqueness backstops + shape checks.
	assert.Contains(t, s, "ON keystore (lower(public_key))")
	assert.Contains(t, s, "ON users (lower(blockchain_address))")
	assert.Contains(t, s, "ON deployed_tokens (lower(address))")
	assert.Contains(t, s, "ON deployed_pools (lower(address))")
	for _, name := range []string{
		"keystore_public_key_format",
		"users_blockchain_address_format",
		"deployed_tokens_address_format",
		"deployed_pools_address_format",
		"upgrade_meta_address_format",
		"platform_contracts_address_format",
		"pool_addresses_address_format",
		"protocol_addresses_address_format",
		"oracle_feeds_address_format",
		"webauthn_address_format",
	} {
		assert.Contains(t, s, name)
	}
	assert.Contains(t, s, `~ '^0x[0-9a-fA-F]{40}$'`)
	// Stored values stay EIP-55 checksummed — never force lowercase storage.
	assert.NotContains(t, s, "= lower(address)")
	assert.NotContains(t, s, "= lower(blockchain_address)")
}

func TestTimestamptzMigration(t *testing.T) {
	content, err := os.ReadFile("004_timestamptz.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "SET LOCAL TIME ZONE 'UTC'")
	assert.Contains(t, s, "TYPE TIMESTAMPTZ")
	// An explicit USING clause forces a full table rewrite even at UTC.
	assert.NotContains(t, s, "USING")
	// SET LOCAL depends on tern's default per-migration transaction.
	assert.NotContains(t, s, "disable-tx")
	for _, tbl := range []string{
		"keystore", "master_key", "noncestore", "otx", "dispatch",
		"upgrade_meta", "platform_contracts", "deployed_tokens",
		"protocol_addresses", "deployed_pools", "oracle_feeds",
		"webauthn_credentials",
	} {
		assert.Contains(t, s, "ALTER TABLE "+tbl)
	}
	// Upgrade path still mentions the old name until 005 renames.
	assert.Contains(t, s, "pool_addresses")
}

func TestRenameProtocolAddressesMigration(t *testing.T) {
	content, err := os.ReadFile("005_rename_protocol_addresses.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "RENAME TO protocol_addresses")
	assert.Contains(t, s, "protocol_addresses_address_format")
	assert.Contains(t, s, "update_protocol_addresses_timestamp")
}

func TestPoolTokenOracleFeedsMigration(t *testing.T) {
	content, err := os.ReadFile("007_pool_token_oracle_feeds.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "DROP COLUMN IF EXISTS exchange_rate")
	assert.Contains(t, s, "DROP CONSTRAINT IF EXISTS pool_tokens_rate_nonneg")
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS oracle_feed_id INT")
	assert.Contains(t, s, "FOREIGN KEY (oracle_feed_id) REFERENCES oracle_feeds(id)")
	assert.Contains(t, s, "pool_tokens_oracle_feed_id_idx")
}

func TestNetworkCatalogMigration(t *testing.T) {
	content, err := os.ReadFile("006_network_catalog.sql")
	require.NoError(t, err)

	s := string(content)
	// Catalog + content tables (MIGRATION.md §1–§5).
	for _, tbl := range []string{
		"voucher_type", "vouchers", "voucher_tags",
		"pools", "pool_tags", "pool_tokens",
		"report_status_type", "reports",
		"report_vouchers", "report_tags", "report_verifiers",
		"commodity_type", "offering_frequency", "offerings", "offering_tags",
	} {
		assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS "+tbl+" (")
	}
	// On-chain identity: (chain_id, address) unique + lower() backstop.
	assert.Contains(t, s, "UNIQUE (chain_id, address)")
	assert.Contains(t, s, "ON vouchers (chain_id, lower(address))")
	assert.Contains(t, s, "ON pools (chain_id, lower(address))")
	// Deploy pipeline links to the catalog; no fake pipeline rows for imports.
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS voucher_id INT UNIQUE REFERENCES vouchers(id)")
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS pool_id INT UNIQUE REFERENCES pools(id)")
	assert.NotContains(t, s, "otx")
	// Identity: user default voucher is a column on users (§1 I6).
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS default_voucher_id INT REFERENCES vouchers(id)")
	// Pool default voucher is a pool fact (§3 P7), hard FK to the catalog.
	assert.Regexp(t, `default_voucher_id\s+INT REFERENCES vouchers\(id\)`, s)
	// Composition cache is a labelled snapshot (§3 P9/P13).
	assert.Contains(t, s, "synced_at")
	assert.Contains(t, s, "pool_tokens_rate_nonneg")
	assert.Contains(t, s, "pool_tokens_limit_nonneg")
	// Shared tag vocabulary + per-entity junctions; no polymorphic taggings.
	assert.Contains(t, s, "REFERENCES token_tag(value)")
	assert.Contains(t, s, "('Regenerative Agriculture')")
	assert.NotContains(t, s, "entity_type")
	// Lookup seeds.
	for _, seed := range []string{
		"('DEMURRAGE')", "('GIFTABLE_EXPIRING')", "('UNKNOWN')",
		"('DRAFT')", "('SUBMITTED')", "('APPROVED')", "('REJECTED')",
		"('GOOD')", "('SERVICE')",
		"('day')", "('week')", "('month')", "('year')",
	} {
		assert.Contains(t, s, seed)
	}
	// Report constraints (§4.2).
	assert.Contains(t, s, "body             JSONB NOT NULL")
	assert.Contains(t, s, "reports_period_valid")
	assert.Contains(t, s, "reports_rejection_when_rejected")
	assert.Contains(t, s, "reports_rejection_only_if_rejected")
	assert.Contains(t, s, "reports_author_format")
	// Offering constraints (§5.2).
	assert.Contains(t, s, "offerings_seller_format")
	assert.Contains(t, s, "offerings_price_nonneg")
	assert.Contains(t, s, "offerings_quantity_nonneg")
	// Addresses stay EIP-55 checksummed — never force lowercase storage.
	assert.NotContains(t, s, "= lower(address)")
	// No data movement inside tern (importer is a separate command).
	assert.NotContains(t, s, "FROM pool_router")
	assert.NotContains(t, s, "FROM sarafu_network")
}

// The baseline carries the network catalog (same DDL as 006) so fresh
// databases create it from 001 and 006's guarded DDL no-ops. The registry
// link columns stay 006-only (002's status/decimals pattern) for identical
// column order on both paths.
func TestBaselineCarriesNetworkCatalog(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS voucher_type",
		"CREATE TABLE IF NOT EXISTS vouchers",
		"CREATE TABLE IF NOT EXISTS voucher_tags",
		"CREATE TABLE IF NOT EXISTS pools",
		"CREATE TABLE IF NOT EXISTS pool_tags",
		"CREATE TABLE IF NOT EXISTS pool_tokens",
		"CREATE TABLE IF NOT EXISTS report_status_type",
		"CREATE TABLE IF NOT EXISTS reports",
		"CREATE TABLE IF NOT EXISTS report_vouchers",
		"CREATE TABLE IF NOT EXISTS report_tags",
		"CREATE TABLE IF NOT EXISTS report_verifiers",
		"CREATE TABLE IF NOT EXISTS commodity_type",
		"CREATE TABLE IF NOT EXISTS offering_frequency",
		"CREATE TABLE IF NOT EXISTS offerings",
		"CREATE TABLE IF NOT EXISTS offering_tags",
		"vouchers_chain_address_lower_key",
		"pools_chain_address_lower_key",
		"vouchers_geo_valid",
		"pools_geo_valid",
		"pools_fee_ppm_range",
		"offerings_price_nonneg",
		"ADD COLUMN IF NOT EXISTS default_voucher_id INT REFERENCES vouchers(id)",
		"('Regenerative Agriculture')",
	} {
		assert.Contains(t, s, want)
	}
	// Registry link columns must not be folded into 001.
	assert.NotContains(t, s, "voucher_id INT UNIQUE")
	assert.NotContains(t, s, "pool_id INT UNIQUE")
}

// The baseline carries the post-010 report shape so a fresh database gets the
// normalized tables from 001 and 010's guarded DDL no-ops. Column order must
// match what 010 leaves on an upgraded database or the two paths diverge.
func TestBaselineCarriesNormalizedReports(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS report_media_kind",
		"CREATE TABLE IF NOT EXISTS report_images",
		"CREATE TABLE IF NOT EXISTS report_attachments",
		"body             TEXT NOT NULL DEFAULT ''",
		"report_images_url_unique UNIQUE (report_id, url)",
		"report_images_url_http",
		"report_attachments_url_http",
		"reports_form_fields_not_blank",
		"kind      TEXT NOT NULL REFERENCES report_media_kind(value)",
	} {
		assert.Contains(t, s, want)
	}
	// The dropped columns must be gone from the reports table specifically —
	// vouchers, pools and offerings keep their own location_name.
	start := strings.Index(s, "CREATE TABLE IF NOT EXISTS reports (")
	require.NotEqual(t, -1, start, "reports table not found in baseline")
	reportsBlock := s[start : start+strings.Index(s[start:], "\n);")]
	assert.NotContains(t, reportsBlock, "location_name")
	assert.NotContains(t, reportsBlock, "rejection_reason")
	assert.NotContains(t, reportsBlock, "JSONB")
	assert.NotContains(t, s, "reports_rejection_when_rejected")
	assert.NotContains(t, s, "reports_rejection_only_if_rejected")

	// Form columns are last, matching where ADD COLUMN puts them in 010.
	assert.Regexp(t,
		`updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,\n(\s*--[^\n]*\n)*\s*host_name        TEXT,\n\s*participants     TEXT,\n\s*time_taken       TEXT,`,
		reportsBlock)
}

func TestReportBodyNormalizationMigration(t *testing.T) {
	content, err := os.ReadFile("010_report_body_normalization.sql")
	require.NoError(t, err)

	s := string(content)
	// The write-time parser, shared by this migration and the importer.
	for _, fn := range []string{
		"report_md_inline", "report_md_list", "report_md_block",
		"report_body_markdown", "report_body_images",
		"report_body_attachments", "report_body_form",
	} {
		assert.Contains(t, s, "CREATE OR REPLACE FUNCTION "+fn)
	}
	// ALTER ... TYPE ... USING keeps `body` in place; add-drop-rename would move
	// it and break column-order parity with the 001 path.
	assert.Contains(t, s, "ALTER COLUMN body TYPE TEXT USING report_body_markdown(body)")
	assert.NotContains(t, s, "RENAME COLUMN")
	// Data movement is guarded on `body` still being JSONB so the migration is a
	// no-op on a fresh database, where 001 already created the final shape.
	assert.Contains(t, s, "AND data_type = 'jsonb'")
	// Both dropped columns, and rejection's two CHECKs which reference it.
	assert.Contains(t, s, "DROP COLUMN IF EXISTS location_name")
	assert.Contains(t, s, "DROP COLUMN IF EXISTS rejection_reason")
	assert.Contains(t, s, "DROP CONSTRAINT IF EXISTS reports_rejection_when_rejected")
	assert.Contains(t, s, "DROP CONSTRAINT IF EXISTS reports_rejection_only_if_rejected")
	// Only http(s) media is ever stored, so no request re-filters data URIs.
	assert.Contains(t, s, "report_images_url_http")
	assert.Contains(t, s, "report_attachments_url_http")
	// Re-running must not double-insert.
	assert.Contains(t, s, "ON CONFLICT DO NOTHING")
	// The form backfill must not fire update_reports_timestamp: that BEFORE
	// UPDATE trigger overrides updated_at, which would stamp every row carrying
	// a form with the migration's run time — and the feed orders by updated_at.
	assert.Contains(t, s, "DISABLE TRIGGER update_reports_timestamp")
	assert.Contains(t, s, "ENABLE TRIGGER update_reports_timestamp")
}

// The baseline carries the hardened constraints inline (same names as 003)
// so fresh databases get them from 001 and 003's guarded ALTERs no-op.
func TestBaselineCarriesHardenedConstraints(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	for _, want := range []string{
		"CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id)",
		"CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id)",
		"master_key_singleton_idx",
		"BIGINT NOT NULL DEFAULT 0",
		"CONSTRAINT noncestore_next_nonce_nonneg CHECK (next_nonce >= 0)",
		"CONSTRAINT otx_nonce_nonneg CHECK (nonce >= 0)",
		"keystore_public_key_format",
		"users_blockchain_address_format",
		"users_quotas_nonneg",
		"deployed_tokens_address_format",
		"deployed_tokens_geo_valid",
		"deployed_pools_address_format",
		"deployed_pools_geo_valid",
		"upgrade_meta_address_format",
		"platform_contracts_address_format",
		"protocol_addresses_address_format",
		"oracle_feeds_address_format",
		"webauthn_address_format",
		"keystore_public_key_lower_key",
		"users_blockchain_address_lower_key",
		"deployed_tokens_address_lower_key",
		"deployed_pools_address_lower_key",
	} {
		assert.Contains(t, s, want)
	}
	// Indexes duplicated by unique constraints must not come back.
	assert.NotContains(t, s, "public_key_idx ON keystore")
	assert.NotContains(t, s, "users_blockchain_address_idx")
	assert.NotContains(t, s, "user_deployments_tracking_id_idx")
}

func TestReportFeedIndexMigration(t *testing.T) {
	content, err := os.ReadFile("009_report_feed_index.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "reports_status_updated_idx")
	// Column order must match the feed's keyset predicate so neither the first
	// page nor a cursor page needs a sort.
	assert.Contains(t, s, "ON reports (status, updated_at DESC, id DESC)")
}

func TestCatalogSearchIndexesMigration(t *testing.T) {
	content, err := os.ReadFile("011_catalog_search_indexes.sql")
	require.NoError(t, err)

	s := string(content)

	// pg_trgm, not a text_pattern_ops B-tree: pgx may execute these statements
	// from a cached generic plan, which cannot derive a B-tree range from a
	// parameterized LIKE.
	assert.Contains(t, s, "CREATE EXTENSION IF NOT EXISTS pg_trgm")
	// Exactly one extension, and it is the trusted one a database owner can
	// install without superuser. Distance is a later slice: earthdistance is not
	// trusted, and PostGIS would need an image change.
	assert.Equal(t, 1, strings.Count(s, "CREATE EXTENSION"))
	assert.NotContains(t, s, "CREATE EXTENSION IF NOT EXISTS earthdistance")
	assert.NotContains(t, s, "CREATE EXTENSION IF NOT EXISTS cube")
	assert.NotContains(t, s, "CREATE EXTENSION IF NOT EXISTS postgis")
	assert.NotContains(t, s, "geography(")
	assert.NotContains(t, s, "geometry(")

	// Trigram indexes on the lowered expressions the search actually queries.
	for _, want := range []string{
		"vouchers_active_name_trgm_idx",
		"ON vouchers USING gin (lower(name) gin_trgm_ops)",
		"vouchers_active_symbol_trgm_idx",
		"ON vouchers USING gin (lower(symbol) gin_trgm_ops)",
		"pools_name_trgm_idx",
		"ON pools USING gin (lower(name) gin_trgm_ops)",
		"pools_symbol_trgm_idx",
		"ON pools USING gin (lower(symbol) gin_trgm_ops)",
	} {
		assert.Contains(t, s, want)
	}
	// The voucher indexes are partial on the catalog's visibility predicate, so
	// they hold only rows a search can return. Pools carry no such flag.
	assert.Equal(t, 2, strings.Count(s, "WHERE active"))

	// Reverse tag indexes: every junction's primary key is entity-first, which
	// cannot serve tag-first filtering or facet aggregation.
	for _, want := range []string{
		"ON offering_tags (tag_value, offering_id)",
		"ON voucher_tags (tag_value, voucher_id)",
		"ON pool_tags (tag_value, pool_id)",
		"ON report_tags (tag_value, report_id)",
	} {
		assert.Contains(t, s, want)
	}

	// Re-running the migration must be a no-op.
	assert.Equal(t, 8, strings.Count(s, "CREATE INDEX IF NOT EXISTS"))
	assert.NotContains(t, s, "CREATE INDEX CONCURRENTLY")

	// Expression-index statistics only exist once the index does, and the
	// planner needs them to cost lower(name) against a trigram index at all.
	assert.Contains(t, s, "ANALYZE vouchers")
	assert.Contains(t, s, "ANALYZE pools")

	// tern wraps each migration in a transaction, which CREATE INDEX
	// CONCURRENTLY cannot join; the tables are small enough not to need it.
	assert.NotContains(t, s, "disable-tx")
}

// The report search indexes are partial on the feed's own visibility predicate, so the index
// holds only rows a search can return. Title and description are both indexed; the body is
// deliberately not — it is normalized into a separate structure and needs a different shape.
func TestReportSearchIndexesMigration(t *testing.T) {
	content, err := os.ReadFile("012_report_search_indexes.sql")
	require.NoError(t, err)

	s := string(content)

	for _, want := range []string{
		"reports_approved_title_trgm_idx",
		"reports_approved_description_trgm_idx",
	} {
		assert.Contains(t, s, want)
	}

	// Trigram GIN, not a B-tree: a generic plan cannot derive a B-tree range from
	// `lower(title) LIKE lower($1) || '%'`.
	assert.Contains(t, s, "gin (lower(title) gin_trgm_ops)")
	assert.Contains(t, s, "gin (lower(description) gin_trgm_ops)")
	// Scoped to what the feed can return.
	assert.Contains(t, s, "WHERE status = 'APPROVED'")
	// Expression-index statistics do not exist until ANALYZE has run against the index.
	assert.Contains(t, s, "ANALYZE reports;")
	// The reverse junction index already exists from 011; re-creating it here would be noise.
	assert.NotContains(t, s, "CREATE INDEX IF NOT EXISTS report_tags_tag_report_idx")
}

func TestVoucherRelationshipFilterIndexesMigration(t *testing.T) {
	content, err := os.ReadFile("013_voucher_relationship_filter_indexes.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "ON report_vouchers (voucher_id, report_id)")
	assert.Contains(t, s, "ON pool_tokens (voucher_id, pool_id)")
	assert.Contains(t, s, "WHERE is_allowed")
	assert.Equal(t, 2, strings.Count(s, "CREATE INDEX IF NOT EXISTS"))
	assert.NotContains(t, s, "CREATE INDEX CONCURRENTLY")
	assert.Contains(t, s, "ANALYZE report_vouchers")
	assert.Contains(t, s, "ANALYZE pool_tokens")
}
