package provider

import (
	"context"
	"net/http"
	"testing"
)

type plainProvider struct{}

func (plainProvider) ID() ID                                  { return "plain" }
func (plainProvider) Models(context.Context) ([]Model, error) { return nil, nil }
func (plainProvider) Build(Request) (Prepared, error)         { return Prepared{}, nil }
func (plainProvider) Classify(*http.Response, []byte) Failure { return Failure{} }

type publicProvider struct{ plainProvider }

func (publicProvider) Credential(string) string { return "anon-marker" }

type keyHeaderProvider struct{ plainProvider }

func (keyHeaderProvider) CredentialHeader() (string, string) { return "x-api-key", "" }

func TestApplyCredentialUsesTheDefaultShape(t *testing.T) {
	prepared := Prepared{Headers: http.Header{}}
	ApplyCredential(plainProvider{}, prepared, "k")
	if got := prepared.Headers.Get("authorization"); got != "Bearer k" {
		t.Fatalf("authorization %q", got)
	}
}

func TestApplyCredentialSendsTheAnonymityMarkerWithoutAKey(t *testing.T) {
	prepared := Prepared{Headers: http.Header{}}
	ApplyCredential(publicProvider{}, prepared, "")
	if got := prepared.Headers.Get("authorization"); got != "Bearer anon-marker" {
		t.Fatalf("authorization %q", got)
	}
}

func TestApplyCredentialUsesTheProviderHeader(t *testing.T) {
	prepared := Prepared{Headers: http.Header{}}
	ApplyCredential(keyHeaderProvider{}, prepared, "k")
	if got := prepared.Headers.Get("x-api-key"); got != "k" {
		t.Fatalf("x-api-key %q", got)
	}
}
