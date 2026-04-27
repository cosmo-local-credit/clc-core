package api

type (
	OKResponse struct {
		Ok          bool           `json:"ok"`
		Description string         `json:"description"`
		Result      map[string]any `json:"result"`
	}

	ErrResponse struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
		ErrCode     string `json:"errorCode"`
	}

	DeployPoolRequest struct {
		Name             string `json:"name" validate:"required"`
		Symbol           string `json:"symbol" validate:"required"`
		Decimals         string `json:"decimals" validate:"required"`
		Owner            string `json:"owner" validate:"required,eth_addr_checksum"`
		QuoterType       string `json:"quoterType" validate:"required,oneof=decimal oracle relative"`
		FeesDecoupled    bool   `json:"feesDecoupled"`
		FeePolicyDefault string `json:"feePolicyDefault" validate:"omitempty"`
		BaseCurrency     string `json:"baseCurrency,omitempty" validate:"omitempty,eth_addr_checksum"`
	}

	DeployTokenRequest struct {
		Name            string `json:"name" validate:"required"`
		Symbol          string `json:"symbol" validate:"required"`
		Decimals        string `json:"decimals" validate:"required"`
		Owner           string `json:"owner" validate:"required,eth_addr_checksum"`
		ExpiryTimestamp string `json:"expiryTimestamp,omitempty" validate:"omitempty"`
	}

	OTXByAccountRequest struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
		PerPage uint64 `query:"perPage"`
		Next    bool   `query:"next"`
		Cursor  uint64 `query:"cursor"`
	}

	AccountAddressParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
	}
)

const (
	ErrCodeInternalServerError = "E01"
	ErrCodeInvalidJSON         = "E02"
	ErrCodeNotFound            = "E03"
	ErrCodeValidationFailed    = "E04"
	ErrCodeMethodNotAllowed    = "E05"
	ErrJWTAuth                 = "E06"
	ErrNoRecordFound           = "E07"
	ErrCodePayloadTooLarge     = "E08"
	ErrCodeRequestTimeout      = "E09"
)
