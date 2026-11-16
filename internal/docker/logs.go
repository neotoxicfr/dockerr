package docker

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// LogLine is one line of a container's output.
type LogLine struct {
	C   int       `json:"c"` // index of the container in the request
	T   time.Time `json:"t"`
	Err bool      `json:"e,omitempty"` // stderr
	M   string    `json:"m"`
}

// maxLine cuts very long lines (a JSON dump on one line).
const maxLine = 16 << 10

// StreamLogs sends a container's log lines to out until the stream ends
// (or ctx does). tty: the container has a terminal, its output is not
// multiplexed.
func StreamLogs(ctx context.Context, api client.APIClient, id string, idx int, tty bool, opts client.ContainerLogsOptions, out chan<- LogLine) error {
	opts.Timestamps = true
	rc, err := api.ContainerLogs(ctx, id, opts)
	if err != nil {
		return err
	}
	defer rc.Close()
	if tty {
		return scanLines(ctx, rc, idx, false, out)
	}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	done := make(chan error, 2)
	go func() { done <- scanLines(ctx, outR, idx, false, out) }()
	go func() { done <- scanLines(ctx, errR, idx, true, out) }()
	_, err = stdcopy.StdCopy(outW, errW, rc)
	outW.Close()
	errW.Close()
	<-done
	<-done
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func scanLines(ctx context.Context, r io.Reader, idx int, stderr bool, out chan<- LogLine) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := ParseLogLine(sc.Text())
		l.C, l.Err = idx, stderr
		select {
		case out <- l:
		case <-ctx.Done():
			// Drain so the writer side is not left blocked.
			_, _ = io.Copy(io.Discard, r)
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		_, _ = io.Copy(io.Discard, r)
		return err
	}
	return nil
}

// ParseLogLine splits Docker's "2006-01-02T15:04:05.999999999Z message".
func ParseLogLine(s string) LogLine {
	s = strings.TrimSuffix(s, "\r")
	var l LogLine
	if ts, rest, ok := strings.Cut(s, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			l.T, s = t, rest
		}
	}
	if len(s) > maxLine {
		s = s[:maxLine] + " …"
	}
	l.M = s
	return l
}
