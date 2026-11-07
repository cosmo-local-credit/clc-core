package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise the composed directory statements against a live database, so
// the SQL is proven to parse, to bind, and to answer the question the composition
// tests only prove it asks. Skipped unless CORE_TEST_DSN points at a database.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/
//
// The transaction is rolled back, so fixtures never persist. Rows seeded by other
// suites and the seeded superadmin are still visible, which is why nothing here
// asserts a total: every assertion is about accounts this test created, found
// through a term that occurs nowhere else, and pagination is asserted over the
// highest IDs in the table — the ones just inserted.

// seedDirectoryUser inserts one account inside the test transaction and returns
// it. Contact details go in through UpdateUserProfile because insert-user does
// not carry them.
func seedDirectoryUser(t *testing.T, tx pgx.Tx, pg *Pg, name, email, phone, status string) User {
	t.Helper()
	ctx := context.Background()

	// A per-row address that satisfies users_blockchain_address_format and is
	// unique per fixture. Derived from the fixture's own identity rather than a
	// counter so it does not depend on how many rows a test seeded before it.
	digest := sha256.Sum256([]byte(name + "|" + email))
	address := "0x" + hex.EncodeToString(digest[:20])

	inserted, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: address,
		Name:            name,
		Email:           email,
		Role:            "USER",
		Status:          status,
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	updated, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: name, Email: email, Phone: phone, Country: "KE",
	})
	require.NoError(t, err)
	require.Equal(t, inserted.ID, updated.ID)

	return updated
}

func TestListUsersFilteredMatchesIdentityAndContact(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// "zqmarker" occurs in one account's name, another's email and a third's
	// phone, so one term proves all three columns are searched.
	named := seedDirectoryUser(t, tx, pg, "Zqmarker Wanjiku", "wanjiku@example.test", "+254700000101", "APPROVED")
	emailed := seedDirectoryUser(t, tx, pg, "Achieng Otieno", "zqmarker@example.test", "+254700000102", "APPROVED")
	phoned := seedDirectoryUser(t, tx, pg, "Kamau Njoroge", "njoroge@example.test", "+2547000zqmarker", "PENDING")
	absent := seedDirectoryUser(t, tx, pg, "Nobody Here", "nobody@example.test", "+254700000104", "APPROVED")

	filter, err := NewUserFilter("zqmarker", "")
	require.NoError(t, err)

	found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
	require.NoError(t, err)

	ids := userIDs(found)
	assert.Contains(t, ids, named.ID, "a name must be searched")
	assert.Contains(t, ids, emailed.ID, "an email must be searched")
	assert.Contains(t, ids, phoned.ID, "a phone must be searched")
	assert.NotContains(t, ids, absent.ID, "an account matching nothing must not be returned")

	// Case folding runs on both sides of the comparison.
	upper, err := NewUserFilter("ZQMARKER", "")
	require.NoError(t, err)
	upperFound, err := pg.ListUsersFiltered(ctx, tx, upper, CatalogFirstPage, 0, 100)
	require.NoError(t, err)
	assert.ElementsMatch(t, ids, userIDs(upperFound), "search must be case-insensitive")
}

func TestListUsersFilteredMatchesAUsername(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// The marker lives only in the username. The display name, email and phone
	// are deliberately free of it, so a hit can only come from that column.
	named := seedDirectoryUser(t, tx, pg, "Handle Owner", "handle-owner@example.test", "+254700000151", "APPROVED")
	other := seedDirectoryUser(t, tx, pg, "Other Person", "other-person@example.test", "+254700000152", "PENDING")
	handle := "zqhandle"
	renamed, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: named.EthereumAddress, Name: named.Name, Email: named.Email, Phone: named.Phone,
		Country: "KE", Username: &handle,
	})
	require.NoError(t, err)
	require.NotNil(t, renamed.Username)
	require.Equal(t, handle, *renamed.Username)

	filter, err := NewUserFilter("zqhandle", "")
	require.NoError(t, err)
	found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
	require.NoError(t, err)
	ids := userIDs(found)
	assert.Contains(t, ids, named.ID, "a username must be searched")
	assert.NotContains(t, ids, other.ID, "an account whose username does not match must not be returned")

	// A pending account is still in the directory. The username index is not
	// limited to APPROVED rows, and neither is the predicate.
	pendingHandle := "zqpending"
	pending, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: other.EthereumAddress, Name: other.Name, Email: other.Email, Phone: other.Phone,
		Country: "KE", Username: &pendingHandle,
	})
	require.NoError(t, err)
	pendingFilter, err := NewUserFilter("zqpending", "PENDING")
	require.NoError(t, err)
	pendingFound, err := pg.ListUsersFiltered(ctx, tx, pendingFilter, CatalogFirstPage, 0, 100)
	require.NoError(t, err)
	assert.Contains(t, userIDs(pendingFound), pending.ID, "a pending username must be searchable")

	upper, err := NewUserFilter("ZQHANDLE", "")
	require.NoError(t, err)
	upperFound, err := pg.ListUsersFiltered(ctx, tx, upper, CatalogFirstPage, 0, 100)
	require.NoError(t, err)
	assert.Contains(t, userIDs(upperFound), named.ID, "username search must be case-insensitive")
}

