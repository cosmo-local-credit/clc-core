package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrAmbiguousRecipient = errors.New("recipient is ambiguous")
var ErrNoUsernameAvailable = errors.New("no username available for this hint")

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,22}[a-z0-9]$`)
var usernameLetter = regexp.MustCompile(`[a-z]`)

func ValidUsername(name string) bool {
	return usernamePattern.MatchString(name) && usernameLetter.MatchString(name)
}

// Suggestions do not claim a name. The constraint remains authoritative at save.
func (pg *Pg) SuggestUsername(ctx context.Context, tx pgx.Tx, hint string) (string, error) {
	stem := usernameStem(hint)
	for i := 0; i < 100; i++ {
		candidate := usernameCandidate(stem, i)
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity_claims
            WHERE namespace = 'recipient' AND name = $1)`, candidate).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", ErrNoUsernameAvailable
}

func usernameStem(hint string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(hint) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '-' {
			b.WriteByte('-')
		}
	}
	stem := strings.Trim(b.String(), "-")
	if len(stem) > 20 {
		stem = strings.TrimRight(stem[:20], "-")
	}
	if !usernameLetter.MatchString(stem) {
		stem = "user"
	}
	if len(stem) < 3 {
		stem += "user"
	}
	return stem
}

func usernameCandidate(stem string, attempt int) string {
	if attempt == 0 {
		return stem
	}
	return fmt.Sprintf("%s%d", stem, attempt)
}

// A savepoint lets automatic assignment recover from a concurrent unique claim
// without aborting the surrounding profile transaction.
func (pg *Pg) assignUsername(ctx context.Context, tx pgx.Tx, address, hint string) (string, error) {
	stem := usernameStem(hint)
	for i := 0; i < 100; i++ {
		candidate := usernameCandidate(stem, i)
		if _, err := tx.Exec(ctx, "SAVEPOINT username_claim"); err != nil {
			return "", err
		}
		var saved string
		err := tx.QueryRow(ctx, `UPDATE users SET username = $2 WHERE lower(blockchain_address) = lower($1) RETURNING username`, address, candidate).Scan(&saved)
		if err == nil {
			_, err = tx.Exec(ctx, "RELEASE SAVEPOINT username_claim")
			return saved, err
		}
		if _, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT username_claim"); rollbackErr != nil {
			return "", rollbackErr
		}
		if _, releaseErr := tx.Exec(ctx, "RELEASE SAVEPOINT username_claim"); releaseErr != nil {
			return "", releaseErr
		}
		var unique *pgconn.PgError
		if !(errors.As(err, &unique) && unique.Code == "23505" && unique.ConstraintName == "identity_claims_name_key") {
			return "", err
		}
	}
	return "", ErrNoUsernameAvailable
}

// Recipient exposes public identity only, never profile contacts or admin data.
type Recipient struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Alias   string `json:"alias"`
	Address string `json:"address"`
	ChainID int64  `json:"chainId"`
}

const recipientProjection = `WITH recipients AS (
    SELECT 'user'::TEXT AS kind, COALESCE(NULLIF(name, ''), username) AS name,
           username AS alias, blockchain_address AS address, $2::BIGINT AS chain_id
    FROM users WHERE username IS NOT NULL AND status = 'APPROVED'
    UNION ALL
    SELECT 'pool', COALESCE(display_name, name), COALESCE(display_name, name), address,
           chain_id
    FROM pools WHERE ($2::BIGINT = 0 OR chain_id = $2) AND COALESCE(display_name, name) IS NOT NULL
) SELECT kind, name, alias, address, chain_id FROM recipients `

func (pg *Pg) SearchRecipients(ctx context.Context, tx pgx.Tx, query string, chainID int64, kind string) ([]Recipient, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(NormalizeDeployName(query))
	rows, err := tx.Query(ctx, recipientProjection+`WHERE ($3 = '' OR kind = $3)
        AND normalize_identity_name(alias) LIKE '%' || $1 || '%' ESCAPE '\'
        ORDER BY (normalize_identity_name(alias) = $4) DESC,
                 lower(alias), kind, address LIMIT 20`, escaped, chainID, kind, NormalizeDeployName(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Recipient{}
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.Kind, &r.Name, &r.Alias, &r.Address, &r.ChainID); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (pg *Pg) ResolveRecipient(ctx context.Context, tx pgx.Tx, name string, chainID int64, reverse bool) (Recipient, error) {
	predicate := `WHERE normalize_identity_name(alias) = normalize_identity_name($1)`
	if reverse {
		predicate = `WHERE lower(address) = lower($1)`
	}
	rows, err := tx.Query(ctx, recipientProjection+predicate+` LIMIT 2`, name, chainID)
	if err != nil {
		return Recipient{}, err
	}
	defer rows.Close()
	var found Recipient
	count := 0
	for rows.Next() {
		if err := rows.Scan(&found.Kind, &found.Name, &found.Alias, &found.Address, &found.ChainID); err != nil {
			return Recipient{}, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return Recipient{}, err
	}
	if count == 0 {
		return Recipient{}, pgx.ErrNoRows
	}
	if count > 1 {
		return Recipient{}, ErrAmbiguousRecipient
	}
	return found, nil
}

func (pg *Pg) ReserveDeploymentIdentity(ctx context.Context, tx pgx.Tx, trackingID, kind, name, symbol string) error {
	if kind == "pool" {
		if _, err := tx.Exec(ctx, `SELECT claim_identity('recipient', $1, $2, 'deploy:' || $3)`, name, kind, trackingID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `SELECT claim_identity('symbol', $1, $2, 'deploy:' || $3)`, symbol, kind, trackingID)
	return err
}

// ReconcileDeploymentIdentities releases reservations left by jobs that were
// cancelled, discarded (including crashes), or pruned after finishing. Queue
// insertion and reservation share a transaction, so an active reservation cannot
// be visible before its job. Durable deployments retain their claims until the
// deployment status explicitly fails or catalog promotion transfers ownership.
func (pg *Pg) ReconcileDeploymentIdentities(ctx context.Context, tx pgx.Tx) error {
	// Lock first, then inspect deployment state in a fresh statement snapshot.
	// A worker persisting its deployment also writes these claims via triggers;
	// skip it while in flight and never delete using a pre-commit snapshot.
	rows, err := tx.Query(ctx, `SELECT owner_key FROM identity_claims
        WHERE starts_with(owner_key, 'deploy:') FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(owners) == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM identity_claims c
        WHERE c.owner_key = ANY($1::TEXT[])
        AND NOT EXISTS (SELECT 1 FROM river_job j
            WHERE j.kind IN ('POOL_DEPLOY', 'TOKEN_DEPLOY')
              AND j.args->>'trackingId' = substring(c.owner_key FROM 8)
              AND j.state NOT IN ('completed', 'cancelled', 'discarded'))
        AND NOT EXISTS (SELECT 1 FROM otx o JOIN deployed_pools d ON d.otx_id = o.id
            WHERE o.tracking_id::TEXT = substring(c.owner_key FROM 8)
              AND d.status IN ('PENDING', 'CONFIRMED'))
        AND NOT EXISTS (SELECT 1 FROM otx o JOIN deployed_tokens d ON d.otx_id = o.id
            WHERE o.tracking_id::TEXT = substring(c.owner_key FROM 8)
              AND d.status IN ('PENDING', 'CONFIRMED'))`, owners)
	return err
}
