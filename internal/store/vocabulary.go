package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	VocabularyEntry struct {
		Value    string `db:"value" json:"value"`
		IsCustom bool   `db:"is_custom" json:"isCustom"`
	}

	TagVocabularyUsage struct {
		DeployedTokens int64 `db:"deployed_tokens" json:"deployedTokens"`
		DeployedPools  int64 `db:"deployed_pools" json:"deployedPools"`
		Vouchers       int64 `db:"vouchers" json:"vouchers"`
		Pools          int64 `db:"pools" json:"pools"`
		Posts          int64 `db:"posts" json:"posts"`
		Offerings      int64 `db:"offerings" json:"offerings"`
		Catalogs       int64 `db:"catalogs" json:"catalogs"`
		Wants          int64 `db:"wants" json:"wants"`
		Deleted        bool  `db:"deleted" json:"-"`
	}

	UnitVocabularyUsage struct {
		Offerings int64 `db:"offerings" json:"offerings"`
		Deleted   bool  `db:"deleted" json:"-"`
	}

	TagVocabulary struct {
		TagVocabularyUsage

		Value    string `db:"value"     json:"value"`
		IsCustom bool   `db:"is_custom" json:"isCustom"`
	}

	UnitVocabulary struct {
		UnitVocabularyUsage

		Value    string `db:"value"     json:"value"`
		IsCustom bool   `db:"is_custom" json:"isCustom"`
	}

	UnitOfAccountVocabularyUsage struct {
		Profiles           int64 `db:"profiles"            json:"profiles"`
		Vouchers           int64 `db:"vouchers"            json:"vouchers"`
		Pools              int64 `db:"pools"               json:"pools"`
		VoucherDeployments int64 `db:"voucher_deployments" json:"voucherDeployments"`
		PoolDeployments    int64 `db:"pool_deployments"    json:"poolDeployments"`
		Deleted            bool  `db:"deleted"             json:"-"`
	}

	UnitOfAccountVocabulary struct {
		UnitOfAccountVocabularyUsage

		Value      string `db:"value"     json:"value"`
		IsCustom   bool   `db:"is_custom" json:"isCustom"`
		TotalCount int64  `db:"total"     json:"total"`
	}

	// Vocabulary is every value in both vocabularies, built-in and custom, so the
	// console can rename the shipped defaults as well as clean up additions.
	Vocabulary struct {
		Tags           []TagVocabulary           `json:"tags"`
		Units          []UnitVocabulary          `json:"units"`
		UnitsOfAccount []UnitOfAccountVocabulary `json:"unitsOfAccount"`
	}
)

func (u TagVocabularyUsage) Total() int64 {
	return u.DeployedTokens + u.DeployedPools + u.Vouchers + u.Pools + u.Posts + u.Offerings + u.Catalogs + u.Wants
}

func (u UnitOfAccountVocabularyUsage) Total() int64 {
	return u.Profiles + u.Vouchers + u.Pools + u.VoucherDeployments + u.PoolDeployments
}

func (pg *Pg) FindTokenTag(ctx context.Context, tx pgx.Tx, value string) (string, error) {
	var canonical string
	err := tx.QueryRow(ctx, pg.queries.FindTokenTag, value).Scan(&canonical)
	return canonical, err
}

func (pg *Pg) InsertCustomTokenTag(ctx context.Context, tx pgx.Tx, value string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertCustomTokenTag, value)
	return err
}

func (pg *Pg) ListOfferingUnits(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, pg.queries.ListOfferingUnits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var units []string
	for rows.Next() {
		var unit string
		if err := rows.Scan(&unit); err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	return units, rows.Err()
}

func (pg *Pg) FindOfferingUnit(ctx context.Context, tx pgx.Tx, value string) (string, error) {
	var canonical string
	err := tx.QueryRow(ctx, pg.queries.FindOfferingUnit, value).Scan(&canonical)
	return canonical, err
}

func (pg *Pg) InsertCustomOfferingUnit(ctx context.Context, tx pgx.Tx, value string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertCustomOfferingUnit, value)
	return err
}

func (pg *Pg) ListUnitsOfAccount(ctx context.Context, tx pgx.Tx) ([]VocabularyEntry, error) {
	var entries []VocabularyEntry
	if err := pgxscan.Select(ctx, tx, &entries, pg.queries.ListUnitsOfAccount); err != nil {
		return nil, err
	}
	return entries, nil
}

func (pg *Pg) FindUnitOfAccount(ctx context.Context, tx pgx.Tx, value string) (VocabularyEntry, error) {
	var entry VocabularyEntry
	err := pgxscan.Get(ctx, tx, &entry, pg.queries.FindUnitOfAccount, value)
	return entry, err
}

func (pg *Pg) InsertCustomUnitOfAccount(ctx context.Context, tx pgx.Tx, value string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertCustomUnitOfAccount, value)
	return err
}

