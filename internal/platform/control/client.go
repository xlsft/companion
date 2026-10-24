package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ErrNotRunning means nothing listens on the socket: the core is not
// started (or crashed and left a stale socket).
var ErrNotRunning = errors.New("control: companion is not running")

// Send delivers one protocol line and waits for its reply. A reply other
// than "ok" is returned as *ProtocolError.
func Send(ctx context.Context, path, line string) error {
	if strings.ContainsAny(line, "\r\n") || len(line) >= maxLine {
		return &ProtocolError{CodeBadCommand}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connTimeout)
		defer cancel()
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w (%s)", ErrNotRunning, path)
		}
		return fmt.Errorf("control: dial: %w", err)
	}
	defer c.Close()
	dl, _ := ctx.Deadline()
	_ = c.SetDeadline(dl)
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		return fmt.Errorf("control: write: %w", err)
	}
	resp, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("control: read reply: %w", err)
	}
	resp = strings.TrimSpace(resp)
	if resp == "ok" {
		return nil
	}
	if code, ok := strings.CutPrefix(resp, "err "); ok {
		return &ProtocolError{Code: code}
	}
	return fmt.Errorf("control: unexpected reply")
}

// Ctl runs one "companion ctl" subcommand against the socket at path.
func Ctl(ctx context.Context, path, sub string) error {
	line, err := CtlLine(sub)
	if err != nil {
		return err
	}
	return Send(ctx, path, line)
}
