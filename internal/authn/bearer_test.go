package authn

import (
	"net/http/httptest"
	"testing"
)

func TestPolicyAuthorized(t *testing.T) {
	t.Parallel()

	token := "secret"
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "missing"},
		{name: "raw token", header: "secret"},
		{name: "wrong scheme", header: "Basic secret"},
		{name: "empty bearer", header: "Bearer"},
		{name: "multi part", header: "Bearer secret extra"},
		{name: "incorrect", header: "Bearer wrong"},
		{name: "standard bearer", header: "Bearer secret", want: true},
		{name: "case insensitive scheme", header: "bearer secret", want: true},
	}

	policy := Policy{Token: func() string { return token }}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest("GET", "/results", nil)
			request.Header.Set("Authorization", testCase.header)
			if got := policy.Authorized(request); got != testCase.want {
				t.Fatalf("Authorized() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestPolicyFailsClosedAndSupportsExplicitLocalMode(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/results", nil)
	if (Policy{}).Authorized(request) {
		t.Fatal("empty production policy authorized a request")
	}
	if !(Policy{AllowUnauthenticated: true}).Authorized(request) {
		t.Fatal("explicit local-development policy rejected a request")
	}
}

func TestPolicyReadsRotatedTokenForEveryRequest(t *testing.T) {
	t.Parallel()

	token := "before"
	policy := Policy{Token: func() string { return token }}
	request := httptest.NewRequest("GET", "/results", nil)
	request.Header.Set("Authorization", "Bearer before")
	if !policy.Authorized(request) {
		t.Fatal("initial token was rejected")
	}
	token = "after"
	request = httptest.NewRequest("GET", "/results", nil)
	request.Header.Set("Authorization", "Bearer after")
	if !policy.Authorized(request) {
		t.Fatal("rotated token was rejected")
	}
}
