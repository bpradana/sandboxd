// Package sandbox implements the domain logic for sandbox lifecycle, command execution, sessions, and file management.
package sandbox

import (
	"context"
	"io"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/observability/metrics"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

type Service interface {
	Ready() error
	ListImages(ctx context.Context) ([]presentation.ImageResponse, error)

	Create(ctx context.Context, req presentation.CreateSandboxRequest) (*presentation.SandboxResponse, error)
	Get(ctx context.Context, id string) (*presentation.SandboxResponse, error)
	List(ctx context.Context, req presentation.ListSandboxesRequest) ([]presentation.SandboxResponse, error)
	Stats(ctx context.Context, id string) (*presentation.SandboxStatsResponse, error)
	Keepalive(ctx context.Context, id string, req presentation.KeepaliveRequest) (*presentation.SandboxResponse, error)
	Destroy(ctx context.Context, id string) error

	Exec(ctx context.Context, id string, req presentation.ExecRequest) (*presentation.ExecResponse, error)
	ExecStream(ctx context.Context, id string, req presentation.ExecRequest, emit StreamEmitter) error

	CreateSession(ctx context.Context, id string, req presentation.CreateSessionRequest) (*presentation.SessionResponse, error)
	ListSessions(ctx context.Context, id string) ([]presentation.SessionResponse, error)
	SessionExec(ctx context.Context, id, sessionID string, req presentation.SessionExecRequest) (*presentation.ExecResponse, error)
	CloseSession(ctx context.Context, id, sessionID string) error

	CreateTerminalTicket(ctx context.Context, id string) (*presentation.TerminalTicketResponse, error)
	// RedeemTerminalTicket consumes a ticket issued for the sandbox.
	RedeemTerminalTicket(id, ticket string) bool
	Terminal(ctx context.Context, id string, req presentation.TerminalRequest, input <-chan TerminalInput, out TerminalOutput, attached func(terminalID string) error) (*presentation.ExecResponse, error)

	OpenFile(ctx context.Context, id string, req presentation.FilePathRequest) (io.ReadCloser, *presentation.FileInfoResponse, error)
	WriteFile(ctx context.Context, id string, req presentation.WriteFileRequest, body io.Reader) (*presentation.FileInfoResponse, error)
	ListDir(ctx context.Context, id string, req presentation.FilePathRequest) ([]presentation.FileInfoResponse, error)
	StatFile(ctx context.Context, id string, req presentation.FilePathRequest) (*presentation.FileInfoResponse, error)
	Mkdir(ctx context.Context, id string, req presentation.MkdirRequest) (*presentation.FileInfoResponse, error)
	RemoveFile(ctx context.Context, id string, req presentation.RemoveFileRequest) error
}

// StreamEmitter writes one Server-Sent Event. Event is stdout, stderr, or exit.
type StreamEmitter func(event string, payload presentation.ExecStreamEvent) error

type Params struct {
	fx.In

	Lifecycle fx.Lifecycle
	Repo      sandboxrepository.Repository
	Engine    engine.Engine
	Logger    *logging.Logger
	Metrics   *metrics.Registry `optional:"true"`
	Sandbox   config.SandboxConfig
	Exec      config.ExecConfig
	Network   config.NetworkConfig
}

type service struct {
	repo    sandboxrepository.Repository
	engine  engine.Engine
	logger  *logging.Logger
	metrics *metrics.Registry
	sandbox config.SandboxConfig
	exec    config.ExecConfig
	network config.NetworkConfig

	// inFlight counts running commands and uploads per sandbox; the idle
	// reaper never stops a sandbox while it is doing work.
	inFlightMu sync.Mutex
	inFlight   map[string]int

	tickets *ticketStore

	// background owns the reaper and exit-watcher goroutines.
	background sync.WaitGroup
	stop       chan struct{}
	now        func() time.Time
}

func NewService(params Params) Service {
	svc := newService(params.Repo, params.Engine, params.Logger, params.Metrics, params.Sandbox, params.Exec, params.Network)
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			svc.startBackground()
			return nil
		},
		OnStop: func(context.Context) error {
			svc.stopBackground()
			return nil
		},
	})
	return svc
}

func newService(
	repo sandboxrepository.Repository,
	eng engine.Engine,
	logger *logging.Logger,
	registry *metrics.Registry,
	sandboxCfg config.SandboxConfig,
	execCfg config.ExecConfig,
	networkCfg config.NetworkConfig,
) *service {
	return &service{
		repo:     repo,
		engine:   eng,
		logger:   logger,
		metrics:  registry,
		sandbox:  sandboxCfg,
		exec:     execCfg,
		network:  networkCfg,
		stop:     make(chan struct{}),
		now:      time.Now,
		inFlight: make(map[string]int),
		tickets:  newTicketStore(),
	}
}

func (s *service) Ready() error {
	return s.engine.Ready()
}
