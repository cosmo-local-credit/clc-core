package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/unicode/norm"
)

// Recipient exposes public identity only, never profile contacts or admin data.
type (
	Recipient struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Alias   string `json:"alias"`
		Address string `json:"address"`
		ChainID int64  `json:"chainId"`
	}
)

const (
	recipientProjection = `WITH recipients AS (
    SELECT 'user'::TEXT AS kind, username AS name,
           username AS alias, blockchain_address AS address, $2::BIGINT AS chain_id
    FROM users WHERE username IS NOT NULL AND status = 'APPROVED'
    UNION ALL
    SELECT 'pool', COALESCE(display_name, name), COALESCE(display_name, name), address,
           chain_id
    FROM pools WHERE active AND ($2::BIGINT = 0 OR chain_id = $2) AND COALESCE(display_name, name) IS NOT NULL
) SELECT kind, name, alias, address, chain_id FROM recipients `

	// recipientForwardLookup starts with the unique current claim, then uses
	// existing address indexes to load its public owner. Deployment reservations
	// never resolve as recipients.
	recipientForwardLookup = `SELECT 'user'::TEXT AS kind,
    u.username AS name, u.username AS alias,
    u.blockchain_address AS address, $2::BIGINT AS chain_id
FROM identity_claims c JOIN users u ON lower(u.blockchain_address) = c.owner_key
WHERE c.namespace = 'recipient' AND c.name = normalize_identity_name($1)
  AND c.owner_kind = 'user' AND u.username IS NOT NULL AND u.status = 'APPROVED'
UNION ALL
SELECT 'pool', COALESCE(p.display_name, p.name), COALESCE(p.display_name, p.name),
    p.address, p.chain_id
FROM identity_claims c JOIN pools p ON p.chain_id = CASE
    WHEN c.owner_kind = 'pool' AND NOT starts_with(c.owner_key, 'deploy:')
    THEN split_part(c.owner_key, ':', 1)::BIGINT END
    AND lower(p.address) = split_part(c.owner_key, ':', 2)
WHERE c.namespace = 'recipient' AND c.name = normalize_identity_name($1)
  AND c.owner_kind = 'pool' AND p.active AND ($2::BIGINT = 0 OR p.chain_id = $2)`

	recipientSearchQuery = recipientProjection + `WHERE ($3 = '' OR kind = $3)
        AND ((kind = 'user' AND normalize_identity_name(alias) = normalize_identity_name($4))
          OR (kind = 'pool' AND normalize_identity_name(alias) LIKE '%' || normalize_identity_name($1) || '%' ESCAPE '\'))
        ORDER BY (normalize_identity_name(alias) = normalize_identity_name($4)) DESC,
                 lower(alias), kind, address LIMIT 20`

	recipientPhoneSearchQuery = `SELECT 'user'::TEXT AS kind,
    username AS name, username AS alias,
    blockchain_address AS address, $2::BIGINT AS chain_id
FROM users
WHERE username IS NOT NULL AND status = 'APPROVED' AND ($3 = '' OR $3 = 'user')
  AND regexp_replace(phone, '[^0-9]', '', 'g') = $1
ORDER BY lower(username), blockchain_address LIMIT 20`

	recipientDeploymentOwnersQuery = `SELECT owner_key FROM identity_claims
        WHERE starts_with(owner_key, 'deploy:') FOR UPDATE SKIP LOCKED`
)

var (
	ErrAmbiguousRecipient  = errors.New("recipient is ambiguous")
	ErrNoUsernameAvailable = errors.New("no username available for this hint")

	recipientPhonePattern    = regexp.MustCompile(`^\+?[0-9\s()-]*[0-9][0-9\s()-]*$`)
	recipientPhoneFormatting = regexp.MustCompile(`[^0-9]`)
)

func ValidUsername(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 80
}

// SuggestUsername does not claim a name. The constraint remains authoritative at save.
func (pg *Pg) SuggestUsername(ctx context.Context, tx pgx.Tx, hint string) (string, error) {
	return availableUsername(ctx, tx, usernameStem(hint))
}

