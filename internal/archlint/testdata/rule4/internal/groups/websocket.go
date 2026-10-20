package groups

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	ws "github.com/coder/websocket"
)

// Bad: every marked line dials a WebSocket connection outside internal/outbound.
func Sockets(ctx context.Context, target string) []any {
	a, _, _ := websocket.Dial(ctx, target, nil)                       // want: 4
	b, _, _ := ws.Dial(ctx, target, &ws.DialOptions{HTTPClient: nil}) // want: 4
	dial := websocket.Dial                                            // want: 4
	return []any{a, b, dial}
}

// Good: accepting a WebSocket connection and building dial options open nothing.
func Serve(w http.ResponseWriter, r *http.Request) error {
	opts := websocket.DialOptions{HTTPHeader: http.Header{}}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return err
	}
	_ = opts
	return conn.CloseNow()
}
