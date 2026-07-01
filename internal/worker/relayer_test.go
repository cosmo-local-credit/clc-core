package worker

import (
	"reflect"
	"testing"
)

func TestRelayDelegateArgsUniqueByEphemeralAddress(t *testing.T) {
	opts := RelayDelegateArgs{}.InsertOpts()
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("RelayDelegateArgs must be unique by args")
	}

	field, ok := reflect.TypeFor[RelayDelegateArgs]().FieldByName("EphemeralAddress")
	if !ok {
		t.Fatal("RelayDelegateArgs.EphemeralAddress field missing")
	}
	if got := field.Tag.Get("river"); got != "unique" {
		t.Fatalf("EphemeralAddress river tag = %q, want unique", got)
	}
}
