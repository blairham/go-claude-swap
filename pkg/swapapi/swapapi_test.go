package swapapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/blairham/go-claude-swap/internal/autoswitch"
)

// shortSockPath returns a socket path under /tmp: sun_path is capped around
// 104 bytes on macOS, and t.TempDir() blows past it.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swapapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func startServer(t *testing.T) (*Server, *Broadcast, AutoSwitchServiceClient) {
	t.Helper()
	sock := shortSockPath(t)
	bcast := NewBroadcast(nil)
	engine := autoswitch.NewEngine(autoswitch.Config{
		Threshold: 85, Interval: 60, Strategy: "best", DryRun: true, UnhealthyTicks: 3,
	}, bcast)

	srv, err := Serve(sock, engine, bcast, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return srv, bcast, NewAutoSwitchServiceClient(conn)
}

func TestGetStatus(t *testing.T) {
	_, _, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := client.GetStatus(ctx, &GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.DryRun || st.ThresholdPct != 85 || st.Strategy != "best" || st.Version != "test" {
		t.Fatalf("status: %+v", st)
	}
}

func TestStreamEventsDeliversEngineEvents(t *testing.T) {
	_, bcast, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.StreamEvents(ctx, &StreamEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Give the server a moment to register the subscriber, then emit
	// through the same sink the engine uses.
	time.Sleep(100 * time.Millisecond)
	bcast.Emit(autoswitch.Event{
		Kind: "switch", TS: time.Now(),
		Fields: map[string]any{"from": "Account-1", "to": "Account-2", "trigger": "proactive"},
	})

	ev, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != "switch" || ev.Human == "" || ev.PayloadJson == "" {
		t.Fatalf("event: %+v", ev)
	}
}

func TestWake(t *testing.T) {
	_, _, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.Wake(ctx, &WakeRequest{})
	if err != nil || !resp.Woken {
		t.Fatalf("wake: %+v %v", resp, err)
	}
}

func TestServeRefusesSecondServer(t *testing.T) {
	sock := shortSockPath(t)
	bcast := NewBroadcast(nil)
	engine := autoswitch.NewEngine(autoswitch.Config{Threshold: 90, Interval: 60, Strategy: "best", UnhealthyTicks: 3}, bcast)
	srv, err := Serve(sock, engine, bcast, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	if _, err := Serve(sock, engine, bcast, "test"); err == nil {
		t.Fatal("a second server on a live socket must be refused")
	}
}

func TestBroadcastSubscribeCancel(t *testing.T) {
	b := NewBroadcast(nil)
	ch, cancel := b.Subscribe()
	b.Emit(autoswitch.Event{Kind: "poll", TS: time.Now()})
	if ev := <-ch; ev.Kind != "poll" {
		t.Fatalf("event: %+v", ev)
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("canceled subscription channel must be closed")
	}
	// Emitting after cancel must not panic.
	b.Emit(autoswitch.Event{Kind: "poll", TS: time.Now()})
}
