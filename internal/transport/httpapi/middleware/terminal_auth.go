package middleware

import (
	"net/url"
	"slices"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// TicketRedeemer consumes single-use terminal tickets.
type TicketRedeemer interface {
	RedeemTerminalTicket(sandboxID, ticket string) bool
}

// NewTerminalAuthMiddleware guards the terminal WebSocket upgrade. Browsers
// cannot set an Authorization header on a WebSocket, so besides a bearer key
// it accepts a single-use ticket (from POST .../terminal/tickets) in the
// "ticket" query parameter. Browsers also attach ambient credentials to
// cross-site WebSockets and CORS does not apply to them, so a browser Origin
// must be this server or one of APP_CORS_ALLOWED_ORIGINS; that check applies
// even when no API keys are configured.
func NewTerminalAuthMiddleware(app config.AppConfig, logger *logging.Logger, tickets TicketRedeemer) fiber.Handler {
	keys := newAPIKeys(app.APIKeys)
	return func(c fiber.Ctx) error {
		if origin := c.Get(fiber.HeaderOrigin); origin != "" && !originAllowed(origin, c.Host(), app.CORSOrigins) {
			logger.Ctx(c.Context()).Warn("terminal origin rejected", zap.String("origin", origin), zap.String("remote_ip", c.IP()))
			return appctx.ErrOriginNotAllowed()
		}
		if token, ok := bearerToken(c.Get(fiber.HeaderAuthorization)); ok && keys.match(token) {
			return c.Next()
		}
		if ticket := c.Query("ticket"); ticket != "" {
			if tickets.RedeemTerminalTicket(c.Params("id"), ticket) {
				return c.Next()
			}
			logger.Ctx(c.Context()).Warn("terminal ticket rejected", zap.String("remote_ip", c.IP()))
			return appctx.ErrInvalidTerminalTicket()
		}
		if keys.empty() {
			return c.Next()
		}
		c.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="sandboxd"`)
		return appctx.ErrUnauthorized()
	}
}

// originAllowed accepts the server's own origin (same host and port) and
// the configured CORS origins.
func originAllowed(origin, host string, allowed []string) bool {
	if slices.Contains(allowed, "*") || slices.Contains(allowed, origin) {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host != "" && parsed.Host == host
}
