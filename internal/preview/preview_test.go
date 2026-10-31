package preview

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
)

func previewFixture(t *testing.T, handler http.Handler) (*Manager, Session, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(handler)
	t.Cleanup(backend.Close)
	eng := engine.NewMockEngine(gomock.NewController(t))
	eng.EXPECT().DialSandbox(gomock.Any(), "sandbox", 3000).DoAndReturn(func(ctx context.Context, _ string, _ int) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(backend.URL, "http://"))
	}).AnyTimes()
	m := &Manager{cfg: config.PreviewConfig{Enabled: true, BaseURL: "http://localhost:8081", TTL: time.Minute, MaxSessions: 2}, engine: eng, entries: map[string]*entry{}}
	session, err := m.Create("sandbox", 3000, time.Now().Add(time.Hour), func(context.Context) (func(), error) { return func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Revoke("sandbox", session.ID) })
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	return m, session, server
}

func TestProxyScopesOriginAndStripsConsoleCredentials(t *testing.T) {
	t.Parallel()
	_, session, server := previewFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || strings.Contains(r.Header.Get("Cookie"), "console") {
			t.Error("console credentials forwarded")
		}
		if r.URL.RequestURI() != "/assets/app.js?q=1" {
			t.Errorf("path lost: %s", r.URL)
		}
		if r.Host != "127.0.0.1:3000" {
			t.Errorf("unexpected upstream host: %s", r.Host)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "app-value", Domain: "localhost", Path: "/", HttpOnly: true})
		_, _ = io.WriteString(w, "app content")
	}))
	u, _ := url.Parse(session.URL)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/assets/app.js?q=1", nil)
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer console-credential")
	req.Header.Set("Cookie", "console=private")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "app content" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unexpected proxy response %d %s", res.StatusCode, body)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "pv_"+session.ID+"_session" || cookies[0].Domain != "" {
		t.Fatalf("unsafe cookies: %+v", cookies)
	}
	unknown, _ := server.Client().Get(server.URL)
	if unknown != nil {
		defer func() { _ = unknown.Body.Close() }()
		if unknown.StatusCode != 404 {
			t.Fatal("preview served on API/unrecognized host")
		}
	}
}

func TestProxyRevocationAndSessionLimits(t *testing.T) {
	t.Parallel()
	m, session, server := previewFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	if m.Revoke("other-sandbox", session.ID) {
		t.Fatal("another sandbox revoked the session")
	}
	if _, err := m.Create("sandbox", 3001, time.Now().Add(time.Minute), func(context.Context) (func(), error) { return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("sandbox", 3002, time.Now().Add(time.Minute), nil); err != ErrLimit {
		t.Fatalf("session limit ignored: %v", err)
	}
	if !m.Revoke("sandbox", session.ID) {
		t.Fatal("revoke failed")
	}
	u, _ := url.Parse(session.URL)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	req.Host = u.Host
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 410 {
		t.Fatalf("revoked preview returned %d", res.StatusCode)
	}
}

func TestProxyRelaysWebSocketAndClosesItOnRevoke(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{}
	m, session, server := previewFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}))
	u, _ := url.Parse(session.URL)
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	}}
	conn, res, err := dialer.Dial("ws://"+u.Host+"/hmr", http.Header{"Origin": []string{strings.TrimSuffix(session.URL, "/")}})
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hmr event")); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil || string(body) != "hmr event" {
		t.Fatalf("websocket echo %q: %v", body, err)
	}
	m.Revoke("sandbox", session.ID)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("revoked websocket remains open")
	}
}

func TestListReportsOnlyThisSandboxesLivePreviews(t *testing.T) {
	m, session, _ := previewFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "app")
	}))

	sessions := m.List("sandbox")
	if len(sessions) != 1 || sessions[0].ID != session.ID || sessions[0].Port != 3000 {
		t.Fatalf("a sandbox's own preview should be listed: %+v", sessions)
	}
	if other := m.List("another-sandbox"); len(other) != 0 {
		t.Errorf("previews belong to one sandbox, got %+v", other)
	}
	// A revoked preview is gone, not merely closed.
	m.Revoke("sandbox", session.ID)
	if sessions := m.List("sandbox"); len(sessions) != 0 {
		t.Errorf("a revoked preview is still listed: %+v", sessions)
	}
}
