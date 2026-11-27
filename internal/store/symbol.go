package store

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func listTakenSymbols(ctx context.Context, tx pgx.Tx, query string, prefix string) ([]string, error) {
	rows, err := tx.Query(ctx, query, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	symbols := make([]string, 0)
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return nil, err
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (pg *Pg) ListTakenVoucherSymbols(ctx context.Context, tx pgx.Tx, prefix string) ([]string, error) {
	return listTakenSymbols(ctx, tx, pg.queries.ListTakenVoucherSymbols, prefix)
}

func (pg *Pg) ListTakenPoolSymbols(ctx context.Context, tx pgx.Tx, prefix string) ([]string, error) {
	return listTakenSymbols(ctx, tx, pg.queries.ListTakenPoolSymbols, prefix)
}

var (
	ErrNativeSymbolUnavailable = errors.New("no available contract symbol")
	displaySymbolPattern       = regexp.MustCompile(`^[A-Za-z0-9]{3,6}$`)
)

func NormalizeVoucherDisplaySymbol(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return strings.ToUpper(value), displaySymbolPattern.MatchString(value)
}

func (pg *Pg) ListTakenDisplaySymbols(ctx context.Context, tx pgx.Tx, prefix string) ([]string, error) {
	return listTakenSymbols(ctx, tx, pg.queries.ListTakenDisplaySymbols, prefix)
}

func (pg *Pg) ReserveVoucherDeploymentSymbols(ctx context.Context, tx pgx.Tx, trackingID, native, display string, allowGenerated bool) (string, error) {
	return pg.reserveDeploymentSymbols(ctx, tx, trackingID, "voucher", native, display, allowGenerated)
}

func (pg *Pg) reserveDeploymentSymbols(ctx context.Context, tx pgx.Tx, trackingID, kind, native, display string, allowGenerated bool) (string, error) {
	owner := "deploy:" + trackingID
	preferred := native
	// Match claim_identity's existing treatment of legacy empty inputs.
	reserved := strings.TrimSpace(native) == ""
	for suffix := 1; !reserved && suffix < 100; suffix++ {
		if suffix > 1 {
			// Only normalized ASCII display symbols reach the generated branch.
			native = preferred[:min(len(preferred), 4)] + strconv.Itoa(suffix)
			if native == preferred {
				continue
			}
		}
		if err := tx.QueryRow(ctx, pg.queries.TryReserveNativeSymbol, native, kind, owner).Scan(&reserved); err != nil {
			return "", err
		}
		if !reserved && !allowGenerated {
			break
		}
	}
	if !reserved {
		return "", ErrNativeSymbolUnavailable
	}
	if _, err := tx.Exec(ctx, pg.queries.ReserveDisplaySymbol, display, kind, owner); err != nil {
		return "", err
	}
	return native, nil
}
