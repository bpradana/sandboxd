package middleware

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// uiAssetPrefix is where the web console's static assets are served; their
// requests are not logged.
const uiAssetPrefix = "/ui/"

var requestLoggerExcludedPaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// redactedQueryParams carry credentials and never reach logs.
var redactedQueryParams = []string{"ticket"}

func NewRequestLogger(logger *logging.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		if _, excluded := requestLoggerExcludedPaths[c.Path()]; excluded || strings.HasPrefix(c.Path(), uiAssetPrefix) {
			return c.Next()
		}

		start := time.Now()
		err := c.Next()

		status := c.Response().StatusCode()
		if err != nil {
			status = appctx.NewErrorResponseBuilder(err).Status()
		}

		fields := []zap.Field{
			zap.String("remote_ip", c.IP()),
			zap.String("method", c.Method()),
			zap.String("uri", redactURI(c.OriginalURL())),
			zap.Int("status", status),
			zap.Duration("latency", time.Since(start)),
			zap.Int("response_size", c.Response().Header.ContentLength()),
		}
		if err != nil {
			fields = append(fields, zap.Error(err))
		} else if handled := appctx.ResponseErrorFromContext(c.Context()); handled != nil {
			// Handlers render most errors themselves and return nil; log the
			// cause they attached so 5xx responses stay debuggable.
			fields = append(fields, zap.Error(handled))
		}

		ctx := c.Context()
		switch {
		case status >= http.StatusInternalServerError:
			logger.Ctx(ctx).Error("http request failed", fields...)
		case status >= http.StatusBadRequest:
			logger.Ctx(ctx).Warn("http request failed", fields...)
		default:
			logger.Ctx(ctx).Info("http request completed", fields...)
		}
		return err
	}
}

// redactURI masks credential query parameters such as terminal tickets.
func redactURI(uri string) string {
	path, rawQuery, found := strings.Cut(uri, "?")
	if !found {
		return uri
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return path + "?REDACTED"
	}
	changed := false
	for _, key := range redactedQueryParams {
		if query.Has(key) {
			query.Set(key, "REDACTED")
			changed = true
		}
	}
	if !changed {
		return uri
	}
	return path + "?" + query.Encode()
}
