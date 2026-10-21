package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	customTagMaxLength  = 100
	customUnitMaxLength = 32
)

// canonicalVocabularyValue is the one spelling rule for user-created values.
// Existing values win before this runs, so "KG" continues to resolve to the
// built-in "kg" while a genuinely new "produce crate" becomes "Produce Crate".
func canonicalVocabularyValue(value string) string {
	words := strings.Fields(value)
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}

func (a *API) ensureTokenTags(c *echo.Context, tx pgx.Tx, requested []string) ([]string, bool, error) {
	if len(requested) == 0 {
		return []string{}, false, nil
	}

	ctx := c.Request().Context()
	canonical := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		collapsed := strings.Join(strings.Fields(raw), " ")
		if collapsed == "" {
			return nil, true, vocabularyValidationError(c, "tags must not contain blank values")
		}
		if utf8.RuneCountInString(collapsed) > customTagMaxLength {
			return nil, true, vocabularyValidationError(c, "tags must be 100 characters or fewer")
		}
		if strings.Contains(collapsed, ",") {
			return nil, true, vocabularyValidationError(c, "tags must not contain commas")
		}

		value, err := a.store.FindTokenTag(ctx, tx, collapsed)
		if errors.Is(err, pgx.ErrNoRows) {
			candidate := canonicalVocabularyValue(collapsed)
			if err := a.store.InsertCustomTokenTag(ctx, tx, candidate); err != nil {
				return nil, true, handlePostgresError(c, err)
			}
			value, err = a.store.FindTokenTag(ctx, tx, candidate)
		}
		if err != nil {
			return nil, true, handlePostgresError(c, err)
		}

		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return nil, true, vocabularyValidationError(c, "tags must not contain duplicate values")
		}
		seen[key] = struct{}{}
		canonical = append(canonical, value)
	}
	slices.Sort(canonical)
	return canonical, false, nil
}

func (a *API) ensureOfferingUnit(c *echo.Context, tx pgx.Tx, requested string) (string, bool, error) {
	collapsed := strings.Join(strings.Fields(requested), " ")
	if collapsed == "" {
		return "", true, vocabularyValidationError(c, "unit must not be blank")
	}
	if utf8.RuneCountInString(collapsed) > customUnitMaxLength {
		return "", true, vocabularyValidationError(c, "unit must be 32 characters or fewer")
	}

	ctx := c.Request().Context()
	value, err := a.store.FindOfferingUnit(ctx, tx, collapsed)
	if errors.Is(err, pgx.ErrNoRows) {
		candidate := canonicalVocabularyValue(collapsed)
		if err := a.store.InsertCustomOfferingUnit(ctx, tx, candidate); err != nil {
			return "", true, handlePostgresError(c, err)
		}
		value, err = a.store.FindOfferingUnit(ctx, tx, candidate)
	}
	if err != nil {
		return "", true, handlePostgresError(c, err)
	}
	return value, false, nil
}

func (a *API) ensureUnitOfAccount(
	c *echo.Context,
	tx pgx.Tx,
	requested string,
) (store.VocabularyEntry, bool, error) {
	collapsed := strings.Join(strings.Fields(requested), " ")
	if collapsed == "" {
		return store.VocabularyEntry{}, true, vocabularyValidationError(c, "unit of account must not be blank")
	}
	if utf8.RuneCountInString(collapsed) > customUnitMaxLength {
		return store.VocabularyEntry{}, true, vocabularyValidationError(c, "unit of account must be 32 characters or fewer")
	}

	ctx := c.Request().Context()
	entry, err := a.store.FindUnitOfAccount(ctx, tx, collapsed)
	if errors.Is(err, pgx.ErrNoRows) {
		candidate := canonicalVocabularyValue(collapsed)
		if err := a.store.InsertCustomUnitOfAccount(ctx, tx, candidate); err != nil {
			return store.VocabularyEntry{}, true, handlePostgresError(c, err)
		}
		entry, err = a.store.FindUnitOfAccount(ctx, tx, candidate)
	}
	if err != nil {
		return store.VocabularyEntry{}, true, handlePostgresError(c, err)
	}
	return entry, false, nil
}

func (a *API) offeringUnitsHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	units, err := a.store.ListOfferingUnits(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Available offering units", Result: map[string]any{"units": units},
	})
}

func (a *API) unitsOfAccountHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	units, err := a.store.ListUnitsOfAccount(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Available units of account", Result: map[string]any{"unitsOfAccount": units},
	})
}

func (a *API) adminVocabularyHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	vocabulary, err := a.store.ListVocabulary(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Vocabulary", Result: map[string]any{
			"tags": vocabulary.Tags, "units": vocabulary.Units, "unitsOfAccount": vocabulary.UnitsOfAccount,
		},
	})
}

