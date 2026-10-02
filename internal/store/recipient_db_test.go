package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func recipientTestStore(t *testing.T) *Pg {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	pg, err := NewPgStore(PgOpts{DSN: dsn, Logg: slog.New(slog.DiscardHandler), MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql"})
	require.NoError(t, err)
	t.Cleanup(pg.Pool().Close)
	concrete, ok := pg.(*Pg)
	require.True(t, ok, "expected PostgreSQL store")
	return concrete
}

func TestRecipientPhoneSearchMatchesLiteralPrefixes(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	marker := "phone-" + uuid.NewString()[:8]
	for i, phone := range []string{"071", "(071) 234-5678", "+254712345678", "00712345678"} {
		_, err := tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,phone,status)
            VALUES($1,'Phone User',$2,$3,$4,'APPROVED')`, randomAddress(t, tx),
			marker+fmt.Sprint(i)+"@example.test", marker+fmt.Sprint(i), phone)
		require.NoError(t, err)
	}
	// Matching contact fields do not expose pending/unnamed users or pools.
	_, err = tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,phone,status)
        VALUES($1,'Pending',$2,$3,'071999','PENDING'),($4,'Unnamed',$5,NULL,'071888','APPROVED')`,
		randomAddress(t, tx), marker+"pending@example.test", marker+"pending",
		randomAddress(t, tx), marker+"unnamed@example.test")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol,phone)
        VALUES(100,$1,$2,$3,'071777')`, randomAddress(t, tx), marker+"pool", marker+"symbol")
	require.NoError(t, err)

	for _, query := range []string{"071", " (071) ", "+071"} {
		found, err := pg.SearchRecipients(ctx, tx, query, 100, "")
		require.NoError(t, err)
		require.Len(t, found, 2)
		require.Equal(t, marker+"0", found[0].Alias, "exact phone first")
		require.Equal(t, marker+"1", found[1].Alias)
	}
	for query, alias := range map[string]string{"+254 712": marker + "2", "007": marker + "3"} {
		found, err := pg.SearchRecipients(ctx, tx, query, 100, "user")
		require.NoError(t, err)
		require.Len(t, found, 1)
		require.Equal(t, alias, found[0].Alias)
	}
	for _, query := range []string{"07", "+0(7)", "999999"} {
		found, err := pg.SearchRecipients(ctx, tx, query, 100, "")
		require.NoError(t, err)
		require.Empty(t, found)
	}
	found, err := pg.SearchRecipients(ctx, tx, "071", 100, "pool")
	require.NoError(t, err)
	require.Empty(t, found)

	// Keep prefix results bounded, even when many profiles share the prefix.
	_, err = tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,phone,status)
        SELECT '0x' || md5($1 || i::TEXT) || '00000000', 'Phone User',
            $1 || i::TEXT || '@example.test', $1 || i::TEXT, '071555' || i::TEXT, 'APPROVED'
        FROM generate_series(1,10000) i`, marker+"limit")
	require.NoError(t, err)
	found, err = pg.SearchRecipients(ctx, tx, "071", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 20)
	require.Equal(t, marker+"0", found[0].Alias)
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+recipientPhoneSearchQuery, "071", 100, "", "071")
	require.NoError(t, err)
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	t.Log("Phone prefix plan on 10,000 matching profiles:\n" + strings.Join(plan, "\n"))

	// Phone input does not change name discovery.
	found, err = pg.SearchRecipients(ctx, tx, marker+"pool", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "pool", found[0].Kind)
}

