package authn

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Policy controls access to an operational HTTP surface.
type Policy struct {
	Token                func() string
	AllowUnauthenticated bool
}

// Authorized accepts a standard Bearer authorization scheme followed by
// exactly one nonempty credential. An empty production token fails closed.
func (p Policy) Authorized(request *http.Request) bool {
	if p.AllowUnauthenticated {
		return true
	}
	expected := ""
	if p.Token != nil {
		expected = p.Token()
	}
	if expected == "" {
		return false
	}

	fields := strings.Fields(request.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return false
	}
	provided := fields[1]
	return len(provided) == len(expected) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
