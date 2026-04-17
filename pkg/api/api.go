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

	PoolDeployRequest struct {
		Name   string `json:"name" validate:"required"`
		Symbol string `json:"symbol" validate:"required"`
		Owner  string `json:"owner" validate:"required,eth_addr_checksum"`
	}

	DeployTokenRequest struct {
		Name            string `json:"name" validate:"required"`
		Symbol          string `json:"symbol" validate:"required"`
		Decimals        string `json:"decimals" validate:"required"`
		Owner           string `json:"owner" validate:"required,eth_addr_checksum"`
		ExpiryTimestamp string `json:"expiryTimestamp,omitempty" validate:"omitempty"`
	}
)

const (
	ErrCodeInternalServerError = "E01"
	ErrCodeInvalidJSON         = "E02"
	ErrCodeValidationFailed = "E04"
	ErrJWTAuth              = "E06"
	ErrNoRecordFound        = "E07"
)