func (a *API) adminCreateUnitOfAccountHandler(c *echo.Context) error {
	var req struct {
		Value string `json:"value" validate:"required"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	entry, handled, err := a.ensureUnitOfAccount(c, tx, req.Value)
	if handled || err != nil {
		return err
	}
	if !entry.IsCustom {
		return vocabularyConflictError(c, "unit of account already exists as a built-in value")
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusCreated, apiresp.OKResponse{
		Ok: true, Description: "Custom unit of account created", Result: map[string]any{"value": entry.Value},
	})
}

func (a *API) adminRenameUnitOfAccountHandler(c *echo.Context) error {
	var req struct {
		Value       string `json:"value" validate:"required"`
		Replacement string `json:"replacement" validate:"required"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.Value = strings.TrimSpace(req.Value)
	req.Replacement = strings.Join(strings.Fields(req.Replacement), " ")
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	if utf8.RuneCountInString(req.Replacement) > customUnitMaxLength {
		return vocabularyValidationError(c, "unit of account must be 32 characters or fewer")
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	entry, err := a.store.LockUnitOfAccount(ctx, tx, req.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return vocabularyNotFoundError(c)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !entry.IsCustom {
		return vocabularyConflictError(c, "built-in units of account cannot be renamed")
	}
	pending, err := a.store.HasUnfinishedUnitOfAccountDeployment(ctx, tx, entry.Value)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if pending {
		return vocabularyConflictError(c, "This unit of account is being used by an unfinished deployment. Please try again after the deployment finishes.")
	}
	replacement := canonicalVocabularyValue(req.Replacement)
	renamed, err := a.store.RenameUnitOfAccount(ctx, tx, entry.Value, replacement)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Unit of account renamed", Result: map[string]any{"value": renamed},
	})
}

func (a *API) adminDeleteUnitOfAccountHandler(c *echo.Context) error {
	var req deleteVocabularyRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.Value = strings.TrimSpace(req.Value)
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	entry, err := a.store.LockUnitOfAccount(ctx, tx, req.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return vocabularyNotFoundError(c)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !entry.IsCustom {
		return vocabularyConflictError(c, "built-in units of account cannot be deleted")
	}
	pending, err := a.store.HasUnfinishedUnitOfAccountDeployment(ctx, tx, entry.Value)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if pending {
		return vocabularyConflictError(c, "This unit of account is being used by an unfinished deployment. Please try again after the deployment finishes.")
	}
	usage, err := a.store.DeleteUnusedCustomUnitOfAccount(ctx, tx, entry.Value)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !usage.Deleted {
		return vocabularyConflictError(c, "unit of account is still in use")
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Custom unit of account deleted", Result: map[string]any{
			"value": entry.Value, "usage": usage, "total": usage.Total(),
		},
	})
}

type (
	deleteVocabularyRequest struct {
		Value string `json:"value" validate:"required"`
	}
)

func (a *API) adminDeleteTokenTagHandler(c *echo.Context) error {
	return a.deleteVocabularyValue(c, true)
}

func (a *API) adminDeleteOfferingUnitHandler(c *echo.Context) error {
	return a.deleteVocabularyValue(c, false)
}

func (a *API) deleteVocabularyValue(c *echo.Context, tag bool) error {
	var req deleteVocabularyRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.Value = strings.TrimSpace(req.Value)
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var entry store.VocabularyEntry
	if tag {
		entry, err = a.store.LockTokenTag(ctx, tx, req.Value)
	} else {
		entry, err = a.store.LockOfferingUnit(ctx, tx, req.Value)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return vocabularyNotFoundError(c)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !entry.IsCustom {
		return vocabularyConflictError(c, "built-in vocabulary values cannot be deleted")
	}

	var result map[string]any
	if tag {
		usage, err := a.store.DeleteCustomTokenTag(ctx, tx, entry.Value)
		if err != nil {
			return handlePostgresError(c, err)
		}
		if !usage.Deleted {
			return vocabularyConflictError(c, "custom tag could not be deleted")
		}
		result = map[string]any{"value": entry.Value, "usage": usage, "total": usage.Total()}
	} else {
		usage, err := a.store.DeleteCustomOfferingUnit(ctx, tx, entry.Value)
		if err != nil {
			return handlePostgresError(c, err)
		}
		if !usage.Deleted {
			return vocabularyConflictError(c, "custom unit could not be deleted")
		}
		result = map[string]any{"value": entry.Value, "usage": usage, "total": usage.Offerings}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Custom vocabulary value deleted", Result: result,
	})
}

func vocabularyValidationError(c *echo.Context, description string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: description,
	})
}

func vocabularyNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "vocabulary value not found",
	})
}

func vocabularyConflictError(c *echo.Context, description string) error {
	return c.JSON(http.StatusConflict, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeConflict, Description: description,
	})
}

func (a *API) adminRenameTokenTagHandler(c *echo.Context) error {
	return a.renameVocabulary(c, true)
}

func (a *API) adminRenameOfferingUnitHandler(c *echo.Context) error {
	return a.renameVocabulary(c, false)
}

func (a *API) renameVocabulary(c *echo.Context, tag bool) error {
	var req struct {
		Value       string `json:"value" validate:"required"`
		Replacement string `json:"replacement" validate:"required"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.Value = strings.TrimSpace(req.Value)
	req.Replacement = strings.Join(strings.Fields(req.Replacement), " ")
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	maxLength := customUnitMaxLength
	if tag {
		maxLength = customTagMaxLength
	}
	if utf8.RuneCountInString(req.Replacement) > maxLength {
		return vocabularyValidationError(c, "vocabulary value is too long")
	}
	if tag && strings.Contains(req.Replacement, ",") {
		return vocabularyValidationError(c, "tags must not contain commas")
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	lock := a.store.LockOfferingUnit
	if tag {
		lock = a.store.LockTokenTag
	}
	entry, err := lock(ctx, tx, req.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return vocabularyNotFoundError(c)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	// The apps translate the built-in units by their stored spelling, so renaming
	// one would strip every locale's label from it. Built-in tags carry no labels.
	if !tag && !entry.IsCustom {
		return vocabularyConflictError(c, "built-in offering units cannot be renamed")
	}
	if tag && req.Replacement != entry.Value {
		pending, err := a.store.HasUnfinishedTagDeployment(ctx, tx, entry.Value)
		if err != nil {
			return handlePostgresError(c, err)
		}
		if pending {
			return vocabularyConflictError(c, "This tag is being used by an unfinished voucher or pool deployment. Please try again after the deployment finishes.")
		}
	}
	renamed, err := a.store.RenameVocabulary(ctx, tx, tag, entry.Value, req.Replacement)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Vocabulary value renamed", Result: map[string]any{"value": renamed},
	})
}