func TestRecipientFullNameSuggestionsAddNumbersOnlyWhenTaken(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	name, err := pg.SuggestUsername(ctx, tx, "Sohail Khan")
	require.NoError(t, err)
	require.Equal(t, "sohail-khan", name)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol)
        SELECT 100, '0x' || md5(i::TEXT) || '00000000',
            'sohail-khan' || CASE WHEN i=0 THEN '' ELSE i::TEXT END, 'FNAME' || i::TEXT
        FROM generate_series(0,1) i`)
	require.NoError(t, err)
	name, err = pg.SuggestUsername(ctx, tx, "Sohail Khan")
	require.NoError(t, err)
	require.Equal(t, "sohail-khan2", name)
	address := randomAddress(t, tx)
	_, err = pg.InsertUser(ctx, tx, User{EthereumAddress: address, Name: "Sohail Khan", Email: "full-name@example.test", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	saved, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{Address: address, Name: "Sohail Khan", Email: "full-name@example.test"})
	require.NoError(t, err)
	require.Equal(t, "sohail-khan2", *saved.Username)

	// A non-Latin name cannot form an ASCII username. Keep profile save working
	// without generating user99 or a random identity.
	otherAddress := randomAddress(t, tx)
	_, err = pg.InsertUser(ctx, tx, User{EthereumAddress: otherAddress, Name: "李明", Email: "manual-name@example.test", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	_, err = pg.SuggestUsername(ctx, tx, "李明")
	require.ErrorIs(t, err, ErrNoUsernameAvailable)
	saved, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{Address: otherAddress, Name: "李明", Email: "manual-name@example.test"})
	require.NoError(t, err)
	require.Nil(t, saved.Username)
}

func TestRecipientConcurrentAutomaticAssignment(t *testing.T) {
	pg := recipientTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hint := "race-" + uuid.NewString()[:8]
	addresses := []string{}
	seed, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer seed.Rollback(ctx)
	for range 4 {
		address := randomAddress(t, seed)
		_, err = seed.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,status)
            VALUES($1,$2,$3,'APPROVED')`, address, hint, address+"@example.test")
		require.NoError(t, err)
		addresses = append(addresses, address)
	}
	require.NoError(t, seed.Commit(ctx))
	t.Cleanup(func() {
		_, err := pg.Pool().Exec(context.Background(), `DELETE FROM users WHERE blockchain_address = ANY($1::TEXT[])`, addresses)
		if err != nil {
			t.Errorf("cleanup concurrent username fixtures: %v", err)
		}
	})
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(addresses))
	start := make(chan struct{})
	for _, address := range addresses {
		go func() {
			tx, err := pg.Pool().Begin(ctx)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer tx.Rollback(ctx)
			<-start
			name, err := pg.assignUsername(ctx, tx, address, hint)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- result{name: name, err: err}
		}()
	}
	close(start)
	names := map[string]bool{}
	for range addresses {
		r := <-results
		// A bounded retry can defer a heavily contended assignment safely.
		if errors.Is(r.err, ErrNoUsernameAvailable) {
			continue
		}
		require.NoError(t, r.err)
		require.False(t, names[r.name], "two users must never receive the same username")
		names[r.name] = true
	}
	require.True(t, names[hint], "one concurrent request must claim the base name")
}

