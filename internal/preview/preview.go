// Package preview serves capability-scoped application previews on a separate
// HTTP listener. Each session gets its own browser origin, never the API origin.
package preview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
)

var (
	ErrDisabled = errors.New("app previews are disabled; enable PREVIEW_ENABLED and expose the preview listener")
	ErrLimit    = errors.New("preview session limit reached; close a preview or wait for it to expire")
)

type Session struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	ExpiresAt time.Time `json:"expires_at"`
}

type entry struct {
	Session
	sandboxID string
	ctx       context.Context
	cancel    context.CancelFunc
	proxy     *httputil.ReverseProxy
	transport *http.Transport
	activity  func(context.Context) (func(), error)
}

type Manager struct {
	cfg     config.PreviewConfig
	engine  engine.Engine
	mu      sync.Mutex
	entries map[string]*entry
}

func NewManager(lifecycle fx.Lifecycle, cfg config.PreviewConfig, eng engine.Engine) *Manager {
	m := &Manager{cfg: cfg, engine: eng, entries: map[string]*entry{}}
	if cfg.Enabled {
		server := &http.Server{Addr: cfg.Address, Handler: m, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
		var listener net.Listener
		lifecycle.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				var err error
				listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Address)
				if err != nil {
					return fmt.Errorf("listen for app previews: %w", err)
				}
				go func() { _ = server.Serve(listener) }()
				return nil
			},
			OnStop: func(ctx context.Context) error {
				m.mu.Lock()
				for id, e := range m.entries {
					e.cancel()
					e.transport.CloseIdleConnections()
					delete(m.entries, id)
				}
				m.mu.Unlock()
				return server.Shutdown(ctx)
			},
		})
	}
	return m
}

func (m *Manager) Create(sandboxID string, port int, expires time.Time, activity func(context.Context) (func(), error)) (Session, error) {
	if !m.cfg.Enabled {
		return Session{}, ErrDisabled
	}
	if port < 1 || port > 65535 {
		return Session{}, fmt.Errorf("preview port must be between 1 and 65535")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, e := range m.entries {
		if e.ctx.Err() != nil {
			e.cancel()
			e.transport.CloseIdleConnections()
			delete(m.entries, id)
		} else if e.sandboxID == sandboxID {
			count++
		}
	}
	if len(m.entries) >= m.cfg.MaxSessions || count >= 8 {
		return Session{}, ErrLimit
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return Session{}, err
	}
	id := hex.EncodeToString(secret)
	u, err := url.Parse(m.cfg.BaseURL)
	if err != nil {
		return Session{}, err
	}
	u.Host = id + "." + u.Host
	u.Path = "/"
	deadline := time.Now().Add(m.cfg.TTL)
	if expires.Before(deadline) {
		deadline = expires
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	e := &entry{Session: Session{ID: id, URL: u.String(), Port: port, ExpiresAt: deadline}, sandboxID: sandboxID, ctx: ctx, cancel: cancel, activity: activity}
	e.transport = &http.Transport{
		Proxy: nil, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return m.engine.DialSandbox(ctx, sandboxID, port)
		},
	}
	e.proxy = &httputil.ReverseProxy{
		Transport: e.transport, FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			r.Out.Host = r.Out.URL.Host
			if r.In.Header.Get("Origin") == u.Scheme+"://"+u.Host {
				// Development servers commonly enforce same-origin WebSockets.
				// Translate only this preview's origin; preserve foreign origins
				// so the app can still reject cross-origin requests.
				r.Out.Header.Set("Origin", "http://"+r.Out.URL.Host)
			}
			// Never forward console credentials or ambient parent-domain cookies.
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Cookie")
			prefix := "pv_" + id + "_"
			for _, c := range r.In.Cookies() {
				if strings.HasPrefix(c.Name, prefix) {
					c.Name = strings.TrimPrefix(c.Name, prefix)
					r.Out.AddCookie(c)
				}
			}
			r.Out.Header.Set("X-Forwarded-Host", r.In.Host)
			r.Out.Header.Set("X-Forwarded-Proto", u.Scheme)
		},
		ModifyResponse: func(r *http.Response) error {
			if location, err := url.Parse(r.Header.Get("Location")); err == nil && location.Host == r.Request.URL.Host {
				location.Scheme, location.Host = u.Scheme, u.Host
				r.Header.Set("Location", location.String())
			}
			cookies := r.Cookies()
			r.Header.Del("Set-Cookie")
			for _, c := range cookies {
				c.Name = "pv_" + id + "_" + c.Name
				c.Domain = ""
				r.Header.Add("Set-Cookie", c.String())
			}
			r.Header.Set("Referrer-Policy", "no-referrer")
			r.Header.Set("Cache-Control", "no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "App is not reachable. Start an HTTP server on the selected port inside the sandbox.", http.StatusBadGateway)
		},
	}
	m.entries[id] = e
	return e.Session, nil
}

func (m *Manager) Revoke(sandboxID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.sandboxID != sandboxID {
		return false
	}
	e.cancel()
	e.transport.CloseIdleConnections()
	delete(m.entries, id)
	return true
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	base, err := url.Parse(m.cfg.BaseURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	host := strings.ToLower(r.Host)
	suffix := "." + strings.ToLower(base.Host)
	if !strings.HasSuffix(host, suffix) {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(host, suffix)
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok || e.ctx.Err() != nil {
		http.Error(w, "Preview expired or closed. Open a new preview from the console.", http.StatusGone)
		return
	}
	done, err := e.activity(r.Context())
	if err != nil {
		e.cancel()
		http.Error(w, "Sandbox is no longer running.", http.StatusGone)
		return
	}
	defer done()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(e.ctx, cancel)
	defer func() { stop(); cancel() }()
	e.proxy.ServeHTTP(w, r.WithContext(ctx))
}
