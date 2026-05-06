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
		QuoterType       string `json:"quoterType" validate:"omitempty,oneof=decimal oracle relative"`
		FeesDecoupled    bool   `json:"feesDecoupled"`
		FeePolicyDefault string `json:"feePolicyDefault" validate:"required"`
		BaseCurrency     string `json:"baseCurrency,omitempty" validate:"omitempty,eth_addr_checksum"`
	}

	DeployTokenRequest struct {
		Name            string `json:"name" validate:"required"`
		Symbol          string `json:"symbol" validate:"required"`
		Decimals        string `json:"decimals" validate:"required"`
		Owner           string `json:"owner" validate:"required,eth_addr_checksum"`
		ExpiryTimestamp string `json:"expiryTimestamp,omitempty" validate:"omitempty"`
	}

	RegisterVersionRequest struct {
		Version               string `json:"version" validate:"required"`
		ContractType          string `json:"contractType" validate:"required,oneof=token pool"`
		ImplementationAddress string `json:"implementationAddress" validate:"required,eth_addr_checksum"`
	}

	UpgradeTokenRequest struct {
		Address string `json:"address" validate:"required,eth_addr_checksum"`
	}

	UpgradePoolRequest struct {
		Address string `json:"address" validate:"required,eth_addr_checksum"`
	}

	TrackOTXRequest struct {
		TrackingID string `param:"trackingId" validate:"required,uuid"`
	}

	CursorPaginationRequest struct {
		PerPage uint64 `query:"perPage"`
		Next    bool   `query:"next"`
		Cursor  uint64 `query:"cursor"`
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

	// Auth request/response types

	SignupRequest struct {
		Message   string `json:"message" validate:"required"`
		Signature string `json:"signature" validate:"required"`
		Name      string `json:"name" validate:"required"`
		Email     string `json:"email" validate:"required,email"`
	}

	SigninRequest struct {
		Message   string `json:"message" validate:"required"`
		Signature string `json:"signature" validate:"required"`
	}

	// Admin request types

	UpdateUserStatusRequest struct {
		// Status is derived from the route; no request body is required.
	}

	UpdateUserLimitsRequest struct {
		MaxPools  int `json:"maxPools" validate:"gte=0"`
		MaxTokens int `json:"maxTokens" validate:"gte=0"`
	}

	// EthAddressParam is a path-parameter type for Ethereum addresses.
	// Address normalisation (lowercasing / checksum) is handled in the handler
	// layer, so checksum validation is intentionally omitted here.
	EthAddressParam struct {
		Address string `param:"address" validate:"required"`
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
	ErrCodeForbidden           = "E10"
	ErrCodeLimitExceeded       = "E11"
)
