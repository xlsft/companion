package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

func TestNewRef(t *testing.T) {
	tests := []struct {
		parts []string
		want  contracts.SecretRef
	}{
		{[]string{"provider", "abc", "api_key"}, "provider/abc/api_key"},
		{[]string{"x"}, "x"},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := NewRef(tt.parts...); got != tt.want {
			t.Errorf("NewRef(%v)=%q want %q", tt.parts, got, tt.want)
		}
	}
}

func roundTrip(t *testing.T, s contracts.SecretStore) {
	ctx := context.Background()
	ref := NewRef("provider", "test", "api_key")
	_ = s.Delete(ctx, ref)

	if _, err := s.Get(ctx, ref); !errors.Is(err, contracts.ErrSecretNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if err := s.Delete(ctx, ref); !errors.Is(err, contracts.ErrSecretNotFound) {
		t.Fatalf("Delete missing: %v", err)
	}
	val := []byte("s3cret-value")
	if err := s.Put(ctx, ref, val); err != nil {
		t.Fatal(err)
	}
	val[0] = 'X' // store must have copied
	got, err := s.Get(ctx, ref)
	if err != nil || !bytes.Equal(got, []byte("s3cret-value")) {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Put(ctx, ref, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, ref); string(got) != "v2" {
		t.Fatalf("overwrite = %q", got)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, ref); !errors.Is(err, contracts.ErrSecretNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestMemory(t *testing.T) { roundTrip(t, NewMemory()) }

func TestMemoryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewMemory().Put(ctx, "a", []byte("v")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestWrapNeverLeaksValue(t *testing.T) {
	err := wrap("put", "ref/x", errors.New("boom with s3cret-value"))
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatal("leak")
	}
}

func TestKeyring(t *testing.T) {
	if testing.Short() || os.Getenv("OPENCLICK_KEYRING_TEST") != "1" {
		t.Skip("set OPENCLICK_KEYRING_TEST=1 to run against the real OS keyring")
	}
	roundTrip(t, NewKeyring())
}