func TestListUsersFilteredUsernameUsesTheTrigramIndex(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// The directory is small, so a sequential scan is the planner's honest
	// choice. Forcing it off is how 018 checked that the indexed expression is
	// the one the predicate asks for.
	_, err := tx.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS users_username_trgm_idx
		    ON users USING gin (lower(username) gin_trgm_ops);
		ANALYZE users;
		SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)

	filter, err := NewUserFilter("zqhandle", "")
	require.NoError(t, err)
	query := composeUserPage(filter, CatalogFirstPage, 0, 25)
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query.sql, query.args...)
	require.NoError(t, err)
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
	}
	require.NoError(t, rows.Err())
	assert.Contains(t, strings.Join(plan, "\n"), "users_username_trgm_idx")
}

func TestListUsersFilteredMatchesAPastedAddress(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	user := seedDirectoryUser(t, tx, pg, "Pasted Address", "pasted@example.test", "+254700000201", "APPROVED")

	// The console's operator pastes what the table showed them, checksum casing
	// included; the stored value and the search must agree regardless.
	for name, term := range map[string]string{
		"as stored":  user.EthereumAddress,
		"lowercased": strings.ToLower(user.EthereumAddress),
		"uppercased": "0X" + strings.ToUpper(strings.TrimPrefix(user.EthereumAddress, "0x")),
	} {
		filter, err := NewUserFilter(term, "")
		require.NoError(t, err, name)

		found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
		require.NoError(t, err, name)
		assert.Contains(t, userIDs(found), user.ID, "%s: a full address must find its account", name)
	}
}

func TestListUsersFilteredEscapesLikeMetacharacters(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	literal := seedDirectoryUser(t, tx, pg, "Zqpercent 50%", "zqpercent@example.test", "+254700000301", "APPROVED")
	other := seedDirectoryUser(t, tx, pg, "Zqpercent 50", "zqplain@example.test", "+254700000302", "APPROVED")

	// "50%" must match the literal percent sign, not "50 followed by anything".
	filter, err := NewUserFilter("Zqpercent 50%", "")
	require.NoError(t, err)

	found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
	require.NoError(t, err)

	ids := userIDs(found)
	assert.Contains(t, ids, literal.ID)
	assert.NotContains(t, ids, other.ID, "an unescaped % would widen the pattern to both rows")
}

func TestListUsersFilteredNarrowsByStatus(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	pending := seedDirectoryUser(t, tx, pg, "Zqstatus Pending", "zqstatus-p@example.test", "+254700000401", "PENDING")
	approved := seedDirectoryUser(t, tx, pg, "Zqstatus Approved", "zqstatus-a@example.test", "+254700000402", "APPROVED")
	rejected := seedDirectoryUser(t, tx, pg, "Zqstatus Rejected", "zqstatus-r@example.test", "+254700000403", "REJECTED")

	for name, tc := range map[string]struct {
		status  string
		want    uint64
		exclude []uint64
	}{
		"pending":  {status: "PENDING", want: pending.ID, exclude: []uint64{approved.ID, rejected.ID}},
		"approved": {status: "APPROVED", want: approved.ID, exclude: []uint64{pending.ID, rejected.ID}},
		"rejected": {status: "REJECTED", want: rejected.ID, exclude: []uint64{pending.ID, approved.ID}},
	} {
		// Combined with a term, because status and search have to compose: the
		// bug this guards is a direction or a reducer that silently drops the
		// other predicate.
		filter, err := NewUserFilter("zqstatus", tc.status)
		require.NoError(t, err, name)

		found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
		require.NoError(t, err, name)

		ids := userIDs(found)
		assert.Contains(t, ids, tc.want, name)
		for _, excluded := range tc.exclude {
			assert.NotContains(t, ids, excluded, name)
		}
	}
}

