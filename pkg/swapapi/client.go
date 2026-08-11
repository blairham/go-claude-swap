package swapapi

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// Dial connects to the control API on the default socket. The unix socket's
// 0600 mode is the access control; no TLS on localhost IPC.
func Dial() (*grpc.ClientConn, error) {
	return grpc.NewClient("unix://"+paths.SocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// Probe reports whether a live engine answers on the socket, and its status
// when it does. gRPC dials lazily, so liveness is proven by the GetStatus
// round-trip, not the dial.
func Probe(timeout time.Duration) (*GetStatusResponse, bool) {
	conn, err := Dial()
	if err != nil {
		return nil, false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	st, err := NewAutoSwitchServiceClient(conn).GetStatus(ctx, &GetStatusRequest{})
	if err != nil {
		return nil, false
	}
	return st, true
}
