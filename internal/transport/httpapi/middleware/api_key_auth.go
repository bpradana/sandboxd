package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// NewAPIKeyAuthMiddleware accepts "Authorization: Bearer <key>" for any key in
// APP_API_KEYS. Keys are compared as SHA-256 digests in constant time so
// neither key length nor content leaks through timing. With no keys
// configured (development only; production config validation rejects it)
// every request is allowed and a warning is logged at startup.
func NewAPIKeyAuthMiddleware(app config.AppConfig, logger *logging.Logger) fiber.Handler {
	keys := newAPIKeys(app.APIKeys)
	if keys.empty() {
		logger.Warn("APP_API_KEYS is empty: the sandbox API is unauthenticated")
	}

	return func(c fiber.Ctx) error {
		// CORS only prevents reading responses. Reject browser requests from
		// other origins too, so untrusted preview apps cannot submit blind
		// mutations against an unauthenticated development API.
		if origin := c.Get(fiber.HeaderOrigin); origin != "" && !originAllowed(origin, c.Host(), app.CORSOrigins) {
			return appctx.ErrOriginNotAllowed()
		}
		if keys.empty() {
			return c.Next()
		}
		token, ok := bearerToken(c.Get(fiber.HeaderAuthorization))
		if !ok {
			c.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="sandboxd"`)
			return appctx.ErrUnauthorized()
		}
		if keys.match(token) {
			return c.Next()
		}

		logger.Ctx(c.Context()).Warn("api key auth failed", zap.String("remote_ip", c.IP()))
		c.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="sandboxd"`)
		return appctx.ErrUnauthorized()
	}
}

// apiKeys holds the SHA-256 digests of the accepted API keys.
type apiKeys [][sha256.Size]byte

func newAPIKeys(keys []string) apiKeys {
	digests := make(apiKeys, 0, len(keys))
	for _, key := range keys {
		digests = append(digests, sha256.Sum256([]byte(key)))
	}
	return digests
}

func (k apiKeys) empty() bool { return len(k) == 0 }

// match compares in constant time against every key.
func (k apiKeys) match(token string) bool {
	presented := sha256.Sum256([]byte(token))
	matched := 0
	for i := range k {
		matched |= subtle.ConstantTimeCompare(presented[:], k[i][:])
	}
	return matched == 1
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
