package util

import (
	"os"
	"strings"
	"testing"

	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

func loadEnvOnly(t *testing.T) *koanf.Koanf {
	t.Helper()
	k := koanf.New(".")
	err := k.Load(env.ProviderWithValue("CORE_", ".", func(key string, value string) (string, any) {
		mappedKey := strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(key, "CORE_")), "__", ".")
		value = strings.ReplaceAll(value, "\\n", "\n")
		if strings.Contains(value, "\n") {
			return mappedKey, value
		}
		if strings.Contains(value, ",") {
			return mappedKey, strings.Split(value, ",")
		}
		if strings.Contains(value, " ") {
			return mappedKey, strings.Split(value, " ")
		}
		return mappedKey, value
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestStringSliceFromEnv(t *testing.T) {
	t.Setenv("CORE_JETSTREAM__ENDPOINTS", "nats://nats:4222")
	k := loadEnvOnly(t)

	if got := k.Strings("jetstream.endpoints"); len(got) != 0 {
		t.Fatalf("expected koanf.Strings to return empty for a scalar, got %v", got)
	}
	got := StringSlice(k, "jetstream.endpoints")
	if len(got) != 1 || got[0] != "nats://nats:4222" {
		t.Fatalf("StringSlice single: got %v", got)
	}
}

func TestStringSliceFromEnvMultiple(t *testing.T) {
	t.Setenv("CORE_JETSTREAM__ENDPOINTS", "nats://a:4222,nats://b:4222")
	k := loadEnvOnly(t)
	if got := StringSlice(k, "jetstream.endpoints"); len(got) != 2 {
		t.Fatalf("StringSlice multi: got %v", got)
	}
}

func TestStringSliceEmpty(t *testing.T) {
	os.Unsetenv("CORE_JETSTREAM__ENDPOINTS")
	k := loadEnvOnly(t)
	if got := StringSlice(k, "jetstream.endpoints"); len(got) != 0 {
		t.Fatalf("StringSlice absent: got %v", got)
	}
}
