// Command gen-user-token mints a user session JWT for local development.
//
// The API already accepts a user JWT in the Authorization header
// (flexibleAuthMiddleware step 2b), so a token minted here lets a local client —
// the admin console in particular — call authenticated endpoints without going
// through SIWE, which needs the user's wallet key.
//
// This is a development convenience only. It signs with api.private_key, so it
// is exactly as trusted as that key; never run it against a production key or
// hand the output to anyone.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/api"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/golang-jwt/jwt/v5"
	"github.com/knadh/koanf/v2"
)

const (
	defaultExpiry = 30 * 24 * time.Hour
)

var (
	confFlag    string
	userIDFlag  uint64
	addressFlag string
	roleFlag    string
	expiryFlag  time.Duration

	lo *slog.Logger
	ko *koanf.Koanf
)

func init() {
	flag.StringVar(&confFlag, "config", "config.toml", "Config file location")
	flag.Uint64Var(&userIDFlag, "user-id", 1, "User ID the token represents")
	flag.StringVar(&addressFlag, "address", "", "Blockchain address the token represents")
	flag.StringVar(&roleFlag, "role", "SUPERADMIN", "Role claim: USER, ADMIN, or SUPERADMIN")
	flag.DurationVar(&expiryFlag, "expiry", defaultExpiry, "Token lifetime")
	flag.Parse()

	lo = util.InitLogger()
	ko = util.InitConfig(lo, confFlag)
}

func main() {
	if addressFlag == "" {
		lo.Error("address is required; pass the user's blockchain_address")
		os.Exit(1)
	}
	if roleFlag != "USER" && roleFlag != "ADMIN" && roleFlag != "SUPERADMIN" {
		lo.Error("role must be USER, ADMIN, or SUPERADMIN", "role", roleFlag)
		os.Exit(1)
	}

	claims := api.UserJWTClaims{
		UserID:          userIDFlag,
		EthereumAddress: addressFlag,
		Role:            roleFlag,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiryFlag)),
		},
	}

	priv, pub, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load private key", "error", err)
		os.Exit(1)
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	if err != nil {
		lo.Error("could not sign token", "error", err)
		os.Exit(1)
	}

	// Verify with the public key before printing, so a bad key fails here rather
	// than as an opaque 401 in the caller.
	verified, err := jwt.ParseWithClaims(signed, &api.UserJWTClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pub, nil
	})
	if err != nil || !verified.Valid {
		lo.Error("could not verify minted token", "error", err)
		os.Exit(1)
	}

	lo.Info("user token generated", "userId", userIDFlag, "role", roleFlag, "expiry", expiryFlag)
	fmt.Println(signed)
}