func (pg *Pg) ListVocabulary(ctx context.Context, tx pgx.Tx) (Vocabulary, error) {
	vocabulary := Vocabulary{
		Tags:           []TagVocabulary{},
		Units:          []UnitVocabulary{},
		UnitsOfAccount: []UnitOfAccountVocabulary{},
	}
	if err := pgxscan.Select(ctx, tx, &vocabulary.Tags, pg.queries.ListTokenTagVocabulary); err != nil {
		return Vocabulary{}, err
	}
	if err := pgxscan.Select(ctx, tx, &vocabulary.Units, pg.queries.ListOfferingUnitVocabulary); err != nil {
		return Vocabulary{}, err
	}
	if err := pgxscan.Select(ctx, tx, &vocabulary.UnitsOfAccount, pg.queries.ListUnitOfAccountVocabulary); err != nil {
		return Vocabulary{}, err
	}
	return vocabulary, nil
}

func (pg *Pg) LockUnitOfAccount(ctx context.Context, tx pgx.Tx, value string) (VocabularyEntry, error) {
	var entry VocabularyEntry
	err := pgxscan.Get(ctx, tx, &entry, pg.queries.LockUnitOfAccount, value)
	return entry, err
}

func (pg *Pg) LockTokenTag(ctx context.Context, tx pgx.Tx, value string) (VocabularyEntry, error) {
	var entry VocabularyEntry
	err := pgxscan.Get(ctx, tx, &entry, pg.queries.LockTokenTag, value)
	return entry, err
}

func (pg *Pg) LockOfferingUnit(ctx context.Context, tx pgx.Tx, value string) (VocabularyEntry, error) {
	var entry VocabularyEntry
	err := pgxscan.Get(ctx, tx, &entry, pg.queries.LockOfferingUnit, value)
	return entry, err
}

func (pg *Pg) DeleteCustomTokenTag(ctx context.Context, tx pgx.Tx, value string) (TagVocabularyUsage, error) {
	var usage TagVocabularyUsage
	err := pgxscan.Get(ctx, tx, &usage, pg.queries.DeleteCustomTokenTag, value)
	return usage, err
}

func (pg *Pg) DeleteCustomOfferingUnit(ctx context.Context, tx pgx.Tx, value string) (UnitVocabularyUsage, error) {
	var usage UnitVocabularyUsage
	err := pgxscan.Get(ctx, tx, &usage, pg.queries.DeleteCustomOfferingUnit, value)
	return usage, err
}

// RenameVocabulary renames any tag but only a custom unit: the built-in units
// are the apps' translation keys.
func (pg *Pg) RenameVocabulary(ctx context.Context, tx pgx.Tx, tag bool, value, replacement string) (string, error) {
	query := pg.queries.RenameCustomOfferingUnit
	if tag {
		query = pg.queries.RenameTokenTag
	}
	var renamed string
	err := tx.QueryRow(ctx, query, value, replacement).Scan(&renamed)
	return renamed, err
}

// HasUnfinishedTagDeployment checks jobs whose saved tag names are outside the
// foreign keys updated by a rename.
func (pg *Pg) HasUnfinishedTagDeployment(ctx context.Context, tx pgx.Tx, value string) (bool, error) {
	var installed bool
	// Catalog-only databases may not have a queue installed.
	if err := tx.QueryRow(ctx, "SELECT to_regclass('river_job') IS NOT NULL").Scan(&installed); err != nil {
		return false, err
	}
	if !installed {
		return false, nil
	}
	var pending bool
	err := tx.QueryRow(ctx, pg.queries.HasUnfinishedTagDeployment, value).Scan(&pending)
	return pending, err
}

func (pg *Pg) RenameUnitOfAccount(ctx context.Context, tx pgx.Tx, value, replacement string) (string, error) {
	var renamed string
	err := tx.QueryRow(ctx, pg.queries.RenameCustomUnitOfAccount, value, replacement).Scan(&renamed)
	return renamed, err
}

func (pg *Pg) HasUnfinishedUnitOfAccountDeployment(ctx context.Context, tx pgx.Tx, value string) (bool, error) {
	var installed bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('river_job') IS NOT NULL").Scan(&installed); err != nil {
		return false, err
	}
	if !installed {
		return false, nil
	}
	var pending bool
	err := tx.QueryRow(ctx, pg.queries.HasUnfinishedUOADeployment, value).Scan(&pending)
	return pending, err
}

func (pg *Pg) DeleteUnusedCustomUnitOfAccount(
	ctx context.Context,
	tx pgx.Tx,
	value string,
) (UnitOfAccountVocabularyUsage, error) {
	var usage UnitOfAccountVocabularyUsage
	err := pgxscan.Get(ctx, tx, &usage, pg.queries.DeleteUnusedCustomUOA, value)
	return usage, err
}
