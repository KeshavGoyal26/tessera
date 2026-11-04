package signal

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// writeTimeout bounds a single WebSocket write.
const writeTimeout = 10 * time.Second

// wsConn implements Conn over a WebSocket with a bounded write queue
// drained by a dedicated writer goroutine.
type wsConn struct {
	ws     *websocket.Conn
	json   bool
	remote string
	queue  chan *tesserav1.SignalResponse

	closeOnce sync.Once
	closed    chan struct{}
	mu        sync.Mutex
	code      int
	reason    string
}

func newWSConn(ws *websocket.Conn, json bool, queueSize int, remote string) *wsConn {
	if queueSize <= 0 {
		queueSize = 256
	}
	return &wsConn{
		ws: ws, json: json, remote: remote,
		queue:  make(chan *tesserav1.SignalResponse, queueSize),
		closed: make(chan struct{}),
		code:   CloseNormal,
	}
}

// ReadRequest implements Conn.
func (c *wsConn) ReadRequest(ctx context.Context) (*tesserav1.SignalRequest, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	req := &tesserav1.SignalRequest{}
	if typ == websocket.MessageText {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, req)
	} else {
		err = proto.Unmarshal(data, req)
	}
	if err != nil {
		return nil, errs.Wrap(errs.CodeMalformed, err, "decode signal request")
	}
	return req, nil
}

// WriteResponse implements Conn: non-blocking enqueue.
func (c *wsConn) WriteResponse(msg *tesserav1.SignalResponse) error {
	select {
	case <-c.closed:
		return errs.New(errs.CodeUnavailable, "connection closed")
	default:
	}
	select {
	case c.queue <- msg:
		return nil
	default:
		return errs.New(errs.CodeResourceExhausted, "signal write queue full")
	}
}

func (c *wsConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			c.flushAndClose()
			return
		case <-c.closed:
			c.flushAndClose()
			return
		case msg := <-c.queue:
			if err := c.write(ctx, msg); err != nil {
				c.closeOnce.Do(func() { close(c.closed) })
				_ = c.ws.CloseNow()
				return
			}
		}
	}
}

func (c *wsConn) write(ctx context.Context, msg *tesserav1.SignalResponse) error {
	var (
		data []byte
		err  error
		typ  = websocket.MessageBinary
	)
	if c.json {
		typ = websocket.MessageText
		data, err = protojson.Marshal(msg)
	} else {
		data, err = proto.Marshal(msg)
	}
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.ws.Write(wctx, typ, data)
}

// flushAndClose writes queued messages (best effort) and closes the socket.
func (c *wsConn) flushAndClose() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		select {
		case msg := <-c.queue:
			if err := c.write(ctx, msg); err != nil {
				_ = c.ws.CloseNow()
				return
			}
			continue
		default:
		}
		break
	}
	c.mu.Lock()
	code, reason := c.code, c.reason
	c.mu.Unlock()
	if err := c.ws.Close(websocket.StatusCode(code), reason); err != nil {
		_ = c.ws.CloseNow()
	}
}

// Close implements Conn. Queued messages are flushed first.
func (c *wsConn) Close(code int, reason string) error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.code, c.reason = code, reason
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

// RemoteAddr implements Conn.
func (c *wsConn) RemoteAddr() string { return c.remote }

// IsClosed reports whether err indicates a closed WebSocket.
func IsClosed(err error) bool {
	if err == nil {
		return false
	}
	var ce websocket.CloseError
	return errors.As(err, &ce) || errors.Is(err, context.Canceled) || websocket.CloseStatus(err) != -1
}
