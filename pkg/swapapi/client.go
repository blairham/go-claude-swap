// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package swapapi

import (
	"context"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// Dial connects to the control API on the default socket. The unix socket's
// 0600 mode is the access control; no TLS on localhost IPC.
func Dial() (*grpc.ClientConn, error) {
	return dialAt(paths.SocketPath())
}

func dialAt(socketPath string) (*grpc.ClientConn, error) {
	return grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// Wake asks a running `cswap auto` loop to tick now, cutting short whatever
// sleep it is in (up to an hour when every account is exhausted). It reports
// whether a loop took the request; with no loop running it is a quiet no-op,
// so callers can fire it after any roster change without checking first.
func Wake(timeout time.Duration) bool {
	return wakeAt(paths.SocketPath(), timeout)
}

func wakeAt(socketPath string, timeout time.Duration) bool {
	// No socket file: no loop. Skip gRPC entirely.
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}
	conn, err := dialAt(socketPath)
	if err != nil {
		return false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := NewAutoSwitchServiceClient(conn).Wake(ctx, &WakeRequest{})
	return err == nil && resp.GetWoken()
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
