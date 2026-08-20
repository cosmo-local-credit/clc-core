package api

import (
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-playground/validator/v10"
)

// Every image URL the API accepts is refused by an HTTP-only database constraint
// if it is not http(s), and for a deploy that constraint is not reached until the
// worker has already signed and broadcast the transaction — MaxAttempts is 1, so
// the job does not retry and the nonce is spent. The `url` tag is not enough: it
// accepts ftp:, data: and javascript:. These assert the edge refuses them first.
func TestImageURLFieldsAcceptOnlyHTTPSchemes(t *testing.T) {
	v := &Validator{ValidatorProvider: validator.New(validator.WithRequiredStructEnabled())}

	good := "https://content.example/voucher/abc_400_a3f91c2e.webp"
	// eth_addr_checksum wants EIP-55, so let go-ethereum produce it.
	owner := common.HexToAddress("0x11a573149db22e759fb9a38bcc461c12855c2600").Hex()
	bad := []string{
		"data:image/jpeg;base64,/9j/4AAQSkZJRg",
		"ftp://content.example/a.webp",
		"javascript:alert(1)",
		"https://",
	}

	t.Run("voucher deploy", func(t *testing.T) {
		base := func() apiresp.DeployTokenRequest {
			return apiresp.DeployTokenRequest{
				Name: "Voucher", Symbol: "VCR", Decimals: "6",
				Owner: owner,
			}
		}
		req := base()
		req.IconURL, req.BannerURL = good, good
		if err := v.Validate(req); err != nil {
			t.Fatalf("http(s) icon and banner rejected: %v", err)
		}
		// Absent is still fine: images are optional.
		if err := v.Validate(base()); err != nil {
			t.Fatalf("omitted images rejected: %v", err)
		}
		for _, url := range bad {
			req = base()
			req.IconURL = url
			if err := v.Validate(req); err == nil {
				t.Errorf("iconUrl %q accepted", url)
			}
			req = base()
			req.BannerURL = url
			if err := v.Validate(req); err == nil {
				t.Errorf("bannerUrl %q accepted", url)
			}
		}
	})

	t.Run("pool deploy", func(t *testing.T) {
		base := func() apiresp.DeployPoolRequest {
			return apiresp.DeployPoolRequest{
				Name: "Pool", Symbol: "POL", Decimals: "6",
				Owner:            owner,
				FeePolicyDefault: "0",
			}
		}
		req := base()
		req.ImageURL, req.BannerURL = good, good
		if err := v.Validate(req); err != nil {
			t.Fatalf("http(s) image and banner rejected: %v", err)
		}
		for _, url := range bad {
			req = base()
			req.ImageURL = url
			if err := v.Validate(req); err == nil {
				t.Errorf("imageUrl %q accepted", url)
			}
		}
	})

	// The two update payloads clear an image differently, and deliberately so.
	//
	// The profile handler preserves anything omitted, so removing an image is an
	// explicit clearAvatar flag — the pattern clearLocation already set in the same
	// handler. The pool handler is assign-all, where omitting a field already means
	// clear, so a flag would be the odd one out and "" carries it instead. That
	// makes the pool tags an `http_url|eq=` alternation, because omitempty does not
	// skip a pointer to an empty string.
	t.Run("update payloads", func(t *testing.T) {
		empty := ""
		if err := v.Validate(apiresp.UpdateProfileRequest{Name: "Owner", AvatarURL: &good}); err != nil {
			t.Fatalf("http(s) avatarUrl rejected: %v", err)
		}
		if err := v.Validate(apiresp.UpdateProfileRequest{Name: "Owner", ClearAvatar: true}); err != nil {
			t.Fatalf("clearAvatar rejected: %v", err)
		}
		// "" is not a way to clear an avatar; the flag is.
		if err := v.Validate(apiresp.UpdateProfileRequest{Name: "Owner", AvatarURL: &empty}); err == nil {
			t.Error("an empty avatarUrl was accepted")
		}
		if err := v.Validate(apiresp.UpdatePoolProfileRequest{ImageURL: &empty, BannerURL: &empty}); err != nil {
			t.Fatalf("empty pool images rejected: %v", err)
		}
		for _, url := range bad {
			bad := url
			if err := v.Validate(apiresp.UpdateProfileRequest{Name: "Owner", AvatarURL: &bad}); err == nil {
				t.Errorf("avatarUrl %q accepted", url)
			}
			if err := v.Validate(apiresp.UpdatePoolProfileRequest{ImageURL: &bad}); err == nil {
				t.Errorf("pool imageUrl %q accepted", url)
			}
		}
	})
}
