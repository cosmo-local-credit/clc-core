# CLC Core — API Reference

> **Version:** 2.0  
> **License:** [AGPL-3.0](../LICENSE)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Authentication](#2-authentication)
   - [Token Types](#21-token-types)
   - [SIWE Flow Summary](#22-siwe-flow-summary)
   - [Service Token Usage](#23-service-token-usage)
3. [Response Format](#3-response-format)
   - [Success](#31-success)
   - [Error](#32-error)
   - [Error Codes](#33-error-codes)
4. [Public Auth Endpoints (`/auth/*`)](#4-public-auth-endpoints-auth)
   - [POST /auth/nonce](#41-post-authnonce)
   - [POST /auth/signup](#42-post-authsignup)
   - [POST /auth/signin](#43-post-authsignin)
   - [POST /auth/logout](#44-post-authlogout)
   - [POST /auth/siwe/nonce](#45-post-authsiwenonce)
   - [POST /auth/siwe/verify](#46-post-authsiweverify)
   - [GET /auth/session](#47-get-authsession)
5. [Protected Endpoints (`/api/v2/*`)](#5-protected-endpoints-apiv2)
   - [GET /api/v2/system](#51-get-apiv2system)
   - [GET /api/v2/otx/track/:trackingId](#52-get-apiv2otxtracktrackingid)
   - [GET /api/v2/account/otx/:address](#53-get-apiv2accountotxaddress)
   - [GET /api/v2/account/status/:address](#54-get-apiv2accountstatusaddress)
   - [POST /api/v2/contracts/token](#55-post-apiv2contractstoken)
   - [POST /api/v2/contracts/pool](#56-post-apiv2contractspool)
   - [Pretium Backend Endpoints (`/api/v2/pretium/*`)](#57-pretium-backend-endpoints-apiv2pretium)
6. [User-Only Endpoints (`/api/v2/users/*`)](#6-user-only-endpoints-apiv2users)
   - [GET /api/v2/users/me](#61-get-apiv2usersme)
   - [GET /api/v2/users/me/tokens](#62-get-apiv2usersmetokens)
   - [GET /api/v2/users/me/pools](#63-get-apiv2usersmepools)
   - [GET /api/v2/users/me/deployments](#64-get-apiv2usersmedeployments)
7. [Superadmin Endpoints (`/api/v2/admin/*`)](#7-superadmin-endpoints-apiv2admin)
   - [GET /api/v2/admin/users](#71-get-apiv2adminusers)
   - [PUT /api/v2/admin/users/:address/approve](#72-put-apiv2adminusersaddressapprove)
   - [PUT /api/v2/admin/users/:address/reject](#73-put-apiv2adminusersaddressreject)
   - [PUT /api/v2/admin/users/:address/limits](#74-put-apiv2adminusersaddresslimits)
   - [GET /api/v2/admin/tokens](#75-get-apiv2admintokens)
   - [GET /api/v2/admin/pools](#76-get-apiv2adminpools)
   - [GET /api/v2/admin/deployments](#77-get-apiv2admindeployments)
8. [SIWE Message Construction](#8-siwe-message-construction)

---

## 1. Overview

| Property | Value |
|---|---|
| Base URL | `http(s)://<host>/` |
| Protected API prefix | `/api/v2` |
| Auth mechanism | SIWE (Sign-In With Ethereum, EIP-4361) |
| Default chain | Celo Mainnet (`chainId: 42220`) |
| Content-Type | `application/json` |

---

## 2. Authentication

### 2.1 Token Types

Two mutually exclusive token types are accepted on protected routes.

#### User JWT (`__clc_auth` cookie)

Issued after a successful `/auth/signin` or `/auth/siwe/verify` for an approved user. Delivered and expected as an **HttpOnly cookie** named `__clc_auth`.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Expiry | 30 days |
| Transport | `Set-Cookie: __clc_auth=<token>; HttpOnly` |

**Claims:**

| Claim | Type | Description |
|---|---|---|
| `userId` | `number` | Internal user identifier |
| `ethereumAddress` | `string` | Checksummed Ethereum address |
| `role` | `"USER"` \| `"SUPERADMIN"` | Authorization role |

#### Service JWT (`Authorization` header)

Long-lived tokens issued offline by the `gen-service-token` CLI tool. Intended for backend-to-backend integrations.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Expiry | Long-lived (no hard expiry) |
| Transport | `Authorization: Bearer <token>` header **or** `__ge_cust_auth` cookie |

**Claims:**

| Claim | Type | Description |
|---|---|---|
| `publicKey` | `string` | Service identity key |
| `service` | `true` | Marks the token as a service credential |

> **Important:** Service JWTs bypass user quota checks but **cannot** access user-only routes (`/api/v2/users/*`). Those routes require a user JWT.

### 2.2 SIWE Flow Summary

```
1. POST /auth/nonce        → receive a one-time nonce (5-min TTL)
2. Client builds an EIP-4361 message embedding the nonce, then signs it
   with a connected wallet
3. Either:
   POST /auth/signup       → create a new PENDING account with profile fields
   POST /auth/signin       → authenticate an existing APPROVED account
   POST /auth/siwe/verify  → unified flow: create unknown addresses as PENDING,
                             authenticate APPROVED accounts
                             → server sets __clc_auth HttpOnly cookie on success
4. Include cookie automatically on all /api/v2/* requests
5. POST /auth/logout       → invalidates the cookie
```

### 2.3 Service Token Usage

Generate a service token using the bundled CLI:

```clc-core/cmd/gen-service-token/main.go#L1-1
go run ./cmd/gen-service-token -config config.toml -service my-service
```

Pass the token with every request:

```/dev/null/http.txt#L1-1
Authorization: Bearer <token>
```

Service tokens are intended for backend-only flows such as `/api/v2/pretium/*`
proxy calls and contract upgrade/version registration.

---

## 3. Response Format

All responses are JSON objects. HTTP status codes are only used for top-level categories; use `ok` and `code` for programmatic branching.

### 3.1 Success

```/dev/null/success.json#L1-5
{
  "ok": true,
  "description": "Human-readable status message",
  "result": { ... }
}
```

### 3.2 Error

```/dev/null/error.json#L1-5
{
  "ok": false,
  "description": "Human-readable error message",
  "code": "E0X"
}
```

### 3.3 Error Codes

| Code | Meaning |
|---|---|
| `E01` | Internal server error |
| `E02` | Invalid or malformed JSON body |
| `E03` | Resource not found |
| `E04` | Validation failed |
| `E05` | Method not allowed |
| `E06` | JWT authentication failure |
| `E07` | No record found |
| `E08` | Payload too large |
| `E09` | Request timeout |
| `E10` | Forbidden (insufficient role or permissions) |
| `E11` | Rate or quota limit exceeded |

---

## 4. Public Auth Endpoints (`/auth/*`)

These endpoints require **no JWT**. They handle the SIWE authentication lifecycle.

---

### 4.1 `POST /auth/nonce`

Generates a one-time 32-character hex nonce to embed in a SIWE message. The nonce expires after **5 minutes** and is invalidated once consumed by a signup or signin attempt.

**Request body:** none

**Response `200`:**

```/dev/null/nonce-response.json#L1-7
{
  "ok": true,
  "description": "Nonce generated for SIWE challenge",
  "result": {
    "nonce": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
  }
}
```

---

### 4.2 `POST /auth/signup`

Registers a new Ethereum address. The client constructs and signs an EIP-4361 message client-side before submitting here.

**Verification steps performed by the server:**
1. Recovers the signing address from the SIWE message and signature.
2. Confirms the recovered address matches the `address` field in the message.
3. Validates the nonce is active and consumes it (one-time use).
4. Returns `409 Conflict` if the address is already registered.

**Account defaults:**
- Status starts as `PENDING` and requires approval before the account can sign in.
- `/auth/signup` does not auto-approve on the request path.
- Legacy federation auto-approval is triggered asynchronously by `/auth/siwe/verify` for pending users when the address exists in `sarafu_network.accounts.blockchain_address` on the configured federation database.
- New signups receive role `USER`. Superadmin role is granted by an operator updating records outside the public signup flow.

**Request body:**

```/dev/null/signup-request.json#L1-6
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>",
  "name":      "Alice",
  "email":     "alice@example.com"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | `string` | Yes | Full EIP-4361 plaintext message |
| `signature` | `string` | Yes | `0x`-prefixed 65-byte hex ECDSA signature |
| `name` | `string` | Yes | Display name for the account |
| `email` | `string` | Yes | Contact email address |

**Response `200`:**

```/dev/null/signup-response.json#L1-8
{
  "ok": true,
  "description": "Registration successful",
  "result": {
    "userId": 123,
    "status": "PENDING",
    "role":   "USER"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `409` | `E04` | Ethereum address already registered |
| `400` | `E02` | Malformed request body |
| `400` | `E04` | Invalid or expired nonce, signature mismatch |

---

### 4.3 `POST /auth/signin`

Authenticates an existing registered address via SIWE. On success, sets the `__clc_auth` HttpOnly JWT cookie (30-day expiry).

**Request body:**

```/dev/null/signin-request.json#L1-4
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | `string` | Yes | Full EIP-4361 plaintext message |
| `signature` | `string` | Yes | `0x`-prefixed 65-byte hex ECDSA signature |

**Response `200`:**

Sets `Set-Cookie: __clc_auth=<jwt>; HttpOnly; Path=/; Max-Age=2592000`.

```/dev/null/signin-response.json#L1-8
{
  "ok": true,
  "description": "Signed in successfully",
  "result": {
    "userId": 123,
    "role":   "USER"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `404` | `E03` | Ethereum address not registered |
| `403` | `E10` | Account status is `PENDING` or `REJECTED` |
| `400` | `E04` | Invalid or expired nonce, signature mismatch |

---

### 4.4 `POST /auth/logout`

Clears the `__clc_auth` session cookie. Safe to call even when not authenticated.

**Request body:** none

**Response `200`:**

```/dev/null/logout-response.json#L1-6
{
  "ok": true,
  "description": "Signed out successfully",
  "result": {}
}
```

---

### 4.5 `POST /auth/siwe/nonce`

Alias for `POST /auth/nonce`. Generates a one-time SIWE nonce for clients that expect SIWE routes under `/auth/siwe/*`.

**Request body:** none

**Response `200`:** same shape as [`POST /auth/nonce`](#41-post-authnonce).

---

### 4.6 `POST /auth/siwe/verify`

Unified SIWE sign-in / registration endpoint. The server verifies the SIWE signature and nonce, then:

- If the address already exists and is `APPROVED`, issues a JWT cookie and returns a token in the response body.
- If the address already exists and is `PENDING` or `REJECTED`, returns `403`.
- If the address is unknown, inserts a `PENDING` user record, queues a background legacy-federation approval check, and returns `403` until approval completes.

The legacy check is performed asynchronously by a River worker against `sarafu_network.accounts.blockchain_address` on the configured federation database, so this route does not block on the old database.

**Request body:**

```/dev/null/siwe-verify-request.json#L1-4
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>"
}
```

**Response `200` — approved user:**

```/dev/null/siwe-verify-response.json#L1-10
{
  "ok": true,
  "description": "Authenticated successfully",
  "result": {
    "token": "<jwt>",
    "address": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
    "expiresAt": "2026-07-17T12:00:00Z"
  }
}
```

Also sets `Set-Cookie: __clc_auth=<jwt>; HttpOnly; SameSite=Strict; Path=/; Expires=<30 days>`.

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `403` | `E10` | Account is newly registered as `PENDING`, still pending approval, or rejected |
| `400` | `E04` | Invalid or expired nonce |
| `401` | `E04` | Signature verification failed or signer/address mismatch |

---

### 4.7 `GET /auth/session`

Returns the active session address and chain ID. Designed for use by wallet SDKs (e.g. Reown AppKit) that need to detect existing session state on page load.

**Request body:** none

**Response `200` — authenticated:**

```/dev/null/session-response.json#L1-4
{
  "address": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "chainId": 42220
}
```

**Response `204 No Content` — no active session.**

No body is returned. Use this to redirect the user to a sign-in flow.

---

## 5. Protected Endpoints (`/api/v2/*`)

All routes in this section require **one** of:

- `__clc_auth` cookie containing a valid user JWT, **or**
- `Authorization: Bearer <token>` header containing a valid service JWT.

A missing or invalid credential returns:

```/dev/null/auth-error.json#L1-5
{
  "ok": false,
  "description": "Authentication required",
  "code": "E06"
}
```

---

### 5.1 `GET /api/v2/system`

Returns the server's signer public key and current build version. Useful for verifying the identity of the signing node.

**Auth:** user JWT or service JWT

**Response `200`:**

```/dev/null/system-response.json#L1-8
{
  "ok": true,
  "description": "Current system information",
  "result": {
    "systemSigner": "0x4a9f1234...",
    "build":        "v1.2.3"
  }
}
```

---

### 5.2 `GET /api/v2/otx/track/:trackingId`

Tracks an on-chain transaction (OTX) by its UUID. Returns the full transaction chain associated with the tracking ID.

**Auth:** user JWT or service JWT

**Access control:**
- Regular users (`USER` role) may only query tracking IDs that belong to their own account.
- Service JWTs may query any tracking ID.
- `SUPERADMIN` users may query any tracking ID.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `trackingId` | `string` (UUID) | The tracking UUID returned at deploy/operation time |

**Response `200`:**

```/dev/null/otx-track-response.json#L1-17
{
  "ok": true,
  "description": "OTX chain for tracking ID f47ac10b-...",
  "result": {
    "otx": [
      {
        "id":         1,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "otxType":    "TOKEN_PROXY_DEPLOY",
        "txHash":     "0xabc123...",
        "nonce":      5,
        "replaced":   false,
        "createdAt":  "2024-01-15T10:30:00Z"
      }
    ]
  }
}
```

**OTX object fields:**

| Field | Type | Description |
|---|---|---|
| `id` | `int` | Internal OTX record ID |
| `trackingId` | `string` (UUID) | Tracking UUID |
| `otxType` | `string` | Operation type (e.g. `TOKEN_PROXY_DEPLOY`) |
| `txHash` | `string` | On-chain transaction hash (`0x...`) |
| `nonce` | `int` | Account nonce at submission time |
| `replaced` | `bool` | Whether this tx was replaced by a later one |
| `createdAt` | `string` (ISO 8601) | Submission timestamp |

---

### 5.3 `GET /api/v2/account/otx/:address`

Returns a paginated list of OTXs for a custodial account address.

**Auth:** user JWT or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Checksummed Ethereum address |

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, paginates forward from `cursor` |
| `cursor` | `int` | — | Last seen OTX `id` from the previous page |

**Response `200`:**

```/dev/null/account-otx-response.json#L1-12
{
  "ok": true,
  "description": "Successfully fetched OTX for 0xd8dA6BF2...",
  "result": {
    "otx":   [ { "id": 1, "trackingId": "...", "otxType": "...", "txHash": "0x...", "nonce": 1, "replaced": false, "createdAt": "..." } ],
    "first": 1,
    "last":  50
  }
}
```

| Field | Type | Description |
|---|---|---|
| `otx` | `array` | Page of OTX objects (see [§5.2](#52-get-apiv2otxtracktrackingid) for field definitions) |
| `first` | `int` | Lowest OTX `id` in this page |
| `last` | `int` | Highest OTX `id` in this page — use as `cursor` for the next page |

---

### 5.4 `GET /api/v2/account/status/:address`

Returns on-chain and internal state for a custodial account.

**Auth:** user JWT or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Checksummed Ethereum address |

**Response `200`:**

```/dev/null/account-status-response.json#L1-10
{
  "ok": true,
  "description": "Custodial account status",
  "result": {
    "gasBalance":    "1000000000000000000",
    "networkNonce":  4,
    "internalNonce": 5,
    "active":        true
  }
}
```

| Field | Type | Description |
|---|---|---|
| `gasBalance` | `string` (wei) | Native gas token balance as a decimal string |
| `networkNonce` | `int` | Last confirmed nonce on-chain |
| `internalNonce` | `int` | Next nonce tracked internally (may be ahead of network) |
| `active` | `bool` | Whether the account is active and usable |

---

### 5.5 `POST /api/v2/contracts/token`

Submits a request to deploy an ERC-20 token contract. The deployment is queued and processed asynchronously; use the returned `trackingId` with [`GET /api/v2/otx/track/:trackingId`](#52-get-apiv2otxtracktrackingid) to monitor progress.

**Auth:** user JWT or service JWT

**Quota:** Regular `USER` accounts are limited to `maxTokens` deployments (default: **20**). `SUPERADMIN` users and service JWTs are exempt.

**Request body:**

```/dev/null/token-deploy-request.json#L1-8
{
  "name":            "My Token",
  "symbol":          "MTK",
  "decimals":        "6",
  "owner":           "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "expiryTimestamp": "2026-01-01T00:00:00Z"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | Yes | Full token name |
| `symbol` | `string` | Yes | Ticker symbol (e.g. `MTK`) |
| `decimals` | `string` (int) | Yes | Token decimal places (e.g. `"6"`) |
| `owner` | `string` | Yes | Checksummed Ethereum address of the token owner |
| `expiryTimestamp` | `string` (ISO 8601) | No | Optional expiry date for the token |

**Response `200`:**

```/dev/null/token-deploy-response.json#L1-7
{
  "ok": true,
  "description": "Token deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `400` | `E04` | Validation failure (missing fields, bad address) |
| `403` | `E11` | User has reached their `maxTokens` quota |

---

### 5.6 `POST /api/v2/contracts/pool`

Submits a request to deploy a SwapPool instance. Like token deployments, processing is asynchronous.

**Auth:** user JWT or service JWT

**Quota:** Regular `USER` accounts are limited to `maxPools` deployments (default: **10**). `SUPERADMIN` users and service JWTs are exempt.

**Request body:**

```/dev/null/pool-deploy-request.json#L1-10
{
  "name":             "My Pool",
  "symbol":           "MYP",
  "decimals":         "6",
  "owner":            "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "quoterType":       "decimal",
  "feesDecoupled":    false,
  "feePolicyDefault": "",
  "baseCurrency":     ""
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | Yes | Pool name |
| `symbol` | `string` | Yes | Pool symbol |
| `decimals` | `string` (int) | Yes | Decimal precision |
| `owner` | `string` | Yes | Checksummed owner address |
| `quoterType` | `string` | Yes | Pricing strategy — see below |
| `feesDecoupled` | `bool` | No | If `true`, fee policy executes independently of swaps (default: `false`) |
| `feePolicyDefault` | `string` | No | Default fee policy identifier |
| `baseCurrency` | `string` | Conditional | Checksummed non-zero token address; **required** when `quoterType` is `"oracle"` |

**`quoterType` values:**

| Value | Description |
|---|---|
| `"decimal"` | Fixed decimal-based pricing *(default)* |
| `"oracle"` | Price sourced from an on-chain oracle; `baseCurrency` must be set |
| `"relative"` | Pricing relative to pool reserves |

**Response `200`:**

```/dev/null/pool-deploy-response.json#L1-7
{
  "ok": true,
  "description": "Pool deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `400` | `E04` | Validation failure (missing fields, `baseCurrency` absent for oracle quoter) |
| `403` | `E11` | User has reached their `maxPools` quota |

---

### 5.7 `Pretium Backend Endpoints (/api/v2/pretium/*)`

These routes proxy the configured Pretium Ramp service for backend
consumers.

**Auth:** service JWT only

**Mount condition:** the route group is only registered when
`pretium.base_url` is configured.

**Config:**

```toml
[pretium]
base_url = "https://pretium.example.com/api/v1"
```

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v2/pretium/trigger-onramp` | Initiate fiat-to-crypto onramp |
| `POST` | `/api/v2/pretium/link` | Link a wallet address to a phone number |
| `GET` | `/api/v2/pretium/link/:phoneNumber` | Fetch wallet links for a phone number |
| `GET` | `/api/v2/pretium/transactions/:phoneNumber` | Fetch transactions for a phone number |
| `GET` | `/api/v2/pretium/transactions-by-address/:address` | Fetch transactions for an address |
| `GET` | `/api/v2/pretium/transactions-recent` | Fetch recent transactions |
| `GET` | `/api/v2/pretium/rates` | Fetch current Pretium rates |

**Behaviour:**
- Success responses are passed through from the upstream Pretium service.
- Upstream `5xx` responses are surfaced as `502 Bad Gateway`.
- Local validation failures still use this API's standard error envelope.

See [PRETIUM.md](./PRETIUM.md) for request bodies, auth expectations, and
integration notes.

---

## 6. User-Only Endpoints (`/api/v2/users/*`)

These endpoints require a valid **user JWT** (`__clc_auth` cookie). Service JWTs are rejected with `403 Forbidden`.

---

### 6.1 `GET /api/v2/users/me`

Returns the full profile of the currently authenticated user.

**Response `200`:**

```/dev/null/me-response.json#L1-16
{
  "ok": true,
  "description": "Current user profile",
  "result": {
    "user": {
      "id":              "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "ethereumAddress": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
      "name":            "Alice",
      "email":           "alice@example.com",
      "role":            "USER",
      "status":          "APPROVED",
      "maxPools":        10,
      "maxTokens":       20
    }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `id` | `string` (UUID) | Internal user ID |
| `ethereumAddress` | `string` | Checksummed Ethereum address |
| `name` | `string` | Display name |
| `email` | `string` | Email address |
| `role` | `"USER"` \| `"SUPERADMIN"` | Authorization role |
| `status` | `"PENDING"` \| `"APPROVED"` \| `"REJECTED"` | Account approval status |
| `maxPools` | `int` | Maximum number of pools this user may deploy |
| `maxTokens` | `int` | Maximum number of tokens this user may deploy |

---

### 6.2 `GET /api/v2/users/me/tokens`

Returns **confirmed** (on-chain) token contracts deployed by the authenticated user using cursor pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward newer records from `cursor` |
| `cursor` | `int` | — | Use `last` to fetch older records or `first` with `next=true` to move back toward newer records |

**Response `200`:**

```/dev/null/me-tokens-response.json#L1-18
{
  "ok": true,
  "description": "Confirmed tokens for user f47ac10b-...",
  "result": {
    "tokens": [
      {
        "id":      42,
        "address": "0xAbCd1234...",
        "symbol":  "MTK",
        "name":    "My Token",
        "admin":   "0xd8dA6BF2...",
        "createdAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 42,
    "last": 33
  }
}
```

| Field | Type | Description |
|---|---|---|
| `tokens` | `array` | Current page of deployed tokens |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page |

---

### 6.3 `GET /api/v2/users/me/pools`

Returns **confirmed** (on-chain) SwapPool contracts deployed by the authenticated user using cursor pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward newer records from `cursor` |
| `cursor` | `int` | — | Use `last` to fetch older records or `first` with `next=true` to move back toward newer records |

**Response `200`:**

```/dev/null/me-pools-response.json#L1-18
{
  "ok": true,
  "description": "Confirmed pools for user f47ac10b-...",
  "result": {
    "pools": [
      {
        "id":      51,
        "address": "0xAbCd5678...",
        "symbol":  "MYP",
        "name":    "My Pool",
        "admin":   "0xd8dA6BF2...",
        "createdAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 51,
    "last": 44
  }
}
```

| Field | Type | Description |
|---|---|---|
| `pools` | `array` | Current page of deployed pools |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page |

---

### 6.4 `GET /api/v2/users/me/deployments`

Returns the complete deployment history for the authenticated user, covering both tokens and pools in reverse-chronological order.

**Response `200`:**

```/dev/null/me-deployments-response.json#L1-13
{
  "ok": true,
  "description": "Deployment history for user f47ac10b-...",
  "result": {
    "deployments": [
      {
        "trackingId":    "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "resourceType":  "TOKEN",
        "createdAt":     "2024-01-15T10:30:00Z"
      }
    ]
  }
}
```

| Field | Type | Description |
|---|---|---|
| `trackingId` | `string` (UUID) | Use with [`GET /api/v2/otx/track/:trackingId`](#52-get-apiv2otxtracktrackingid) |
| `resourceType` | `"TOKEN"` \| `"POOL"` | Type of deployed resource |
| `createdAt` | `string` (ISO 8601) | When the deploy request was submitted |

---

## 7. Superadmin Endpoints (`/api/v2/admin/*`)

These endpoints require a caller with role `SUPERADMIN`. Any other caller (including service JWTs) receives `403 Forbidden` with error code `E10`.

The baseline migration seeds an initial approved superadmin account. Additional superadmins must be granted directly in the `users` table by an operator with write access; public signup and SIWE verify create normal `USER` accounts only.

---

### 7.1 `GET /api/v2/admin/users`

Lists all registered users. Optionally filter by account status.

**Query parameters:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `status` | `string` | No | Filter by status: `PENDING`, `APPROVED`, or `REJECTED` |

**Response `200`:**

```/dev/null/admin-users-response.json#L1-14
{
  "ok": true,
  "description": "Users list",
  "result": {
    "users": [
      {
        "id":              "f47ac10b-...",
        "ethereumAddress": "0xd8dA6BF2...",
        "role":            "USER",
        "status":          "PENDING"
      }
    ]
  }
}
```

---

### 7.2 `PUT /api/v2/admin/users/:address/approve`

Approves a user account, setting its status to `APPROVED`. The user can then sign in.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the account to approve |

**Request body:** none

**Response `200`:**

```/dev/null/admin-approve-response.json#L1-7
{
  "ok": true,
  "description": "User status updated to APPROVED",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.3 `PUT /api/v2/admin/users/:address/reject`

Rejects a user account, setting its status to `REJECTED`. The user will be blocked from signing in.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the account to reject |

**Request body:** none

**Response `200`:**

```/dev/null/admin-reject-response.json#L1-7
{
  "ok": true,
  "description": "User status updated to REJECTED",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.4 `PUT /api/v2/admin/users/:address/limits`

Updates the deployment quotas for a specific user.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the target user |

**Request body:**

```/dev/null/admin-limits-request.json#L1-4
{
  "maxPools":  20,
  "maxTokens": 50
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `maxPools` | `int` | Yes | New maximum pool deployment count |
| `maxTokens` | `int` | Yes | New maximum token deployment count |

**Response `200`:**

```/dev/null/admin-limits-response.json#L1-7
{
  "ok": true,
  "description": "User limits updated",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.5 `GET /api/v2/admin/tokens`

Lists all confirmed token contracts deployed across all users.

**Response `200`:**

```/dev/null/admin-tokens-response.json#L1-7
{
  "ok": true,
  "description": "All deployed tokens",
  "result": {
    "tokens": [ { "address": "0x...", "symbol": "MTK", "name": "My Token" } ]
  }
}
```

---

### 7.6 `GET /api/v2/admin/pools`

Lists all confirmed SwapPool contracts deployed across all users.

**Response `200`:**

```/dev/null/admin-pools-response.json#L1-7
{
  "ok": true,
  "description": "All deployed pools",
  "result": {
    "pools": [ { "address": "0x...", "symbol": "MYP", "name": "My Pool" } ]
  }
}
```

---

### 7.7 `GET /api/v2/admin/deployments`

Lists all deployment history entries across all users.

**Response `200`:**

```/dev/null/admin-deployments-response.json#L1-7
{
  "ok": true,
  "description": "All deployment history",
  "result": {
    "deployments": [ { "trackingId": "...", "resourceType": "TOKEN", "createdAt": "..." } ]
  }
}
```

---

## 8. SIWE Message Construction

The following is a client-side example using the [`siwe`](https://www.npmjs.com/package/siwe) JavaScript library. This pattern applies to `/auth/signup`, `/auth/signin`, and `/auth/siwe/verify`.

```/dev/null/siwe-example.js#L1-24
import { SiweMessage } from 'siwe';

// Step 1: Fetch a one-time nonce from the server
const nonceRes = await fetch('/auth/nonce', { method: 'POST', credentials: 'include' });
const { result: { nonce } } = await nonceRes.json();

// Step 2: Build the EIP-4361 message
const message = new SiweMessage({
  domain:    window.location.host,
  address:   walletAddress,   // must be checksummed (EIP-55)
  statement: 'Sign in to CLC',
  uri:       window.location.origin,
  version:   '1',
  chainId:   42220,           // Celo Mainnet
  nonce,
}).prepareMessage();

// Step 3: Sign with the connected wallet
const signature = await signer.signMessage(message);

// Step 4: Submit to /auth/signin (or /auth/siwe/verify for unified auth)
await fetch('/auth/signin', {
  method:      'POST',
  headers:     { 'Content-Type': 'application/json' },
  body:        JSON.stringify({ message, signature }),
  credentials: 'include',   // required so the browser stores the __clc_auth cookie
});
```

> **Note on `credentials: 'include'`:** All `fetch` calls to `/auth/*` and `/api/v2/*` must include `credentials: 'include'` so the browser sends and stores the `__clc_auth` HttpOnly cookie cross-origin.

For a full Reown AppKit (formerly WalletConnect) integration example, including the `siweConfig` adapter and sign-up flow, see [`docs/AUTH.md`](./AUTH.md).