// Check all ordinary suffix candidates in one database round trip. Selection
// does not reserve the name; the unique constraint still arbitrates concurrent saves.
func availableUsername(ctx context.Context, tx pgx.Tx, stem string) (string, error) {
	if !ValidUsername(stem) {
		return "", ErrNoUsernameAvailable
	}
	var candidate string
	err := tx.QueryRow(ctx, `SELECT candidate FROM (
        SELECT i, CASE WHEN i = 0 THEN $1::TEXT ELSE left($1::TEXT, 80 - char_length(i::TEXT)) || i::TEXT END AS candidate
        FROM generate_series(0,99) i
    ) candidates WHERE NOT EXISTS (
        SELECT 1 FROM identity_claims WHERE namespace = 'recipient' AND name = normalize_identity_name(candidate)
    ) ORDER BY i LIMIT 1`, stem).Scan(&candidate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoUsernameAvailable
	}
	return candidate, err
}

func usernameStem(hint string) string {
	// Preserve the user's spelling and script, including internal spaces.
	runes := []rune(norm.NFC.String(strings.TrimSpace(hint)))
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return strings.TrimSpace(string(runes))
}

// A savepoint lets automatic assignment recover from a concurrent unique claim
// without aborting the surrounding profile transaction.
func (pg *Pg) assignUsername(ctx context.Context, tx pgx.Tx, address, hint string) (string, error) {
	stem := usernameStem(hint)
	for range 3 {
		candidate, err := availableUsername(ctx, tx, stem)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, "SAVEPOINT username_claim"); err != nil {
			return "", err
		}
		var saved string
		err = tx.QueryRow(ctx, `UPDATE users SET username = $2 WHERE lower(blockchain_address) = lower($1) RETURNING username`, address, candidate).Scan(&saved)
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
		if !errors.As(err, &unique) || unique.Code != "23505" || unique.ConstraintName != "identity_claims_name_key" {
			return "", err
		}
	}
	return "", ErrNoUsernameAvailable
}

func (pg *Pg) SearchRecipients(ctx context.Context, tx pgx.Tx, query string, chainID int64, kind string) ([]Recipient, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(norm.NFC.String(query))
	exact := norm.NFC.String(query)
	rows, err := tx.Query(ctx, recipientSearchQuery, escaped, chainID, kind, exact)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if recipientPhonePattern.MatchString(strings.TrimSpace(query)) {
		phone := recipientPhoneFormatting.ReplaceAllString(strings.TrimSpace(query), "")
		if phone != "" {
			nameMatches := result
			result = []Recipient{}
			for _, r := range nameMatches {
				if NormalizeDeployName(norm.NFC.String(r.Alias)) == NormalizeDeployName(exact) {
					result = append(result, r)
				}
			}
			phoneRows, err := tx.Query(ctx, recipientPhoneSearchQuery, phone, chainID, kind)
			if err != nil {
				return nil, err
			}
			defer phoneRows.Close()
			for phoneRows.Next() {
				var r Recipient
				if err := phoneRows.Scan(&r.Kind, &r.Name, &r.Alias, &r.Address, &r.ChainID); err != nil {
					return nil, err
				}
				duplicate := false
				for _, existing := range result {
					if existing.Kind == r.Kind && existing.Address == r.Address && existing.ChainID == r.ChainID {
						duplicate = true
						break
					}
				}
				if !duplicate && len(result) < 20 {
					result = append(result, r)
				}
			}
			if err := phoneRows.Err(); err != nil {
				return nil, err
			}
			for _, r := range nameMatches {
				duplicate := false
				for _, existing := range result {
					if existing.Kind == r.Kind && existing.Address == r.Address && existing.ChainID == r.ChainID {
						duplicate = true
						break
					}
				}
				if !duplicate && len(result) < 20 {
					result = append(result, r)
				}
			}
		}
	}
	return result, nil
}

func (pg *Pg) ResolveRecipient(ctx context.Context, tx pgx.Tx, name string, chainID int64, reverse bool) (Recipient, error) {
	query := recipientForwardLookup + ` LIMIT 2`
	if reverse {
		query = recipientProjection + `WHERE lower(address) = lower($1) LIMIT 2`
	}
	rows, err := tx.Query(ctx, query, name, chainID)
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
	rows, err := tx.Query(ctx, recipientDeploymentOwnersQuery)
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
