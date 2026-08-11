// Package swapapi is the gRPC control API a running `cswap auto` loop
// serves over a unix socket in the backup root: presence/status, a live
// event stream, and wake-for-immediate-tick. The socket doubles as the
// engine-presence signal for the TUI.
package swapapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/blairham/go-claude-swap/internal/autoswitch"
)

// Server hosts the control API for one engine.
type Server struct {
	UnimplementedAutoSwitchServiceServer

	engine    *autoswitch.Engine
	bcast     *Broadcast
	version   string
	startedAt time.Time

	grpcServer *grpc.Server
	socketPath string
}

// Serve starts the API on a unix socket. A stale socket file from a dead
// process is removed first (the flock-based presence marker, not the
// socket file, is the liveness authority). Callers must Stop() on
// shutdown.
func Serve(socketPath string, engine *autoswitch.Engine, bcast *Broadcast, version string) (*Server, error) {
	// A previous unclean exit leaves the socket file behind; dial it to
	// tell a live server from a stale file.
	if _, err := os.Stat(socketPath); err == nil {
		conn, derr := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
		if derr == nil {
			conn.Close()
			return nil, errors.New("another cswap auto loop is already serving " + socketPath)
		}
		os.Remove(socketPath)
	}

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	// Owner-only: the API can switch accounts.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		lis.Close()
		os.Remove(socketPath)
		return nil, err
	}

	s := &Server{
		engine:     engine,
		bcast:      bcast,
		version:    version,
		startedAt:  time.Now(),
		grpcServer: grpc.NewServer(),
		socketPath: socketPath,
	}
	RegisterAutoSwitchServiceServer(s.grpcServer, s)
	go func() {
		// Serve returns on Stop; listener errors surface to clients as
		// connection failures, which they treat as "engine not running".
		_ = s.grpcServer.Serve(lis)
	}()
	return s, nil
}

// Stop shuts the API down and removes the socket file.
func (s *Server) Stop() {
	s.grpcServer.Stop()
	os.Remove(s.socketPath)
}

// GetStatus implements the API.
func (s *Server) GetStatus(context.Context, *GetStatusRequest) (*GetStatusResponse, error) {
	return &GetStatusResponse{
		Version:         s.version,
		DryRun:          s.engine.DryRun,
		ThresholdPct:    s.engine.Threshold,
		IntervalSeconds: s.engine.Interval,
		Strategy:        s.engine.Strategy,
		StartedAtUnix:   s.startedAt.Unix(),
	}, nil
}

// StreamEvents implements the API: follows the engine's event feed until
// the client disconnects.
func (s *Server) StreamEvents(_ *StreamEventsRequest, stream grpc.ServerStreamingServer[Event]) error {
	events, cancel := s.bcast.Subscribe()
	defer cancel()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			payload, err := json.Marshal(e.Fields)
			if err != nil {
				payload = []byte("{}")
			}
			if serr := stream.Send(&Event{
				Kind:        e.Kind,
				TsUnix:      e.TS.Unix(),
				PayloadJson: string(payload),
				Human:       e.Human(),
			}); serr != nil {
				return serr
			}
		}
	}
}

// Wake implements the API: the engine ticks as soon as it can.
func (s *Server) Wake(context.Context, *WakeRequest) (*WakeResponse, error) {
	s.engine.Wake()
	return &WakeResponse{Woken: true}, nil
}
