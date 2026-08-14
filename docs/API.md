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
   - [GET /api/v2/pools](#58-get-apiv2pools)
   - [GET /api/v2/pools/:address](#59-get-apiv2poolsaddress)
6. [User-Only Endpoints (`/api/v2/users/*`)](#6-user-only-endpoints-apiv2users)
   - [GET /api/v2/users/me](#61-get-apiv2usersme)
   - [GET /api/v2/users/me/tokens](#62-get-apiv2usersmetokens)
   - [GET /api/v2/users/me/pools](#63-get-apiv2usersmepools)
   - [GET /api/v2/users/me/pools/:address](#64-get-apiv2usersmepoolsaddress)
   - [GET /api/v2/users/me/deployments](#65-get-apiv2usersmedeployments)
7. [Superadmin Endpoints (`/api/v2/admin/*`)](#7-superadmin-endpoints-apiv2admin)
   - [GET /api/v2/admin/users](#71-get-apiv2adminusers)
   - [PUT /api/v2/admin/users/:address/approve](#72-put-apiv2adminusersaddressapprove)
   - [PUT /api/v2/admin/users/:address/reject](#73-put-apiv2adminusersaddressreject)
   - [PUT /api/v2/admin/users/:address/limits](#74-put-apiv2adminusersaddresslimits)
   - [PUT /api/v2/admin/users/:address/profile](#75-put-apiv2adminusersaddressprofile)
   - [GET /api/v2/admin/users/:address/pools](#76-get-apiv2adminusersaddresspools)
   - [GET /api/v2/admin/users/:address/stats](#77-get-apiv2adminusersaddressstats)
   - [GET /api/v2/admin/tokens](#78-get-apiv2admintokens)
   - [GET /api/v2/admin/pools](#79-get-apiv2adminpools)
   - [GET /api/v2/admin/deployments](#710-get-apiv2admindeployments)
   - [GET /api/v2/admin/dashboard/stats](#711-get-apiv2admindashboardstats)
8. [SIWE Message Construction](#8-siwe-message-construction)
9. [Public Market Catalog (`GET /api/v2/vouchers`)](#9-public-market-catalog-get-apiv2vouchers)
   - [9.6 Voucher detail (`GET /api/v2/vouchers/:address`)](#96-voucher-detail-get-apiv2vouchersaddress)
   - [9.7 Voucher offerings (`GET /api/v2/vouchers/:address/offerings`)](#97-voucher-offerings-get-apiv2vouchersaddressofferings)
10. [Pool offerings (`GET /api/v2/pools/:address/offerings`)](#10-pool-offerings-get-apiv2poolsaddressofferings)
11. [Reports (`GET /api/v2/reports`)](#11-reports-get-apiv2reports)

---

## 1. Overview

| Property | Value |
|---|---|
| Base URL | `http(s)://<host>/` |
| Protected API prefix | `/api/v2` |
| Public `/api/v2` routes | [`GET /api/v2/vouchers`](#9-public-market-catalog-get-apiv2vouchers), [`GET /api/v2/vouchers/:address`](#96-voucher-detail-get-apiv2vouchersaddress), [`GET /api/v2/vouchers/:address/offerings`](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |
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

Service tokens are intended for backend-to-backend flows such as contract
upgrade/version registration. `/api/v2/pretium/*` also accepts service tokens
for service callers, but browser clients may use normal user authentication.

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

> The three public voucher catalog routes are the only exceptions to the
> `/api/v2` prefix being protected. They are not part of this section — see
> [section 9](#9-public-market-catalog-get-apiv2vouchers). Every statement
> below applies unchanged to all other `/api/v2` routes.

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
| `imageUrl` | `string` | No | Optional pool image URL |
| `bannerUrl` | `string` | No | Optional pool banner image URL |

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

These routes proxy the configured Pretium Ramp service for cash in/out flows.

**Auth:** user JWT cookie or service JWT

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
| `POST` | `/api/v2/pretium/link` | Link a wallet address to a phone number (re-links on phone change) |
| `GET` | `/api/v2/pretium/link/:phoneNumber` | Fetch wallet links for a phone number |
| `GET` | `/api/v2/pretium/link-by-address/:address` | Fetch the active link (phone) for a wallet address |
| `GET` | `/api/v2/pretium/transactions/:phoneNumber` | Fetch transactions for a phone number |
| `GET` | `/api/v2/pretium/transactions-by-address/:address` | Fetch transactions for an address |
| `GET` | `/api/v2/pretium/transactions-recent` | Fetch recent transactions |
| `GET` | `/api/v2/pretium/rates` | Fetch current Pretium rates |

**Behaviour:**
- Success responses are passed through from the upstream Pretium service.
- Upstream `5xx` responses are surfaced as `502 Bad Gateway`.
- Local validation failures still use this API's standard error envelope.

See [PRETIUM.md](./PRETIUM.md) for request bodies and integration notes.

---

### 5.8 `GET /api/v2/pools`

Returns **all confirmed** (on-chain) SwapPool contracts across all users — the global pools catalog for the app. Cursor pagination matches activity and `/users/me/pools`.

**Auth:** user JWT cookie or service JWT

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward older records from `cursor` (load more) |
| `cursor` | `int` | — | Use `last` with `next=true` for the next older page, or `first` without `next` to move back toward newer records |
| `q` | `string` | — | Pool name/symbol search; normalized length must be `2`–`64` characters |
| `tags` | `string` or repeated | — | Comma-separated pool tags; at most `12` distinct values |
| `tagMatch` | `any` \| `all` | `any` | Whether a pool must carry any or all selected tags |
| `vouchers` | `string` or repeated | — | Comma-separated stable voucher references (`chainId:0xAddress`); at most `12` |
| `voucherMatch` | `any` \| `all` | `any` | Whether a pool must contain any or all selected vouchers |
| `author` | `string` | — | Only reports authored by this address. Compared case-insensitively; a malformed address is a `400` |
| `facets` | `bool` | `false` | With `true` on the first page, include tag and voucher facet counts |

Voucher membership uses allowed `pool_tokens` rows, matching the pool voucher
list and `voucherCount`; a historical disallowed row does not satisfy the
filter. Voucher references are resolved by `(chainId, address)` rather than by
environment-local numeric IDs. An invalid or unknown reference rejects the
whole filter with `400` instead of silently broadening it.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Confirmed pools",
  "result": {
    "pools": [
      {
        "id": 51,
        "address": "0xAbCd5678...",
        "name": "My Pool",
        "symbol": "MYP",
        "description": "...",
        "tags": ["Market"],
        "imageUrl": null,
        "tokenRegistryAddress": "0x...",
        "feePolicyAddress": "0x...",
        "quoterAddress": "0x...",
        "feesDecoupled": false,
        "locationName": null,
        "createdAt": "2024-01-15T10:30:00Z",
        "updatedAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 51,
    "last": 44
  }
}
```

| Field | Type | Description |
|---|---|---|
| `pools` | `array` | Current page of confirmed pools (`[]` when empty) |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page — use as `cursor` with `next=true` |

When `facets=true` is requested on the first page, `result.tagFacets` contains
`{tag, count}` rows and `result.voucherFacets` contains
`{id, chainId, address, name, symbol, count}` rows. Each facet retains the
search and the other facet dimension while omitting its own selected reducer,
so the counts describe options the user can add. Facet keys are absent from
cursor pages.

**Note:** Prefer this endpoint for the Pools tab browse list. `GET /api/v2/users/me/pools` remains the authenticated user's own deployed pools only.

---

### 5.9 `GET /api/v2/pools/:address`

Returns a single confirmed SwapPool by contract address. Address match is case-insensitive. Any authenticated client may load any confirmed pool.

**Auth:** user JWT cookie or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Pool contract address |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Pool at address 0xAbCd5678...",
  "result": {
    "pool": {
      "id": 51,
      "address": "0xAbCd5678...",
      "name": "My Pool",
      "symbol": "MYP",
      "description": "...",
      "tags": ["Market"],
      "imageUrl": null,
      "tokenRegistryAddress": "0x...",
      "feePolicyAddress": "0x...",
      "quoterAddress": "0x...",
      "feesDecoupled": false,
      "locationName": null,
      "createdAt": "2024-01-15T10:30:00Z",
      "updatedAt": "2024-01-15T10:30:00Z"
    }
  }
}
```

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
| `next` | `bool` | `false` | When `true`, page toward older records from `cursor` (load more) |
| `cursor` | `int` | — | Use `last` with `next=true` for the next older page, or `first` without `next` to move back toward newer records |

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
| `pools` | `array` | Current page of deployed pools (`[]` when empty) |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page — use as `cursor` with `next=true` |

---

### 6.4 `GET /api/v2/users/me/pools/:address`

Returns a single confirmed SwapPool by contract address. Address match is case-insensitive.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Pool contract address |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Pool at address 0xAbCd5678...",
  "result": {
    "pool": {
      "id": 51,
      "address": "0xAbCd5678...",
      "name": "My Pool",
      "symbol": "MYP",
      "description": "...",
      "tags": ["Market"],
      "imageUrl": null,
      "tokenRegistryAddress": "0x...",
      "feePolicyAddress": "0x...",
      "quoterAddress": "0x...",
      "feesDecoupled": false,
      "locationName": null,
      "createdAt": "2024-01-15T10:30:00Z",
      "updatedAt": "2024-01-15T10:30:00Z"
    }
  }
}
```

---

### 6.5 `GET /api/v2/users/me/deployments`

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

Returns one page of the account directory, newest first, using cursor pagination that matches the catalog (`perPage`, `cursor`, `next`). Optionally narrowed by approval status and by a search term.

> **Breaking change.** This endpoint previously ignored pagination and answered with every row. `users` is now at most `perPage` accounts and the response carries `first` / `last`. A client that pages or filters the directory locally must move both jobs to the server, or it will silently present a truncated directory as the whole one.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `q` | `string` | — | Search over name, email, phone and address. 2–64 characters; a shorter or longer term is rejected. Country and role are not searched |
| `status` | `string` | — | Filter by status: `PENDING`, `APPROVED`, or `REJECTED` (case-insensitive) |
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward newer records from `cursor` |
| `cursor` | `int` | — | Use `last` to fetch older records or `first` with `next=true` to move back toward newer records |

`q` matches as a prefix at two characters and as a substring above that, and the same rules and error messages apply as on the public catalog search.

**Response `200`:**

```/dev/null/admin-users-response.json#L1-20
{
  "ok": true,
  "description": "Users list",
  "result": {
    "users": [
      {
        "id":              4210,
        "ethereumAddress": "0xd8dA6BF2...",
        "name":            "Ada Lovelace",
        "email":           "ada@example.com",
        "phone":           "+254700000000",
        "country":         "KE",
        "role":            "USER",
        "status":          "PENDING"
      }
    ],
    "first": 4210,
    "last": 4187
  }
}
```

| Field | Type | Description |
|---|---|---|
| `users` | `array` | Current page of accounts |
| `first` | `int` | Highest account ID in the current page |
| `last` | `int` | Lowest account ID in the current page |

**Errors:**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | `q` is shorter than 2 or longer than 64 characters, or `status` is not one of the three approval states |

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

### 7.5 `PUT /api/v2/admin/users/:address/profile`

Corrects another account's identity and contact details. `PUT /auth/profile` edits only the signed-in account, so this is the operator equivalent; it takes the same body and applies the same validation.

It cannot change status, role or limits — those have their own endpoints.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the target user |

**Request body:**

```/dev/null/admin-profile-request.json#L1-8
{
  "name":         "Ada Lovelace",
  "email":        "ada@example.com",
  "phone":        "+254700000000",
  "country":      "KE",
  "locationName": "Nairobi",
  "geoLat":       -1.2921,
  "geoLng":       36.8219
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | No | Max 80 characters |
| `email` | `string` | No | Must be a valid address, unique across accounts |
| `phone` | `string` | No | Unique across accounts |
| `country` | `string` | No | ISO 3166-1 alpha-2 code |
| `locationName` | `string` | No | Max 200 characters |
| `geoLat` | `number` | No | −90 to 90. Must be sent with `geoLng` |
| `geoLng` | `number` | No | −180 to 180. Must be sent with `geoLat` |

Omitted location fields preserve the stored values; they are not cleared.

**Response `200`:**

```/dev/null/admin-profile-response.json#L1-7
{
  "ok": true,
  "description": "User profile updated",
  "result": {
    "user": { "..." : "..." }
  }
}
```

**Errors:**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | Validation failed, including `geoLat` sent without `geoLng` or vice versa |
| `404` | `E03` | No account at that address |
| `409` | `E17` / `E18` | `email` or `phone` already belongs to another account |

---

### 7.6 `GET /api/v2/admin/users/:address/pools`

The pools an account belongs to, cursor paginated.

Membership is **derived**, not stored: nothing records a user joining a pool, but the catalog records which vouchers a pool allows, so an account belongs to the pools that allow a voucher it owns. Pool ownership and deployment history are deliberately not part of this.

**Query parameters:** `perPage`, `cursor`, `next`, as elsewhere.

**Response `200`:** `{ "pools": [ … ], "first": 42, "last": 17 }` — pool rows in the same shape as [`GET /api/v2/pools`](#10-pool-offerings-get-apiv2poolsaddressofferings).

---

### 7.7 `GET /api/v2/admin/users/:address/stats`

The figures behind one account.

**Response `200`:**

```/dev/null/admin-account-stats.json#L1-12
{
  "ok": true,
  "description": "Account statistics",
  "result": {
    "reports":         3,
    "vouchers":        2,
    "poolMemberships": 1,
    "transactions":    118,
    "swaps":           4,
    "partners":        9
  }
}
```

| Field | Source | Description |
|---|---|---|
| `reports` | catalog | Approved reports authored by the account |
| `vouchers` | catalog | Vouchers the account owns that the public catalog can return (active, geolocated) |
| `poolMemberships` | catalog | Distinct pools allowing a voucher the account owns |
| `transactions` | federation | Successful transfers sent or received |
| `swaps` | federation | Successful swaps the account initiated |
| `partners` | federation | Distinct counterparties across successful transfers, excluding the account itself |

The three catalog figures use the same visibility rules as the lists they sit beside, so a count and its list never disagree.

**The three federation figures are `null` when the federation database is not configured or a read fails.** Null rather than zero, deliberately: a caller must be able to tell "none" from "no answer", and a zero reads as the first. The catalog half is still returned in that case.

---

### 7.8 `GET /api/v2/admin/tokens`

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

### 7.9 `GET /api/v2/admin/pools`

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

### 7.10 `GET /api/v2/admin/deployments`

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

### 7.11 `GET /api/v2/admin/dashboard/stats`

Dashboard aggregates. Mounted on the `/admin/dashboard` group, not mixed with user/token mutations.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `range` | `string` | `5m`, `15m`, `30m`, `1h`, `6h`, `1d` (default), or `custom` |
| `since` | `string` | RFC3339 start, required when `range=custom` |
| `until` | `string` | RFC3339 end, required when `range=custom` |

`pools`, `vouchers`, and `accounts` are lifetime catalog stock. `reports` and `transactions` are counts inside the requested window. Two series feed the charts: catalog creations, and `chain_data_v2.tx` envelopes. `transactions` is `null` when federation is down.

### 7.12 `GET /api/v2/admin/dashboard/transactions`

Latest indexed envelopes from `chain_data_v2.tx`, classified from live event tables. Cursor pagination matches the catalog (`perPage`, `cursor`, `next`).

| Parameter | Type | Description |
|---|---|---|
| `perPage` | `number` | Page size, default/max 100 |
| `cursor` | `number` | Event or envelope id bound |
| `next` | `boolean` | `true` pages forward (lower ids), `false` pages back |
| `type` | `string` | `transfer`, `swap`, `mint`, `burn`, `deposit`, `faucet` |
| `voucher` | `string` | Token or pool `0x` address (`contract_address`) |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Admin dashboard totals",
  "result": {
    "range": { "preset": "1d", "since": "2026-08-12T00:00:00Z", "until": "2026-08-13T00:00:00Z", "bucket": "1h" },
    "pools": 42,
    "vouchers": 2192,
    "accounts": 310,
    "reports": 12,
    "transactions": 1843,
    "catalogSeries": [
      { "t": "2026-08-12T00:00:00Z", "reports": 1, "vouchers": 2, "pools": 0 }
    ],
    "transactionSeries": [
      { "t": "2026-08-12T00:00:00Z", "transactions": 16 }
    ]
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

> **Note on `credentials: 'include'`:** Calls to `/auth/*` and protected
> `/api/v2/*` routes must include credentials so the browser sends and stores
> the `__clc_auth` HttpOnly cookie cross-origin. The public market routes in
> [section 9](#9-public-market-catalog-get-apiv2vouchers) do not use the cookie
> and may explicitly send `credentials: 'omit'`.

For a full Reown AppKit (formerly WalletConnect) integration example, including the `siweConfig` adapter and sign-up flow, see [`docs/AUTH.md`](./AUTH.md).

---

## 9. Public Market Catalog (`GET /api/v2/vouchers`)

These are the only public routes under `/api/v2`. They back the Market
experience, which is browsable by signed-out visitors. Sending the
`__clc_auth` cookie is harmless but changes nothing: a guest and a signed-in
user receive byte-identical responses.

The list route returns active, geolocated voucher summaries with a
backend-derived marker kind. Per-voucher detail — contact details, images, and
unit of account — lives in [section 9.6](#96-voucher-detail-get-apiv2vouchersaddress);
its available offerings live in
[section 9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

### 9.1 Request

```http
GET /api/v2/vouchers?perPage=500
GET /api/v2/vouchers?perPage=500&cursor=2212&next=true
GET /api/v2/vouchers?perPage=500&bbox=36.6,-1.5,37.1,-1.1
```

| Parameter | Type | Default | Notes |
|---|---|---|---|
| `perPage` | integer | `500` | **Endpoint exception:** default *and* maximum are 500, not the 100 used by every other catalog route. `0` or any value above 500 resolves to 500. |
| `cursor` | integer | – | A voucher `id` from a previous response's `first` / `last`. |
| `next` | boolean | `false` | `true` pages forward (`id < cursor`); `false` with a cursor pages backward (`id > cursor`). |
| `bbox` | string | – | Optional `minLng,minLat,maxLng,maxLat` viewport filter. |
| `owner` | string | – | Only vouchers owned by this address. Compared case-insensitively; a malformed address is a `400`. |

**Pagination** uses descending `id` keyset paging. Requests without a cursor
return the newest IDs first. Responses always carry `first` and `last` as JSON
numbers; an empty page returns `"vouchers": []`, `"first": 0`, `"last": 0` —
never `null` and never omitted.

**`bbox` validation.** All four values must be finite and comma-separated, with
`-180 <= lng <= 180`, `-90 <= lat <= 90`, `minLng <= maxLng`, and
`minLat <= maxLat`. A single box therefore **cannot cross the antimeridian**; a
client whose viewport does must issue two requests (`[-180, east]` and
`[west, 180]`) and merge them by `id`. Malformed, out-of-range, or inverted
values return `400` with code `E04` (validation failed).

`bbox` composes with the cursor, but a caller **must keep one bbox fixed while
traversing its pages** — changing it mid-traversal produces an inconsistent set.

### 9.2 Response

```/dev/null/vouchers.json#L1-20
{
  "ok": true,
  "description": "Market vouchers",
  "result": {
    "vouchers": [
      {
        "id": 2710,
        "chainId": 42220,
        "address": "0x0000000000000000000000000000000000000000",
        "name": "Priscilah Malii",
        "symbol": "VOUCHER",
        "iconUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
        "locationName": "C96, KEN",
        "geoLat": -1.450979,
        "geoLng": 38.235115,
        "kind": "voucher"
      }
    ],
    "first": 2710,
    "last": 2212
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `id` | integer | Database ID. Doubles as the pagination cursor and stable map feature identity. |
| `chainId` | integer | Settlement chain, `42220` for Celo mainnet. |
| `address` | string | Contract address, preserved in its stored EIP-55 checksummed form. Never lowercased by the API. |
| `name` | string \| null | |
| `symbol` | string \| null | |
| `iconUrl` | string \| null | Profile image used by list rows. Clients must provide a fallback. |
| `locationName` | string \| null | Free-text place label from the import. |
| `geoLat` | number | Never null — rows without complete coordinates are excluded. |
| `geoLng` | number | Never null. |
| `kind` | string | `"goods"`, `"services"`, or `"voucher"`. |

**Included rows.** Only vouchers that are `active` **and** have complete
coordinates. There is no `internal` predicate: `internal` is an import
artifact, not a visibility or product-policy flag, and it is not exposed in
this contract. See `MIGRATION.md` §4.7.

**`kind` derivation.** Computed from the voucher's **available** offerings:

| Condition | `kind` |
|---|---|
| Dominant available `commodity_type` is `GOOD` | `goods` |
| Dominant available `commodity_type` is `SERVICE` | `services` |
| Equal `GOOD` / `SERVICE` counts | `goods` |
| No available offerings | `voucher` |

Unavailable offerings are ignored entirely. `commodity_type` is FK-constrained
to `GOOD`/`SERVICE`, so the `voucher` fallback represents "no available
offerings", never an unrecognised type.

**No distance field.** Distance is relative to optional browser geolocation and
is presentation state, not catalog state. It is never calculated server-side.

`iconUrl` is included because the catalog backs the default list view. Larger
profile fields remain on
[voucher detail](#96-voucher-detail-get-apiv2vouchersaddress).

### 9.3 Caching

```http
Cache-Control: public, max-age=60, stale-while-revalidate=300
Vary: Origin
```

Successful `200` representations never vary by authorization or cookie, so
they are publicly cacheable. Validation and server errors do not carry this
cache policy. `Origin` is **merged** into any existing `Vary` rather than
replacing it; the global CORS policy sets `AllowCredentials: true`, which makes
origin variance mandatory even though this client sends no credentials.

### 9.4 Rate limiting

Because the routes have no authentication to fall back on, they carry a
per-client-IP token bucket shared by all three public routes: **60 requests per
minute sustained, burst 40**. Exceeding it returns `429 Too Many Requests` with
a `Retry-After` header in whole seconds, calculated from the next available
token.

The burst is sized from how the Market tab actually opens: five catalog pages,
a bounded viewport bootstrap, then detail and offerings requests for selected
vouchers. Voucher-only rows skip the offerings request, so 40 still leaves
headroom for a run of selections immediately after load.

```/dev/null/rate-limited.json#L1-5
{
  "ok": false,
  "description": "rate limit exceeded, retry later",
  "code": "E11"
}
```

The client key is the **direct peer address**. `X-Forwarded-For` is honoured
only when `api.trust_proxy` is enabled, which must be set only where the
service is reachable exclusively through the Caddy proxy network; otherwise any
caller could forge the header and spend another client's budget. When trusted,
the **rightmost** entry is used, since that is the address the proxy actually
observed.

The limiter's client registry is bounded and idle entries are swept
periodically, so forged source addresses cannot grow it without bound.

> This limit assumes the current single API replica. If the service is scaled
> horizontally, move the limit to a shared edge or distributed limiter rather
> than multiplying the allowance per replica.

### 9.5 Examples

First page:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500'
```

Next page, using the previous response's `last`:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500&cursor=2212&next=true'
```

Bounded to a Nairobi-sized viewport:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500&bbox=36.6,-1.5,37.1,-1.1'
```

### 9.6 Voucher detail (`GET /api/v2/vouchers/:address`)

Public for the same reason as the list: a shared voucher link must open for a
signed-out visitor. It backs both the map's selection card and the voucher page.

```http
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7
```

`:address` must be a valid hex address; anything else returns `400` with code
`E04` before the database is touched. Lookup is **case-insensitive**, so a
lowercased pasted link resolves, but the response echoes the stored EIP-55
checksummed form.

```/dev/null/voucher.json#L1-27
{
  "ok": true,
  "description": "Market voucher",
  "result": {
    "voucher": {
      "id": 2710,
      "chainId": 42220,
      "address": "0x0001423EC38205B125e7340aa37F4D19276e3aB7",
      "name": "Priscilah Malii",
      "symbol": "MALII",
      "decimals": 6,
      "ownerAddress": "0xDeF0000000000000000000000000000000000456",
      "description": "Community voucher",
      "voucherUoa": "KSH",
      "voucherValue": "10",
      "locationName": "C96, KEN",
      "geoLat": -1.450979,
      "geoLng": 38.235115,
      "email": "issuer@example.org",
      "website": "https://example.org",
      "phone": "+254700000000",
      "iconUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
      "bannerUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
      "kind": "goods"
    }
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `id`, `chainId`, `address`, `name`, `symbol`, `locationName`, `kind` | — | Identical semantics to the [list contract](#92-response). |
| `decimals` | integer \| null | ERC-20 decimals. |
| `ownerAddress` | string \| null | Issuer's wallet, stored casing preserved. |
| `description` | string \| null | |
| `voucherUoa` | string \| null | Unit of account, **free text entered by the issuer** — `KSH`, `Ksh`, `KES`, `usd`, and `Hour` all occur. Never normalised server-side; render verbatim. |
| `voucherValue` | string \| null | What one voucher is worth in `voucherUoa`, so `symbol` + `voucherValue` + `voucherUoa` reads "1 MALII = 10 KSH". Serialized as a **string**: the column is `NUMERIC` and a float would lose scale. |
| `geoLat`, `geoLng` | number \| null | **Nullable here, unlike the list.** Coordinates are not required — a voucher without them still has a readable page, it simply renders no map. Absent coordinates are `null`, never `0`. |
| `email`, `website`, `phone` | string \| null | Issuer contact, as imported. |
| `iconUrl` | string \| null | Profile image. Absent for roughly two thirds of the imported catalog, so clients need a real fallback. |
| `bannerUrl` | string \| null | Wide header image. Absent about as often as `iconUrl`. |

**Visibility.** Only `active` vouchers resolve. A deactivated voucher returns
`404` with a body **byte-identical** to an address that never existed, so the
response cannot be used to probe which addresses are in the database. Unlike the
list, coordinates are not required.

Caching and rate limiting are exactly as in [9.3](#93-caching) and
[9.4](#94-rate-limiting); all three routes share one per-IP bucket.

> **Swagger drift.** `docs/swagger.yaml` predates the catalog surface and does
> not describe these routes. Reconciling it is tracked as separate follow-up
> work.

### 9.7 Voucher offerings (`GET /api/v2/vouchers/:address/offerings`)

Returns the available offerings for one active voucher. It is public so a
signed-out visitor can open the default Offerings tab on a shared voucher page.
The address lookup is case-insensitive and rejects malformed hex addresses with
`400` and code `E04`.

```http
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7/offerings?perPage=20
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7/offerings?perPage=20&cursor=42&next=true
```

The route uses the standard descending-`id` keyset pagination contract:
`perPage` defaults to 100 and is capped at 100, `next=true` selects IDs below
the cursor, and a cursor without `next=true` selects IDs above it. Every
response contains numeric `first` and `last` values. An empty result is
`"offerings": []`, `"first": 0`, `"last": 0`, never `null`.

Only rows satisfying both `offering.available = true` and
`voucher.active = true` are returned. This matches the market voucher `kind`
derivation: a catalog row with `kind: "voucher"` has an empty offerings result.
The imported catalog observed on 2026-07-27 contains at most 19 offerings for a
single voucher, so the client page size of 20 normally completes in one request.

Caching and rate limiting are exactly as in [9.3](#93-caching) and
[9.4](#94-rate-limiting).

---

## 10. Pool offerings (`GET /api/v2/pools/:address/offerings`)

Returns every available offering published against a voucher the pool allows.
The pool detail screen lists these in its default Offerings tab, instead of
walking the pool's vouchers one request at a time.

**Authentication required** — this route sits on the authenticated API group
with the other `/api/v2/pools/*` routes, because the pool screen itself requires
a session. It is therefore `no-store` and carries no public cache policy, unlike
the voucher-scoped route in [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

```http
GET /api/v2/pools/0x3d11B609689FefDD887045933AED75e2B014a0cc/offerings?perPage=20
GET /api/v2/pools/0x3d11B609689FefDD887045933AED75e2B014a0cc/offerings?perPage=20&cursor=1354&next=true
```

The address lookup is case-insensitive and rejects malformed hex addresses with
`400` and code `E04`. Pagination is the standard descending-`id` keyset
contract: `perPage` defaults to 100 and is capped at 100, `next=true` selects
IDs below the cursor, and a cursor without `next=true` selects IDs above it.
Every response contains numeric `first` and `last`. An empty result is
`"offerings": []`, `"first": 0`, `"last": 0`, never `null`.

Rows are filtered on three predicates:

| Predicate               | Why                                                              |
| ----------------------- | ---------------------------------------------------------------- |
| `pool_tokens.is_allowed` | Matches `GET /api/v2/pools/:address/vouchers`, so the two tabs agree |
| `offering.available`     | Matches [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |
| `voucher.active`         | Matches [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |

The last two are deliberately identical to the voucher-scoped route: an offering
must read the same whether the user reached it through its voucher or through a
pool that accepts it.

Each row carries `voucherName`, `voucherSymbol`, and `voucherAddress`, because a
pool's offerings come from many vouchers and the UI has to say which voucher each
one belongs to. The response body is otherwise byte-identical in shape to
[9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

---

## 11. Reports (`GET /api/v2/reports`)

The Posts feed. **Authentication required** — Posts is a private home tab, so
these routes sit on the authenticated API group.

Only `APPROVED` reports are returned. `DRAFT`, `SUBMITTED`, and `REJECTED` are
invisible on both routes, and an unapproved id is a `404` rather than a `403`:
whether a draft exists is not public information.

### 11.1 Feed (`GET /api/v2/reports`)

```http
GET /api/v2/reports?perPage=20
GET /api/v2/reports?perPage=20&cursorUpdatedAt=2026-07-19T08:14:02.311Z&cursorId=3182
```

`perPage` defaults to 20 and is capped at 100.

The feed also accepts the catalog filter parameters below:

| Parameter | Type | Default | Description |
|---|---|---|---|
| `q` | `string` | — | Report title/description search; normalized length must be `2`–`64` characters |
| `tags` | `string` or repeated | — | Comma-separated report tags; at most `12` distinct values |
| `tagMatch` | `any` \| `all` | `any` | Whether a report must carry any or all selected tags |
| `vouchers` | `string` or repeated | — | Comma-separated stable voucher references (`chainId:0xAddress`); at most `12` |
| `voucherMatch` | `any` \| `all` | `any` | Whether a report must be linked to any or all selected vouchers |
| `facets` | `bool` | `false` | With `true` on the first page, include tag and voucher facet counts |

Voucher references are case-insensitive addresses scoped by chain ID. Invalid
or unknown references reject the whole request with `400`. `tagFacets` and
`voucherFacets` use the same disjunctive counting contract as the pool catalog
and are absent from cursor pages.

**This route does not use the numeric `cursor`/`next` keyset the catalog routes
use.** The feed is ordered by `updated_at DESC, id DESC`, and on the imported
catalog that order is unrelated to id order — measured on 3,730 approved rows,
the two agree on 4 of them and drift by up to 2,606 positions. An id cursor
would therefore skip and repeat rows. The cursor is the `(updatedAt, id)` pair
instead:

- `cursorUpdatedAt` is RFC3339 with nanoseconds; `cursorId` is a positive
  integer. Sending one without the other is a `400`, because accepting one alone
  would silently return the first page again.
- The response carries `next` — the pair to send for the following page — or
  `null` on the last page. A short page ends the feed, so `next` is `null`
  rather than the client having to guess from emptiness.
- `(updated_at, id)` row comparison is index-backed by
  `reports_status_updated_idx` (migration 009). Measured: index-only scan,
  0 heap fetches, ~0.03 ms for a cursor page.

Each row is a summary: `id`, `title`, `description`, `imageUrl`, `geoLat`,
`geoLng`, `periodFrom`, `periodTo`, `authorAddress`, `createdAt`, `updatedAt`,
`tags`, and `vouchers` (each `{id, chainId, address, name, symbol}`).

`body` is not returned on the feed — a row is a summary and `description`
already carries what it renders. It is available on the detail route.

`locationName` was removed in migration 010: the column was `NULL` on all 4,632
imported rows, because the source stored geography as a `"lat, lng"` string and
never a place name. Use `geoLat` / `geoLng`.

### 11.2 Detail (`GET /api/v2/reports/:id`)

```http
GET /api/v2/reports/3182
```

Returns one report under `result.report` with every feed field plus `body`,
`images`, `attachments`, `hostName`, `participants`, and `timeTaken`.

Since migration 010 **none of this is parsed at request time.** The editor
document used to live in `reports.body` as JSONB and this route re-derived the
image list and the form from it on every call, with two independent
`jsonb_array_elements` passes. That work now happens once, at write time:

| Field                                     | Storage                        |
| ----------------------------------------- | ------------------------------ |
| `body`                                    | `reports.body`, markdown TEXT  |
| `images`                                  | `report_images`, by `ordinal`  |
| `attachments`                             | `report_attachments`           |
| `hostName`, `participants`, `timeTaken`   | columns on `reports`           |

`body` is **markdown**, not the editor's JSON and not plain text: heading
levels, lists, tables, emphasis and links are preserved, and 41 hyperlinks
across 8 reports have their URL only in the document (the visible text is a bare
commit hash), so plain-text flattening would have lost them. Only 310 of 4,632
imported reports have any prose at all — the rest keep their content in
`description` — and the whole corpus is 129 KB, down from 17 MB of JSONB.

`hostName`, `participants`, and `timeTaken` are issuer free text and are never
normalized — `timeTaken` alone contains `2`, `1 hour`, `2 hours`, `2hrs`, and
`1 hr`. Seven imported reports carry two form nodes and on two of them the
second differs, so the stored value is the first form in document order that has
any value at all; the previous unordered `LIMIT 1` picked arbitrarily.

`images` entries are
`{url, width, height, maxWidth, maxHeight, byteSize, caption}`. `width` and
`height` are the editor's display box; `maxWidth` and `maxHeight` are the
intrinsic size, and the two are **not** a scaled pair (a 267×1000 display box
against a 747×1600 intrinsic occurs). Any of them may be `null`. `caption` is
new — it was previously discarded. Reports carry 0 to 37 images; 73% have
exactly one and 381 have none.

`attachments` entries are `{kind, url, filename, byteSize}`, where `kind` is
`FILE` for an uploaded document or video and `EMBED` for an external player URL.
`filename` is set only for uploads. These were previously not served at all.

**Only `http(s)` URLs are stored** in either table — a CHECK constraint enforces
it, so the exclusion is a storage invariant rather than a filter each request
re-applies. Legacy `img` nodes embed base64 `data:` URIs up to ~900 KB and 48
`cloud_image` nodes hold unresolved `#nanoid` upload placeholders; none of them
reach a response.

One gap remains at the column level: six approved reports store a base64
`data:` URI directly in `reports.image_url`, up to 876 KB. Both routes return
`null` for those rather than the URI. They are not nulled in storage because
four of the six have no other image, so the fix is a CDN re-upload, not a
delete.
