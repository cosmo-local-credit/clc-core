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
		ErrCode     string `json:"code"`
	}

	DeployPoolRequest struct {
		Name             string   `json:"name" validate:"required"`
		Symbol           string   `json:"symbol" validate:"required"`
		Decimals         string   `json:"decimals" validate:"required"`
		Owner            string   `json:"owner" validate:"required,eth_addr_checksum"`
		QuoterType       string   `json:"quoterType" validate:"omitempty,oneof=decimal oracle relative"`
		FeesDecoupled    bool     `json:"feesDecoupled"`
		FeePolicyDefault string   `json:"feePolicyDefault" validate:"required"`
		BaseCurrency     string   `json:"baseCurrency,omitempty" validate:"omitempty,eth_addr_checksum"`
		Description      string   `json:"description,omitempty"`
		Website          string   `json:"website,omitempty" validate:"omitempty,url"`
		Email            string   `json:"email,omitempty" validate:"omitempty,email"`
		Phone            string   `json:"phone,omitempty"`
		LocationName     string   `json:"locationName,omitempty"`
		GeoLat           *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng           *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		Tags             []string `json:"tags,omitempty"`
	}

	DeployTokenRequest struct {
		Name            string   `json:"name" validate:"required"`
		Symbol          string   `json:"symbol" validate:"required"`
		Decimals        string   `json:"decimals" validate:"required"`
		Owner           string   `json:"owner" validate:"required,eth_addr_checksum"`
		ExpiryTimestamp string   `json:"expiryTimestamp,omitempty" validate:"omitempty"`
		Description     string   `json:"description,omitempty"`
		Website         string   `json:"website,omitempty" validate:"omitempty,url"`
		Email           string   `json:"email,omitempty" validate:"omitempty,email"`
		Phone           string   `json:"phone,omitempty"`
		VoucherUoa      string   `json:"voucherUoa,omitempty"`
		VoucherValue    *float64 `json:"voucherValue,omitempty" validate:"omitempty,gte=0"`
		LocationName    string   `json:"locationName,omitempty"`
		GeoLat          *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng          *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		Tags            []string `json:"tags,omitempty"`
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

	PretiumTriggerOnrampRequest struct {
		Address     string  `json:"address" validate:"required,eth_addr_checksum"`
		PhoneNumber string  `json:"phoneNumber,omitempty"`
		Asset       string  `json:"asset" validate:"required,oneof=USDT USDC cUSD"`
		Amount      float64 `json:"amount" validate:"required,gte=20,lte=250000"`
	}

	PretiumLinkRequest struct {
		PublicKey   string `json:"publicKey" validate:"required,eth_addr_checksum"`
		PhoneNumber string `json:"phoneNumber" validate:"required"`
	}

	PhoneNumberParam struct {
		PhoneNumber string `param:"phoneNumber" validate:"required"`
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

	// Relayer (EIP-7702 / Calibur) types.

	RelayerEOANonceParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
	}

	CaliburNonceParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
		KeyHash string `param:"keyHash" validate:"omitempty,len=66,startswith=0x,hexadecimal"`
	}

	EIP7702Authorization struct {
		ChainID         string `json:"chainId"         validate:"required,numeric"`
		ContractAddress string `json:"contractAddress" validate:"required,eth_addr_checksum"`
		Nonce           string `json:"nonce"           validate:"required,numeric"`
		YParity         uint8  `json:"yParity"         validate:"oneof=0 1"`
		R               string `json:"r"               validate:"required,len=66,startswith=0x"`
		S               string `json:"s"               validate:"required,len=66,startswith=0x"`
	}

	DelegateRequest struct {
		EphemeralAddress string               `json:"ephemeralAddress" validate:"required,eth_addr_checksum"`
		Authorization    EIP7702Authorization `json:"authorization"    validate:"required"`
	}

	CaliburCall struct {
		To    string `json:"to"    validate:"required,eth_addr_checksum"`
		Value string `json:"value" validate:"required,numeric"`
		Data  string `json:"data"  validate:"required,startswith=0x"`
	}

	CaliburBatchedCall struct {
		Calls           []CaliburCall `json:"calls"           validate:"required,min=1,max=16,dive"`
		RevertOnFailure bool          `json:"revertOnFailure"`
	}

	CaliburSignedBatch struct {
		BatchedCall CaliburBatchedCall `json:"batchedCall" validate:"required"`
		Nonce       string             `json:"nonce"       validate:"required,numeric"`
		KeyHash     string             `json:"keyHash"     validate:"required,len=66,startswith=0x,hexadecimal"`
		Executor    string             `json:"executor"    validate:"required,eth_addr_checksum"`
		Deadline    string             `json:"deadline"    validate:"required,numeric"`
	}

	RelayRequest struct {
		UserAddress   string                `json:"userAddress"   validate:"required,eth_addr_checksum"`
		SignedBatch   CaliburSignedBatch    `json:"signedBatch"   validate:"required"`
		Signature     string                `json:"signature"     validate:"required,startswith=0x"`
		SignatureType string                `json:"signatureType" validate:"required,oneof=secp256k1 webauthn"`
		Authorization *EIP7702Authorization `json:"authorization,omitempty" validate:"omitempty"`
		// Registration-time fields (passed alongside the first secp256k1-signed
		// relay so the backend can persist the WebAuthn credential mapping).
		CredentialID string `json:"credentialId,omitempty" validate:"omitempty"`
		PublicKeyX   string `json:"publicKeyX,omitempty"   validate:"omitempty,numeric"`
		PublicKeyY   string `json:"publicKeyY,omitempty"   validate:"omitempty,numeric"`
	}

	// RelayPreviewRequest asks the relayer to resolve the Calibur nonce and
	// compute the EIP-712 digest for a batch, so the dapp can sign exactly
	// what the relayer will submit. No signature is involved.
	RelayPreviewRequest struct {
		UserAddress string             `json:"userAddress" validate:"required,eth_addr_checksum"`
		BatchedCall CaliburBatchedCall `json:"batchedCall" validate:"required"`
		KeyHash     string             `json:"keyHash"     validate:"omitempty,len=66,startswith=0x,hexadecimal"`
		Executor    string             `json:"executor"    validate:"required,eth_addr_checksum"`
		Deadline    string             `json:"deadline"    validate:"required,numeric"`
	}

	WebAuthnRecoverRequest struct {
		CredentialID      string `json:"credentialId"      validate:"required"`
		AuthenticatorData string `json:"authenticatorData" validate:"required"`
		ClientDataJSON    string `json:"clientDataJSON"    validate:"required"`
		Signature         string `json:"signature"         validate:"required"`
	}

	// SIWEVerifyRequest is the unified sign-in / sign-up request.
	// Both flows share the same endpoint; the server upserts the user.
	SIWEVerifyRequest struct {
		Message   string `json:"message"   validate:"required"`
		Signature string `json:"signature" validate:"required"`
	}

	// UpdateProfileRequest carries optional profile fields.
	UpdateProfileRequest struct {
		Name    string `json:"name"    validate:"omitempty,max=80"`
		Email   string `json:"email"   validate:"omitempty,email"`
		Phone   string `json:"phone"`
		Country string `json:"country" validate:"omitempty,len=2"`
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

	ErrCodeRelayerDisabled            = "E12"
	ErrCodeAuthorizationRequired      = "E13"
	ErrCodeRelayerExecutorMismatch    = "E14"
	ErrCodeWebAuthnVerificationFailed = "E15"
	ErrCodeConflict                   = "E16"
)
