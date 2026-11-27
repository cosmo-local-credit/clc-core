package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type (
	requiredIssue struct {
		Kind         string `json:"kind"`
		Reason       string `json:"reason"`
		ChainID      int64  `json:"chainId,omitempty"`
		Address      string `json:"address,omitempty"`
		DeploymentID int64  `json:"deploymentId,omitempty"`
		TrackingID   string `json:"trackingId,omitempty"`
		JobID        int64  `json:"jobId,omitempty"`
		Candidate    string `json:"candidate,omitempty"`
	}

	requiredDeployment struct {
		issue      requiredIssue
		noNewClaim bool
	}
)

const (
	// Evaluate the proposed insert's value in the same order as catalog
	// promotion. A matching existing catalog record can be a retired Voucher.
	requiredDurableDeploymentsSQL = `
		SELECT d.id, pc.chain_id, COALESCE(d.address, ''), o.tracking_id::TEXT,
		       COALESCE(v.display_symbol, d.display_symbol,
		         CASE WHEN btrim(d.symbol) COLLATE "C" ~ '^[A-Za-z0-9]{3,6}$'
		              THEN upper(btrim(d.symbol) COLLATE "C") END, ''),
		       v.display_symbol IS NOT NULL OR (v.id IS NOT NULL AND NOT v.active)
		FROM deployed_tokens d
		JOIN platform_contracts pc ON pc.id = d.platform_contract_id
		JOIN otx o ON o.id = d.otx_id
		LEFT JOIN vouchers v ON v.chain_id = pc.chain_id AND lower(v.address) = lower(d.address)
		WHERE d.status IN ('PENDING', 'CONFIRMED') AND d.voucher_id IS NULL
		ORDER BY d.id`

	// Check each actionable job's own arguments. A retry can create another
	// deployment even after the previous attempt committed its durable row.
	// Explicit saved labels must already be canonical: only an absent or JSON
	// null label uses the legacy ticker fallback.
	requiredQueueSQL = `
		SELECT j.id, COALESCE(lower(j.args->>'trackingId'), ''),
		       COALESCE(CASE
		         WHEN j.args ? 'displaySymbol' AND jsonb_typeof(j.args->'displaySymbol') <> 'null'
		           THEN CASE WHEN jsonb_typeof(j.args->'displaySymbol') = 'string'
		                     THEN j.args->>'displaySymbol' END
		         WHEN jsonb_typeof(j.args->'symbol') = 'string'
		              AND btrim(j.args->>'symbol') COLLATE "C" ~ '^[A-Za-z0-9]{3,6}$'
		           THEN upper(btrim(j.args->>'symbol') COLLATE "C")
		       END, '')
		FROM river_job j
		WHERE j.kind = 'TOKEN_DEPLOY'
		  AND j.state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
		ORDER BY j.id`
)

