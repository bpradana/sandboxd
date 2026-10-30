package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

const cloudTestTimeout = 30 * time.Second

func (s *service) loadCloudStorage(ctx context.Context) error {
	s.snapshots = nil
	s.cloudUpdatedAt = time.Time{}
	s.cloudSettings = snapshot.NewSettingsFile(s.snapshotCfg.SettingsDir)
	record, exists, err := s.cloudSettings.Load()
	if err != nil {
		return fmt.Errorf("load cloud storage settings: %w", err)
	}
	s.cloudConnection = snapshot.ConnectionFromConfig(s.snapshotCfg)
	s.cloudSource = "none"
	if exists {
		s.cloudConnection, s.cloudSource, s.cloudUpdatedAt = record.Connection, "saved", record.UpdatedAt
		if record.Disconnected {
			return nil
		}
		s.snapshots, err = snapshot.NewConnectionStore(ctx, s.snapshotCfg, record.Connection)
	} else if s.snapshotCfg.Enabled {
		s.cloudSource = "environment"
		s.snapshots, err = snapshot.NewS3Store(ctx, s.snapshotCfg)
	}
	return err
}

func (s *service) cloudStorageResponse() *presentation.CloudStorageResponse {
	c := s.cloudConnection
	if c.AuthMode == "" {
		c.AuthMode = snapshot.AuthServerIdentity
	}
	return &presentation.CloudStorageResponse{
		Connected: s.snapshots != nil, Source: s.cloudSource,
		AuthMode: c.AuthMode, Bucket: c.Bucket, Region: c.Region, Endpoint: c.Endpoint, Prefix: c.Prefix, PathStyle: c.PathStyle,
		HasSavedCredentials: s.cloudSource == "saved" && c.AuthMode == snapshot.AuthAccessKeys && c.SecretAccessKey != "", UpdatedAt: s.cloudUpdatedAt,
	}
}

func (s *service) GetCloudStorage(context.Context) (*presentation.CloudStorageResponse, error) {
	s.snapshotStoreMu.RLock()
	defer s.snapshotStoreMu.RUnlock()
	return s.cloudStorageResponse(), nil
}

func (s *service) resolveCloudConnection(req presentation.CloudStorageRequest) (snapshot.Connection, error) {
	if err := validation.Struct(req); err != nil {
		return snapshot.Connection{}, invalidCloudStorage("invalid cloud storage settings")
	}
	c := snapshot.Connection{
		AuthMode: req.AuthMode, Bucket: strings.TrimSpace(req.Bucket), Region: strings.TrimSpace(req.Region),
		Endpoint: strings.TrimSpace(req.Endpoint), Prefix: strings.Trim(strings.TrimSpace(req.Prefix), "/"), PathStyle: req.PathStyle,
		AccessKeyID: req.AccessKeyID, SecretAccessKey: req.SecretAccessKey, SessionToken: req.SessionToken,
	}
	if req.UseSavedCredentials {
		if c.AuthMode != snapshot.AuthAccessKeys || s.cloudSource != "saved" || s.cloudConnection.AuthMode != snapshot.AuthAccessKeys || s.cloudConnection.SecretAccessKey == "" || c.AccessKeyID != "" || c.SecretAccessKey != "" || c.SessionToken != "" {
			return c, invalidCloudStorage("saved access keys are unavailable or were combined with new credentials")
		}
		c.AccessKeyID, c.SecretAccessKey, c.SessionToken = s.cloudConnection.AccessKeyID, s.cloudConnection.SecretAccessKey, s.cloudConnection.SessionToken
	}
	if err := c.Validate(); err != nil {
		return c, invalidCloudStorage(err.Error())
	}
	return c, nil
}

func invalidCloudStorage(message string) error {
	return appctx.NewHTTPError(http.StatusBadRequest, message).WithErrorCode(appctx.CodeValidation)
}

func cloudStorageBusy() error {
	return appctx.NewHTTPError(http.StatusConflict, "cloud storage is busy; finish snapshot operations before changing the connection").WithErrorCode(appctx.CodeCloudStorageBusy)
}

func (s *service) checkCloudConnection(ctx context.Context, c snapshot.Connection) (snapshot.ConnectionStore, error) {
	ctx, cancel := context.WithTimeout(ctx, cloudTestTimeout)
	defer cancel()
	store, err := snapshot.NewConnectionStore(ctx, s.snapshotCfg, c)
	if err == nil {
		err = store.TestConnection(ctx)
	}
	if err != nil {
		// Provider responses can contain credential identifiers or reflect input;
		// keep them out of client errors, request logs and tracing error causes.
		return nil, appctx.NewHTTPError(http.StatusBadGateway, snapshot.ConnectionTestMessage(err)).WithErrorCode(appctx.CodeCloudStorageTestFailed)
	}
	return store, nil
}

func (s *service) TestCloudStorage(ctx context.Context, req presentation.CloudStorageRequest) error {
	s.snapshotStoreMu.RLock()
	c, err := s.resolveCloudConnection(req)
	s.snapshotStoreMu.RUnlock()
	if err != nil {
		return err
	}
	_, err = s.checkCloudConnection(ctx, c)
	return err
}

func (s *service) SaveCloudStorage(ctx context.Context, req presentation.CloudStorageRequest) (*presentation.CloudStorageResponse, error) {
	if !s.snapshotStoreMu.TryLock() {
		return nil, cloudStorageBusy()
	}
	defer s.snapshotStoreMu.Unlock()
	c, err := s.resolveCloudConnection(req)
	if err != nil {
		return nil, err
	}
	store, err := s.checkCloudConnection(ctx, c)
	if err != nil {
		return nil, err
	}
	record := snapshot.ConnectionRecord{Connection: c, UpdatedAt: s.now()}
	if err := s.cloudSettings.Save(record); err != nil {
		return nil, mapError(err)
	}
	s.cloudConnection, s.cloudUpdatedAt, s.cloudSource, s.snapshots = c, record.UpdatedAt, "saved", store
	return s.cloudStorageResponse(), nil
}

func (s *service) DisconnectCloudStorage(context.Context) error {
	if !s.snapshotStoreMu.TryLock() {
		return cloudStorageBusy()
	}
	defer s.snapshotStoreMu.Unlock()
	c := s.cloudConnection
	c.AccessKeyID, c.SecretAccessKey, c.SessionToken = "", "", ""
	c.AuthMode = snapshot.AuthServerIdentity
	record := snapshot.ConnectionRecord{Disconnected: true, Connection: c, UpdatedAt: s.now()}
	// Persist an explicit disconnect, so environment configuration cannot
	// silently reconnect the destination after a daemon restart.
	if err := s.cloudSettings.Save(record); err != nil {
		return mapError(err)
	}
	s.cloudConnection, s.cloudUpdatedAt, s.cloudSource, s.snapshots = c, record.UpdatedAt, "saved", nil
	return nil
}
