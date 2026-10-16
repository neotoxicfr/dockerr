// Package execterm bridges a browser terminal (WebSocket) to a TTY exec
// session in a container. Binary frames carry terminal bytes both ways;
// text frames from the browser carry JSON control messages (resize).
package execterm

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/coder/websocket"
	"github.com/moby/moby/client"
)

// Shell starts bash when the image has it, sh otherwise.
var Shell = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

type control struct {
	Type string `json:"type"`
	Cols uint   `json:"cols"`
	Rows uint   `json:"rows"`
}

func Serve(ctx context.Context, ws *websocket.Conn, api client.APIClient, containerID, user string, cols, rows uint) error {
	ws.SetReadLimit(1 << 20)
	ex, err := api.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		User: user, TTY: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Env: []string{"TERM=xterm-256color"}, Cmd: Shell,
		ConsoleSize: client.ConsoleSize{Height: rows, Width: cols},
	})
	if err != nil {
		return err
	}
	att, err := api.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: client.ConsoleSize{Height: rows, Width: cols}})
	if err != nil {
		return err
	}
	defer att.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := att.Reader.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if typ == websocket.MessageText {
				var c control
				if json.Unmarshal(data, &c) == nil && c.Type == "resize" && c.Cols > 0 && c.Rows > 0 {
					_, _ = api.ExecResize(ctx, ex.ID, client.ExecResizeOptions{Height: c.Rows, Width: c.Cols})
				}
				continue
			}
			if _, err := att.Conn.Write(data); err != nil {
				errc <- err
				return
			}
		}
	}()
	err = <-errc
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
		err = nil
	}
	_ = ws.Close(websocket.StatusNormalClosure, "session terminée")
	return err
}
