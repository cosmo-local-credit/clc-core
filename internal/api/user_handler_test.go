package api

import (
	"context"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
)

type (
	recipientSearchStoreStub struct {
		calls []string
	}
)

func (s *recipientSearchStoreStub) SearchUsersByPhonePrefix(
	context.Context,
	pgx.Tx,
	string,
) ([]store.RecipientSearchUser, error) {
	s.calls = append(s.calls, "phone")
	return []store.RecipientSearchUser{{Name: "Phone result"}}, nil
}

func (s *recipientSearchStoreStub) SearchUsersByEmailPrefix(
	context.Context,
	pgx.Tx,
	string,
) ([]store.RecipientSearchUser, error) {
	s.calls = append(s.calls, "email")
	return []store.RecipientSearchUser{{Name: "Email result"}}, nil
}

func (s *recipientSearchStoreStub) SearchUsersByENSPrefix(
	context.Context,
	pgx.Tx,
	string,
) ([]store.RecipientSearchUser, error) {
	s.calls = append(s.calls, "ens")
	return []store.RecipientSearchUser{{Name: "ENS result"}}, nil
}

func TestSearchRecipientUsersDispatchesExactlyOneQuery(t *testing.T) {
	for _, kind := range []string{"phone", "email", "ens"} {
		t.Run(kind, func(t *testing.T) {
			searchStore := &recipientSearchStoreStub{}

			users, err := searchRecipientUsers(context.Background(), nil, searchStore, kind, "prefix")
			if err != nil {
				t.Fatalf("searchRecipientUsers() error = %v", err)
			}
			if len(users) != 1 {
				t.Fatalf("searchRecipientUsers() returned %d users, want 1", len(users))
			}
			if len(searchStore.calls) != 1 || searchStore.calls[0] != kind {
				t.Fatalf("search calls = %v, want only %q", searchStore.calls, kind)
			}
		})
	}
}

func TestNormalizeRecipientSearchQuery(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		input     string
		want      string
		wantError bool
	}{
		{name: "national phone without country is rejected", kind: "phone", input: "0711", wantError: true},
		{name: "international phone prefix", kind: "phone", input: "+255 711", want: "+255711"},
		{name: "country code without plus", kind: "phone", input: "254711", want: "+254711"},
		{name: "non-Kenyan country code without plus", kind: "phone", input: "381 64", want: "+38164"},
		{name: "international access prefix", kind: "phone", input: "00 49 151", want: "+49151"},
		{name: "email prefix", kind: "email", input: " Alice@Exa ", want: "alice@exa"},
		{name: "email can end at separator", kind: "email", input: "alice@", want: "alice@"},
		{name: "ENS prefix", kind: "ens", input: " Alice.Sarafu ", want: "alice.sarafu"},
		{name: "ENS trailing dot", kind: "ens", input: "alice.", want: "alice."},
		{name: "short ENS", kind: "ens", input: "al", wantError: true},
		{name: "short phone", kind: "phone", input: "07", wantError: true},
		{name: "invalid phone characters", kind: "phone", input: "+254abc", wantError: true},
		{name: "email without at sign", kind: "email", input: "alice", wantError: true},
		{name: "unsupported kind", kind: "name", input: "alice", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeRecipientSearchQuery(tc.kind, tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("normalizeRecipientSearchQuery() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRecipientSearchQuery() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeRecipientSearchQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}