// auditRequired is read-only. The operator must check the complete catalog and
// live deployments before making the display-label column required.
func auditRequired(ctx context.Context, tx pgx.Tx) ([]requiredIssue, error) {
	report := make([]requiredIssue, 0)
	active, err := audit(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, row := range active {
		report = append(report, requiredIssue{
			Kind: "voucher", Reason: row.Reason, ChainID: row.ChainID,
			Address: row.Address, Candidate: row.Suggested,
		})
	}
	retired, err := auditRetiredLabels(ctx, tx)
	if err != nil {
		return nil, err
	}
	report = append(report, retired...)

	deployments, err := auditRequiredDeployments(ctx, tx)
	if err != nil {
		return nil, err
	}
	return append(report, deployments...), nil
}

func auditRetiredLabels(ctx context.Context, tx pgx.Tx) ([]requiredIssue, error) {
	rows, err := tx.Query(ctx, `
		SELECT chain_id, address,
		       CASE WHEN btrim(symbol) COLLATE "C" ~ '^[A-Za-z0-9]{3,6}$'
		            THEN upper(btrim(symbol) COLLATE "C") ELSE '' END
		FROM vouchers
		WHERE NOT active AND display_symbol IS NULL
		ORDER BY chain_id, lower(address)`)
	if err != nil {
		return nil, fmt.Errorf("audit retired Voucher labels: %w", err)
	}
	defer rows.Close()

	var report []requiredIssue
	for rows.Next() {
		issue := requiredIssue{Kind: "voucher", Reason: "missing-retired-label"}
		if err := rows.Scan(&issue.ChainID, &issue.Address, &issue.Candidate); err != nil {
			return nil, err
		}
		report = append(report, issue)
	}
	return report, rows.Err()
}

func auditRequiredDeployments(ctx context.Context, tx pgx.Tx) ([]requiredIssue, error) {
	targets, err := collectRequiredDeployments(ctx, tx)
	if err != nil {
		return nil, err
	}

	// Old queue jobs may not have a database reservation. Check for collisions
	// between prospective owners as well as the existing catalog and claims.
	// Replays do not reserve another copy of a saved catalog label, and
	// updates to a retired catalog Voucher do not claim an active label.
	prospective := make(map[string]string)
	duplicate := make(map[string]bool)
	for _, deployment := range targets {
		label := deployment.issue.Candidate
		if deployment.noNewClaim || !symbolPattern.MatchString(label) {
			continue
		}
		owner := deployment.prospectiveOwnerKey()
		if previous, ok := prospective[label]; ok && previous != owner {
			duplicate[label] = true
		}
		prospective[label] = owner
	}

	var report []requiredIssue
	for _, deployment := range targets {
		issue := deployment.issue
		switch {
		case !symbolPattern.MatchString(issue.Candidate):
			issue.Reason = "invalid-or-missing-label"
			issue.Candidate = "" // Avoid emitting malformed queue argument contents.
		case deployment.noNewClaim:
			continue
		case duplicate[issue.Candidate]:
			issue.Reason = "duplicate-or-reserved"
		default:
			var occupied bool
			err := tx.QueryRow(ctx, `
				SELECT clc_display_symbol_occupied($1, 'voucher', $2, NULLIF($3, ''))`,
				issue.Candidate, deployment.ownerKey(), deployment.trackingKey()).Scan(&occupied)
			if err != nil {
				return nil, fmt.Errorf("check deployment label reservation: %w", err)
			}
			if !occupied {
				continue
			}
			issue.Reason = "duplicate-or-reserved"
		}
		report = append(report, issue)
	}
	return report, nil
}

func collectRequiredDeployments(ctx context.Context, tx pgx.Tx) ([]requiredDeployment, error) {
	rows, err := tx.Query(ctx, requiredDurableDeploymentsSQL)
	if err != nil {
		return nil, fmt.Errorf("audit unlinked Voucher deployments: %w", err)
	}
	defer rows.Close()

	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (requiredDeployment, error) {
		target := requiredDeployment{issue: requiredIssue{Kind: "deployment"}}
		err := row.Scan(&target.issue.DeploymentID, &target.issue.ChainID,
			&target.issue.Address, &target.issue.TrackingID, &target.issue.Candidate,
			&target.noNewClaim)
		return target, err
	})
	if err != nil {
		return nil, err
	}

	var hasQueue bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('river_job') IS NOT NULL").Scan(&hasQueue); err != nil {
		return nil, err
	}
	if !hasQueue {
		return targets, nil
	}

	queueRows, err := tx.Query(ctx, requiredQueueSQL)
	if err != nil {
		return nil, fmt.Errorf("audit queued Voucher deployments: %w", err)
	}
	defer queueRows.Close()

	queued, err := pgx.CollectRows(queueRows, func(row pgx.CollectableRow) (requiredDeployment, error) {
		target := requiredDeployment{issue: requiredIssue{Kind: "river-job"}}
		err := row.Scan(&target.issue.JobID, &target.issue.TrackingID, &target.issue.Candidate)
		return target, err
	})
	if err != nil {
		return nil, err
	}
	return append(targets, queued...), nil
}

// A durable row and its retry describe the same prospective claim even when
// only the durable row knows the Voucher address.
func (d requiredDeployment) prospectiveOwnerKey() string {
	if key := d.trackingKey(); key != "" {
		return key
	}
	return d.ownerKey()
}

func (d requiredDeployment) ownerKey() string {
	if d.issue.Address != "" {
		return fmt.Sprintf("%d:%s", d.issue.ChainID, strings.ToLower(d.issue.Address))
	}
	if key := d.trackingKey(); key != "" {
		return key
	}
	return fmt.Sprintf("river-job:%d", d.issue.JobID)
}

func (d requiredDeployment) trackingKey() string {
	if d.issue.TrackingID == "" {
		return ""
	}
	return "deploy:" + d.issue.TrackingID
}
