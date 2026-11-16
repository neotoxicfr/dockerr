package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

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
		return fmt.Errorf("lecture des logs : %w", err)
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
	_ = outW.Close() // a pipe writer's Close never fails
	_ = errW.Close()
	scanOut, scanErr := <-done, <-done
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("flux des logs interrompu : %w", err)
	}
	return errors.Join(scanOut, scanErr)
}

// scanLines reads lines of any length: past maxLine (plus room for the
// timestamp) the rest of a line is skipped, never the stream.
func scanLines(ctx context.Context, r io.Reader, idx int, stderr bool, out chan<- LogLine) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(buf) < maxLine+64 {
			buf = append(buf, chunk[:min(len(chunk), maxLine+64-len(buf))]...)
		}
		if isPrefix && err == nil {
			continue
		}
		if err != nil {
			_, _ = io.Copy(io.Discard, r) // the writer side must not stay blocked
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("lecture des logs : %w", err)
		}
		l := ParseLogLine(string(buf))
		buf = buf[:0]
		l.C, l.Err = idx, stderr
		select {
		case out <- l:
		case <-ctx.Done():
			_, _ = io.Copy(io.Discard, r)
			return nil
		}
	}
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
		cut := maxLine
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut-- // never in the middle of a character
		}
		s = s[:cut] + " …"
	}
	l.M = s
	return l
}
