package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type domainMembershipFixture struct {
	user                 User
	kosta, other, global uint64
	profile              UserProfileUpdate
}

func newDomainMembershipFixture(t *testing.T, pg *Pg, tx pgx.Tx) domainMembershipFixture {
	t.Helper()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: randomAddress(t, tx), Name: "Before onboarding", Email: uuid.NewString() + "@example.test", Role: "USER", Status: "APPROVED", MaxPools: 10, MaxTokens: 20})
	require.NoError(t, err)
	global, err := pg.GetGlobalDomain(ctx, tx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE domain_hosts SET status = 'active', verified_at = CURRENT_TIMESTAMP WHERE domain_id = $1`, global.ID)
	require.NoError(t, err)
	fixture := domainMembershipFixture{user: user, kosta: insertDomainFixture(t, tx, "kosta"), other: insertDomainFixture(t, tx, "other"), global: global.ID,
		profile: UserProfileUpdate{Name: "Ana", Email: user.Email, Country: "BA"}}
	for _, id := range []uint64{fixture.kosta, fixture.other} {
		_, err = tx.Exec(ctx, `INSERT INTO domain_hosts (domain_id, hostname, status, verified_at) VALUES ($1, $2, 'active', CURRENT_TIMESTAMP)`, id, uuid.NewString()+".example")
		require.NoError(t, err)
	}
	return fixture
}

func domainAuditCount(t *testing.T, tx pgx.Tx, userID uint64) int {
	t.Helper()
	var count int
	require.NoError(t, tx.QueryRow(context.Background(), `SELECT count(*) FROM domain_audit_events WHERE actor_key = $1`, fmt.Sprintf("user:%d", userID)).Scan(&count))
	return count
}

func TestDomainMembershipOnboardingAndFallback(t *testing.T) {
	for _, otherJoined := range []bool{false, true} {
		t.Run(fmt.Sprintf("other-membership-%t", otherJoined), func(t *testing.T) {
			pg, tx, cleanup := newCatalogSearchTestPg(t)
			defer cleanup()
			ctx := context.Background()
			f := newDomainMembershipFixture(t, pg, tx)
			state, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
			require.NoError(t, err)
			require.NotNil(t, state.RegistrationDomainID)
			assert.Equal(t, f.kosta, *state.RegistrationDomainID)
			assert.EqualValues(t, 1, state.DomainRevision)
			assert.False(t, state.GlobalRequired)
			assert.Len(t, state.Memberships, 1)
			assert.Equal(t, "active", state.membershipStatus(f.kosta))
			if otherJoined {
				state, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
				require.NoError(t, err)
			}
			revision := state.DomainRevision
			count := domainAuditCount(t, tx, f.user.ID)
			state, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, revision)
			require.NoError(t, err)
			assert.Equal(t, revision+1, state.DomainRevision)
			assert.True(t, state.GlobalRequired)
			assert.Equal(t, "left", state.membershipStatus(f.kosta))
			assert.Equal(t, "active", state.membershipStatus(f.global))
			assert.Equal(t, count+2, domainAuditCount(t, tx, f.user.ID))
			if otherJoined {
				assert.Equal(t, "active", state.membershipStatus(f.other))
			}
			retry, err := pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, state.DomainRevision)
			require.NoError(t, err)
			assert.Equal(t, state, retry)
			assert.Equal(t, count+2, domainAuditCount(t, tx, f.user.ID))
			_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.global, state.DomainRevision)
			require.ErrorIs(t, err, ErrGlobalDomainRequired)
			state, err = pg.JoinDomain(ctx, tx, f.user.ID, f.kosta, state.DomainRevision)
			require.NoError(t, err)
			assert.False(t, state.GlobalRequired)
			assert.Equal(t, "active", state.membershipStatus(f.global), "rejoining does not automatically remove Sarafu")
			state, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.global, state.DomainRevision)
			require.NoError(t, err)
			assert.Equal(t, "left", state.membershipStatus(f.global))
			user, err := pg.GetUserByID(ctx, tx, f.user.ID)
			require.NoError(t, err)
			assert.Equal(t, "Ana", user.Name)
			assert.Equal(t, f.user.EthereumAddress, user.EthereumAddress)
			assert.Equal(t, f.user.MaxPools, user.MaxPools)
			assert.Equal(t, f.user.MaxTokens, user.MaxTokens)
		})
	}
}

func TestDomainMembershipRetriesAndRegistration(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	f := newDomainMembershipFixture(t, pg, tx)
	_, err := pg.JoinDomain(ctx, tx, f.user.ID, f.other, 0)
	require.ErrorIs(t, err, ErrDomainOnboardingRequired)
	// The caller-supplied address cannot update a different wallet's profile.
	f.profile.Address = "0x1111111111111111111111111111111111111111"
	state, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
	require.NoError(t, err)
	count := domainAuditCount(t, tx, f.user.ID)
	f.profile.Name = "Retry must not overwrite"
	retry, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, state.DomainRevision, f.profile)
	require.NoError(t, err)
	assert.Equal(t, state, retry)
	assert.Equal(t, count, domainAuditCount(t, tx, f.user.ID))
	_, err = pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.other, state.DomainRevision, f.profile)
	require.ErrorIs(t, err, ErrDomainRegistrationConflict)
	_, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, 0)
	require.ErrorIs(t, err, ErrDomainRevisionConflict)
	state, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.NoError(t, err)
	retry, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.NoError(t, err)
	assert.Equal(t, state, retry)
	assert.Equal(t, f.kosta, *state.RegistrationDomainID)
	state, err = pg.JoinDomain(ctx, tx, f.user.ID, f.global, state.DomainRevision)
	require.NoError(t, err)
	count = domainAuditCount(t, tx, f.user.ID)
	state, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, state.DomainRevision)
	require.NoError(t, err)
	assert.Equal(t, count+1, domainAuditCount(t, tx, f.user.ID), "already-active Sarafu needs no extra join event")
	retry, err = pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, state.DomainRevision, f.profile)
	require.NoError(t, err)
	assert.Equal(t, state, retry, "onboarding replay must not undo a deliberate leave")
	user, err := pg.GetUserByID(ctx, tx, f.user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Ana", user.Name)
}

func TestDomainMembershipFallbackFailures(t *testing.T) {
	for _, failure := range []string{"disabled-host", "missing-host", "suspended-global", "injected-write-failure"} {
		t.Run(failure, func(t *testing.T) {
			pg, tx, cleanup := newCatalogSearchTestPg(t)
			defer cleanup()
			ctx := context.Background()
			f := newDomainMembershipFixture(t, pg, tx)
			_, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
			require.NoError(t, err)
			switch failure {
			case "disabled-host":
				_, err = tx.Exec(ctx, `UPDATE domain_hosts SET status = 'disabled' WHERE domain_id = $1`, f.global)
			case "missing-host":
				_, err = tx.Exec(ctx, `DELETE FROM domain_hosts WHERE domain_id = $1`, f.global)
			case "suspended-global":
				_, err = tx.Exec(ctx, `INSERT INTO user_domains (user_id, domain_id, status) VALUES ($1, $2, 'suspended')`, f.user.ID, f.global)
			case "injected-write-failure":
				_, err = tx.Exec(ctx, `CREATE FUNCTION pg_temp.fail_global_join() RETURNS trigger LANGUAGE plpgsql AS $$
     BEGIN IF (SELECT is_global FROM domains WHERE id = NEW.domain_id) THEN
       RAISE EXCEPTION 'injected global join failure'; END IF; RETURN NEW; END $$;
     CREATE TRIGGER domain_test_fail_global_join BEFORE INSERT OR UPDATE ON user_domains
     FOR EACH ROW EXECUTE FUNCTION pg_temp.fail_global_join()`)
			}
			require.NoError(t, err)
			before, err := pg.GetUserDomains(ctx, tx, f.user.ID)
			require.NoError(t, err)
			count := domainAuditCount(t, tx, f.user.ID)
			_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, before.DomainRevision)
			if failure == "injected-write-failure" {
				require.ErrorContains(t, err, "injected global join failure")
			} else {
				require.ErrorIs(t, err, ErrDomainFallbackUnavailable)
			}
			after, err := pg.GetUserDomains(ctx, tx, f.user.ID)
			require.NoError(t, err, "savepoint failure leaves the outer transaction usable")
			assert.Equal(t, before, after)
			assert.Equal(t, count, domainAuditCount(t, tx, f.user.ID))
		})
	}
}

func TestDomainMembershipOnboardingRollback(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	f := newDomainMembershipFixture(t, pg, tx)
	_, err := tx.Exec(ctx, `CREATE FUNCTION pg_temp.fail_domain_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'injected audit failure'; END $$;
 CREATE TRIGGER domain_test_fail_audit BEFORE INSERT ON domain_audit_events
 FOR EACH ROW EXECUTE FUNCTION pg_temp.fail_domain_audit()`)
	require.NoError(t, err)
	_, err = pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
	require.ErrorContains(t, err, "injected audit failure")
	state, err := pg.GetUserDomains(ctx, tx, f.user.ID)
	require.NoError(t, err)
	assert.Nil(t, state.RegistrationDomainID)
	assert.Zero(t, state.DomainRevision)
	assert.Empty(t, state.Memberships)
	assert.False(t, state.GlobalRequired)
	user, err := pg.GetUserByID(ctx, tx, f.user.ID)
	require.NoError(t, err)
	assert.Equal(t, f.user, user, "profile and timestamps roll back with the failed registration")
	assert.Zero(t, domainAuditCount(t, tx, f.user.ID))
}

func TestDomainMembershipRestrictions(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	f := newDomainMembershipFixture(t, pg, tx)
	for _, status := range []string{"PENDING", "REJECTED"} {
		_, err := pg.UpdateUserStatus(ctx, tx, f.user.EthereumAddress, status)
		require.NoError(t, err)
		_, err = pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
		require.ErrorIs(t, err, ErrDomainAccountRestricted)
	}
	_, err := pg.UpdateUserStatus(ctx, tx, f.user.EthereumAddress, "APPROVED")
	require.NoError(t, err)
	state, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE domains SET status = 'archived' WHERE id = $1`, f.other)
	require.NoError(t, err)
	_, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.ErrorIs(t, err, ErrDomainUnavailable)
	_, err = pg.JoinDomain(ctx, tx, f.user.ID, 2147483647, state.DomainRevision)
	require.ErrorIs(t, err, ErrDomainUnavailable)
	_, err = tx.Exec(ctx, `INSERT INTO user_domains (user_id, domain_id, status) VALUES ($1, $2, 'suspended')`, f.user.ID, f.other)
	require.NoError(t, err)
	_, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.ErrorIs(t, err, ErrDomainMembershipSuspended)
	_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.ErrorIs(t, err, ErrDomainMembershipSuspended)
	// Legacy/admin damage needs an explicit repair operation, not a retry side effect.
	_, err = tx.Exec(ctx, `UPDATE user_domains SET status = 'left', left_at = clock_timestamp() WHERE user_id = $1 AND domain_id = $2`, f.user.ID, f.kosta)
	require.NoError(t, err)
	_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, state.DomainRevision)
	require.ErrorIs(t, err, ErrDomainRepairRequired)
}