func TestListUsersFilteredNarrowsByAdministrativeRoles(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	admin := seedDirectoryUser(t, tx, pg, "Zqrole Admin", "zqrole-admin@example.test", "+254700000311", "APPROVED")
	superadmin := seedDirectoryUser(t, tx, pg, "Zqrole Super", "zqrole-super@example.test", "+254700000312", "APPROVED")
	ordinary := seedDirectoryUser(t, tx, pg, "Zqrole User", "zqrole-user@example.test", "+254700000313", "APPROVED")
	_, err := tx.Exec(ctx, `UPDATE users SET role='ADMIN' WHERE id=$1`, admin.ID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE users SET role='SUPERADMIN' WHERE id=$1`, superadmin.ID)
	require.NoError(t, err)

	filter, err := NewUserFilter("zqrole", "", "ADMIN,SUPERADMIN")
	require.NoError(t, err)
	found, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
	require.NoError(t, err)
	ids := userIDs(found)
	assert.Contains(t, ids, admin.ID)
	assert.Contains(t, ids, superadmin.ID)
	assert.NotContains(t, ids, ordinary.ID)
}

func TestAdministrativeRoleFilterUsesRoleIndex(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, err := tx.Exec(ctx, `
		INSERT INTO users (blockchain_address, name, email, role, status)
		SELECT '0x' || lpad(to_hex(700000 + g), 40, '0'),
		       'Zq role plan ' || g,
		       'zq-role-plan-' || g || '@example.test',
		       CASE WHEN g <= 10 THEN 'ADMIN'::user_role
		            WHEN g <= 20 THEN 'SUPERADMIN'::user_role
		            ELSE 'USER'::user_role END,
		       'APPROVED'::user_status
		FROM generate_series(1, 5000) AS g
		ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `ANALYZE users`)
	require.NoError(t, err)

	filter, err := NewUserFilter("", "", "ADMIN,SUPERADMIN")
	require.NoError(t, err)
	query := composeUserPage(filter, CatalogFirstPage, 0, 100)
	plan := explainGenericPlan(t, tx, query.sql)
	assert.Contains(t, plan, "users_role_id_idx", "role filtering must be index-backed\nplan:\n%s", plan)
}

func TestListUsersFilteredPagesInBothDirections(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Five accounts sharing a term nothing else matches, so the pages below are
	// exactly these rows in a known order: newest first, which is highest id
	// first.
	var seeded []uint64
	for i := range 5 {
		user := seedDirectoryUser(t, tx, pg,
			fmt.Sprintf("Zqpage Account %d", i),
			fmt.Sprintf("zqpage-%d@example.test", i),
			fmt.Sprintf("+2547000005%02d", i),
			"APPROVED")
		seeded = append(seeded, user.ID)
	}

	filter, err := NewUserFilter("zqpage", "")
	require.NoError(t, err)

	// Newest first: the last seeded account leads.
	descending := make([]uint64, len(seeded))
	for i, id := range seeded {
		descending[len(seeded)-1-i] = id
	}

	first, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogFirstPage, 0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, descending[:2], userIDs(first))

	next, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogNextPage, descending[1], 2)
	require.NoError(t, err)
	require.Len(t, next, 2)
	assert.Equal(t, descending[2:4], userIDs(next), "a next page continues below the cursor")

	// Stepping back from the third page must return the second, in the same
	// descending order the first page was served in.
	previous, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogPrevPage, descending[4], 2)
	require.NoError(t, err)
	require.Len(t, previous, 2)
	assert.Equal(t, descending[2:4], userIDs(previous), "a previous page is returned newest first")

	// The term still applies on a cursor page: no direction falls through to an
	// unfiltered read.
	last, err := pg.ListUsersFiltered(ctx, tx, filter, CatalogNextPage, descending[3], 2)
	require.NoError(t, err)
	assert.Equal(t, descending[4:], userIDs(last))
}

func TestListUsersFilteredUnfilteredPageIsBounded(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	for i := range 3 {
		seedDirectoryUser(t, tx, pg,
			fmt.Sprintf("Zqbound Account %d", i),
			fmt.Sprintf("zqbound-%d@example.test", i),
			fmt.Sprintf("+2547000006%02d", i),
			"APPROVED")
	}

	// The endpoint's whole reason for changing: an unfiltered read is a page now,
	// not the table. The limit has to bind on the shape that carries no search
	// predicate too.
	page, err := pg.ListUsersFiltered(ctx, tx, UserFilter{}, CatalogFirstPage, 0, 2)
	require.NoError(t, err)
	assert.Len(t, page, 2)
}

func userIDs(users []User) []uint64 {
	ids := make([]uint64, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.ID)
	}
	return ids
}