func TestRecipientCurrentNamesAndSymbols(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	userAddress := "0x1111111111111111111111111111111111111111"
	poolAddress := "0x2222222222222222222222222222222222222222"
	_, err = tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,status)
        VALUES($1,'Sohail','recipient-test@example.test','sohail','APPROVED')`, userAddress)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol) VALUES(100,$1,'Sohail Pool','SPOL')`, poolAddress)
	require.NoError(t, err)
	found, err := pg.SearchRecipients(ctx, tx, "sohail pool", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, poolAddress, found[0].Address)
	found, err = pg.SearchRecipients(ctx, tx, "SPOL", 100, "")
	require.NoError(t, err)
	require.Empty(t, found)
	resolved, err := pg.ResolveRecipient(ctx, tx, "SOHAIL", 100, false)
	require.NoError(t, err)
	require.Equal(t, userAddress, resolved.Address)
	_, err = tx.Exec(ctx, `SAVEPOINT conflict`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pools SET display_name='  SOHAIL  ' WHERE address=$1`, poolAddress)
	var unique *pgconn.PgError
	require.True(t, errors.As(err, &unique))
	require.Equal(t, "23514", unique.Code) // invalid padding is rejected first
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT conflict`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pools SET display_name='SOHAIL' WHERE address=$1`, poolAddress)
	require.True(t, errors.As(err, &unique))
	require.Equal(t, "23505", unique.Code)
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT conflict`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol) VALUES(100,'0x3333333333333333333333333333333333333333','Another Voucher','spol')`)
	require.True(t, errors.As(err, &unique))
	require.Equal(t, "23505", unique.Code)
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT conflict`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pools SET display_name='New Pool' WHERE address=$1`, poolAddress)
	require.NoError(t, err)
	_, err = pg.ResolveRecipient(ctx, tx, "Sohail Pool", 100, false)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	resolved, err = pg.ResolveRecipient(ctx, tx, poolAddress, 100, true)
	require.NoError(t, err)
	require.Equal(t, "New Pool", resolved.Alias)
	assigned, err := pg.assignUsername(ctx, tx, userAddress, "new-pool")
	require.NoError(t, err)
	require.Equal(t, "new-pool", assigned)
	suggestion, err := pg.SuggestUsername(ctx, tx, "new-pool")
	require.NoError(t, err)
	require.Equal(t, "new-pool1", suggestion)
	// The renamed username is released and resolves only after another current owner claims it.
	_, err = pg.ResolveRecipient(ctx, tx, "sohail", 100, false)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = tx.Exec(ctx, `UPDATE pools SET display_name='sohail' WHERE address=$1`, poolAddress)
	require.NoError(t, err)
	resolved, err = pg.ResolveRecipient(ctx, tx, "sohail", 100, false)
	require.NoError(t, err)
	require.Equal(t, poolAddress, resolved.Address)
}

func TestRecipientConcurrentClaimHasOneWinner(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	first, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer first.Rollback(ctx)
	second, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer second.Rollback(ctx)
	firstAddress := "0x4444444444444444444444444444444444444444"
	_, err = first.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol) VALUES(100,$1,'concurrent-name','CNCR')`, firstAddress)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, e := second.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,status)
        VALUES('0x5555555555555555555555555555555555555555','Concurrent','concurrent@example.test','concurrent-name','APPROVED')`)
		result <- e
	}()
	select {
	case err := <-result:
		t.Fatalf("competing claim did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, first.Commit(ctx))
	t.Cleanup(func() {
		_, err := pg.Pool().Exec(context.Background(), `DELETE FROM pools WHERE address=$1`, firstAddress)
		if err != nil {
			t.Errorf("cleanup concurrent pool fixture: %v", err)
		}
	})
	select {
	case err := <-result:
		var unique *pgconn.PgError
		require.True(t, errors.As(err, &unique))
		require.Equal(t, "23505", unique.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not finish")
	}
}

func TestRecipientDeploymentLifecycle(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	factory := platformContractID(t, tx, "SWAPPOOL")
	otxID := seedDeployOTX(t, tx, POOL_PROXY_DEPLOY)
	var tracking string
	require.NoError(t, tx.QueryRow(ctx, "SELECT tracking_id::TEXT FROM otx WHERE id=$1", otxID).Scan(&tracking))
	require.NoError(t, pg.ReserveDeploymentIdentity(ctx, tx, tracking, "pool", "Lifecycle Pool", "LFPL"))
	address := "0x6666666666666666666666666666666666666666"
	deployedID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID: otxID, PlatformContractID: factory, Name: "Lifecycle Pool", Symbol: "LFPL", Decimals: 6,
		TokenRegistryAddress: address, LimiterAddress: address, FeePolicyAddress: address,
		QuoterAddress: address, ProtocolFeeControllerAddress: address, Admin: address,
	})
	require.NoError(t, err)
	taken, err := pg.PoolDisplayNameTaken(ctx, tx, "Lifecycle Pool", nil)
	require.NoError(t, err)
	require.True(t, taken)
	_, err = pg.ConfirmDeployedPool(ctx, tx, otxID, address)
	require.NoError(t, err)
	poolID, err := pg.UpsertPoolFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	var owner string
	require.NoError(t, tx.QueryRow(ctx, "SELECT owner_key FROM identity_claims WHERE namespace='recipient' AND name='lifecycle pool'").Scan(&owner))
	require.NotEqual(t, "deploy:"+tracking, owner)
	_, err = tx.Exec(ctx, "UPDATE pools SET display_name='Current Lifecycle Pool' WHERE id=$1", poolID)
	require.NoError(t, err)
	taken, err = pg.PoolDisplayNameTaken(ctx, tx, "Lifecycle Pool", nil)
	require.NoError(t, err)
	require.False(t, taken)
	// Clearing an override reclaims its original name, without retaining history.
	_, err = tx.Exec(ctx, "UPDATE pools SET display_name=NULL WHERE id=$1", poolID)
	require.NoError(t, err)
	taken, err = pg.PoolDisplayNameTaken(ctx, tx, "Current Lifecycle Pool", nil)
	require.NoError(t, err)
	require.False(t, taken)
	failedOTX := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	_, err = pg.InsertDeployedToken(ctx, tx, DeployedToken{OTXID: failedOTX,
		PlatformContractID: platformContractID(t, tx, "ERC20"), Name: "Failed Lifecycle Voucher", Symbol: "FLFV", Decimals: 6, Admin: address})
	require.NoError(t, err)
	require.NoError(t, pg.FailDeployedToken(ctx, tx, failedOTX))
	symbols, err := pg.ListTakenPoolSymbols(ctx, tx, "FLFV")
	require.NoError(t, err)
	require.Empty(t, symbols)
	_, err = tx.Exec(ctx, "INSERT INTO pools(chain_id,address,name,symbol) VALUES(100,'0x7777777777777777777777777777777777777777','Reused Failed Symbol','FLFV')")
	require.NoError(t, err)
}

func TestRecipientProfileUsernamePersists(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	address := "0x8888888888888888888888888888888888888888"
	_, err = pg.InsertUser(ctx, tx, User{EthereumAddress: address, Name: "User Example", Email: "username-profile@example.test", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	update := UserProfileUpdate{Address: address, Name: "User Example", Email: "username-profile@example.test"}
	saved, err := pg.UpdateUserProfile(ctx, tx, update)
	require.NoError(t, err)
	require.NotNil(t, saved.Username)
	require.Equal(t, "user-example", *saved.Username)
	username := "manual-profile"
	update.Username = &username
	saved, err = pg.UpdateUserProfile(ctx, tx, update)
	require.NoError(t, err)
	require.Equal(t, username, *saved.Username)
	// Re-saving the same explicit username remains idempotent.
	saved, err = pg.UpdateUserProfile(ctx, tx, update)
	require.NoError(t, err)
	require.Equal(t, username, *saved.Username)
	_, err = pg.ResolveRecipient(ctx, tx, "user-example", 100, false)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	found, err := pg.ResolveRecipient(ctx, tx, username, 100, false)
	require.NoError(t, err)
	require.Equal(t, address, found.Address)
}

func TestRecipientSearchPrioritizesExactMatches(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol)
        SELECT 100, '0x' || lpad(i::TEXT,40,'0'), 'AAA wllr ' || i, 'ZZR' || i
        FROM generate_series(1,25) i`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol) VALUES
        (100,'0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','Zebra Pool','WLLR'),
        (100,'0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','wllr','ZNM')`)
	require.NoError(t, err)
	found, err := pg.SearchRecipients(ctx, tx, "WLLR", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 20)
	require.Equal(t, "wllr", found[0].Alias)
	for _, recipient := range found {
		require.NotEqual(t, "Zebra Pool", recipient.Alias)
	}
}

func TestRecipientProfileDefersExhaustedAutoUsername(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol)
        SELECT 100, '0x' || lpad(i::TEXT,40,'0'),
            'crowded' || CASE WHEN i=0 THEN '' ELSE i::TEXT END, 'CRWD' || i
        FROM generate_series(0,99) i`)
	require.NoError(t, err)
	_, err = pg.SuggestUsername(ctx, tx, "Crowded")
	require.ErrorIs(t, err, ErrNoUsernameAvailable)
	address := "0xcccccccccccccccccccccccccccccccccccccccc"
	_, err = pg.InsertUser(ctx, tx, User{EthereumAddress: address, Name: "Before", Email: "deferred@example.test", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	update := UserProfileUpdate{Address: address, Name: "Crowded", Email: "deferred@example.test"}
	saved, err := pg.UpdateUserProfile(ctx, tx, update)
	require.NoError(t, err)
	require.Nil(t, saved.Username)
	require.Equal(t, "Crowded", saved.Name)
	// Saving again can fill the deferred username once a candidate is available.
	_, err = tx.Exec(ctx, `DELETE FROM pools WHERE name='crowded99'`)
	require.NoError(t, err)
	saved, err = pg.UpdateUserProfile(ctx, tx, update)
	require.NoError(t, err)
	require.Equal(t, "crowded99", *saved.Username)
	chosen := "crowded"
	update.Username = &chosen
	_, err = pg.UpdateUserProfile(ctx, tx, update)
	var unique *pgconn.PgError
	require.ErrorAs(t, err, &unique)
	require.Equal(t, "identity_claims_name_key", unique.ConstraintName)
}

func TestRecipientSymbolSuggestionsIgnoreLongSuffixes(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol)
        SELECT 100, '0x' || lpad(i::TEXT,40,'0'), 'Long suffix ' || i, 'AB' || i
        FROM generate_series(100,240) i;
        INSERT INTO pools(chain_id,address,name,symbol)
        VALUES (100,'0xdddddddddddddddddddddddddddddddddddddddd','Short suffix','aB2')`)
	require.NoError(t, err)
	for _, list := range []func(context.Context, pgx.Tx, string) ([]string, error){pg.ListTakenVoucherSymbols, pg.ListTakenPoolSymbols} {
		symbols, err := list(ctx, tx, "AB")
		require.NoError(t, err)
		require.Equal(t, []string{"AB2"}, symbols)
	}
}

func TestRecipientReconcileAbandonedReservations(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	migrator, err := rivermigrate.New(riverpgxv5.New(pg.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	for _, kind := range []string{"pool", "voucher"} {
		for _, state := range []string{"available", "pending", "running", "retryable", "scheduled", "completed", "cancelled", "discarded", "missing"} {
			tracking := kind + "-" + state
			require.NoError(t, pg.ReserveDeploymentIdentity(ctx, tx, tracking, kind, tracking, tracking))
			if state != "missing" {
				jobKind := "POOL_DEPLOY"
				if kind == "voucher" {
					jobKind = "TOKEN_DEPLOY"
				}
				_, err = tx.Exec(ctx, `INSERT INTO river_job(kind,args,state,max_attempts,priority,queue,finalized_at)
                    VALUES ($1,jsonb_build_object('trackingId',$2::TEXT),$3::river_job_state,1,1,'default',
                        CASE WHEN $3 IN ('completed','cancelled','discarded') THEN now() END)`, jobKind, tracking, state)
				require.NoError(t, err)
			}
		}
	}
	// Persisted deployments keep their reservation even after the queue job disappears.
	for i, status := range []string{"PENDING", "CONFIRMED"} {
		id := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
		_, err = pg.InsertDeployedToken(ctx, tx, DeployedToken{OTXID: id,
			PlatformContractID: platformContractID(t, tx, "ERC20"), Name: "Durable " + status,
			Symbol: fmt.Sprintf("DUR%d", i), Decimals: 6, Admin: "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"})
		require.NoError(t, err)
		if status == "CONFIRMED" {
			_, err = pg.ConfirmDeployedToken(ctx, tx, id, "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
			require.NoError(t, err)
		}
	}
	require.NoError(t, pg.ReconcileDeploymentIdentities(ctx, tx))
	for _, kind := range []string{"pool", "voucher"} {
		for _, state := range []string{"available", "pending", "running", "retryable", "scheduled", "completed", "cancelled", "discarded", "missing"} {
			var kept bool
			require.NoError(t, tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity_claims WHERE owner_key=$1)`, "deploy:"+kind+"-"+state).Scan(&kept))
			terminal := state == "completed" || state == "cancelled" || state == "discarded" || state == "missing"
			require.Equal(t, !terminal, kept, kind+"/"+state)
		}
	}
	var durable int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM identity_claims WHERE namespace='symbol' AND name IN ('dur0','dur1')`).Scan(&durable))
	require.Equal(t, 2, durable)
}

func TestRecipientSearchIgnoresPoolSymbols(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	userAddress := "0xabababababababababababababababababababab"
	_, err = tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,status)
        VALUES($1,'Will','symbol-overlap@example.test','wllr','APPROVED')`, userAddress)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol)
        VALUES(100,'0xcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd','Will Pool','WLLR')`)
	require.NoError(t, err)
	found, err := pg.SearchRecipients(ctx, tx, "WLLR", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "user", found[0].Kind)
	require.Equal(t, userAddress, found[0].Address)
	found, err = pg.SearchRecipients(ctx, tx, "wllr", 100, "pool")
	require.NoError(t, err)
	require.Empty(t, found)
	found, err = pg.SearchRecipients(ctx, tx, "Will Pool", 100, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "pool", found[0].Kind)
}