func TestDomainMembershipDirectGlobalRegistration(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	f := newDomainMembershipFixture(t, pg, tx)
	state, err := pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.global, 0, f.profile)
	require.NoError(t, err)
	assert.True(t, state.GlobalRequired)
	state, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
	require.NoError(t, err)
	_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.global, state.DomainRevision)
	require.ErrorIs(t, err, ErrGlobalDomainRequired)
}

// Concurrent callers share a revision. Exactly one changing operation commits;
// the other must refetch before retrying. These fixtures commit so both database
// connections can see them, unlike the ordinary rolled-back test harness.
func TestDomainMembershipConcurrentLeaveJoin(t *testing.T) {
	pg, pool := newCatalogTestPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	setup, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer setup.Rollback(ctx)
	var oldHost domainHost
	require.NoError(t, setup.QueryRow(ctx, `SELECT h.domain_id, h.hostname, h.status, h.verified_at FROM domain_hosts h JOIN domains d ON d.id = h.domain_id WHERE d.is_global`).Scan(&oldHost.DomainID, &oldHost.Hostname, &oldHost.Status, &oldHost.VerifiedAt))
	f := newDomainMembershipFixture(t, pg, setup)
	state, err := pg.CompleteDomainOnboarding(ctx, setup, f.user.ID, f.kosta, 0, f.profile)
	require.NoError(t, err)
	require.NoError(t, setup.Commit(ctx))
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		err := pgx.BeginFunc(cleanupCtx, pool, func(tx pgx.Tx) error {
			statements := []struct {
				query string
				args  []any
			}{
				{`DELETE FROM domain_audit_events WHERE actor_key = $1`, []any{fmt.Sprintf("user:%d", f.user.ID)}},
				{`DELETE FROM user_domains WHERE user_id = $1`, []any{f.user.ID}},
				{`DELETE FROM users WHERE id = $1`, []any{f.user.ID}},
				{`DELETE FROM domain_hosts WHERE domain_id IN ($1, $2)`, []any{f.kosta, f.other}},
				{`DELETE FROM domains WHERE id IN ($1, $2)`, []any{f.kosta, f.other}},
				{`UPDATE domain_hosts SET status = $2, verified_at = $3 WHERE domain_id = $1`, []any{oldHost.DomainID, oldHost.Status, oldHost.VerifiedAt}},
			}
			for _, statement := range statements {
				if _, err := tx.Exec(cleanupCtx, statement.query, statement.args...); err != nil {
					return err
				}
			}
			return nil
		})
		assert.NoError(t, err)
	}()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, leave := range []bool{true, false} {
		go func() {
			<-start
			results <- pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				if leave {
					_, err = pg.LeaveDomain(ctx, tx, f.user.ID, f.kosta, state.DomainRevision)
				} else {
					_, err = pg.JoinDomain(ctx, tx, f.user.ID, f.other, state.DomainRevision)
				}
				return err
			})
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDomainRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
	read, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer read.Rollback(ctx)
	final, err := pg.GetUserDomains(ctx, read, f.user.ID)
	require.NoError(t, err)
	assert.Equal(t, state.DomainRevision+1, final.DomainRevision)
	assert.Equal(t, f.kosta, *final.RegistrationDomainID)
	if final.membershipStatus(f.kosta) == "left" {
		assert.Equal(t, "active", final.membershipStatus(f.global))
		assert.Equal(t, "", final.membershipStatus(f.other))
	} else {
		assert.Equal(t, "active", final.membershipStatus(f.other))
	}
}

func TestDomainMembershipOnboardingRequiresReadyHosts(t *testing.T) {
	for _, target := range []bool{true, false} {
		t.Run(fmt.Sprintf("target-host-%t", target), func(t *testing.T) {
			pg, tx, cleanup := newCatalogSearchTestPg(t)
			defer cleanup()
			ctx := context.Background()
			f := newDomainMembershipFixture(t, pg, tx)
			id := f.global
			expected := ErrDomainFallbackUnavailable
			if target {
				id = f.kosta
				expected = ErrDomainUnavailable
			}
			_, err := tx.Exec(ctx, `UPDATE domain_hosts SET status = 'pending', verified_at = NULL WHERE domain_id = $1`, id)
			require.NoError(t, err)
			_, err = pg.CompleteDomainOnboarding(ctx, tx, f.user.ID, f.kosta, 0, f.profile)
			require.ErrorIs(t, err, expected)
			state, err := pg.GetUserDomains(ctx, tx, f.user.ID)
			require.NoError(t, err)
			assert.Nil(t, state.RegistrationDomainID)
			assert.Empty(t, state.Memberships)
			assert.Zero(t, state.DomainRevision)
			assert.Zero(t, domainAuditCount(t, tx, f.user.ID))
		})
	}
}
