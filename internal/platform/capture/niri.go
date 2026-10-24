package capture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// NiriActive asks niri for its focused output over the IPC socket, the
// same request `niri msg --json focused-output` sends. It avoids spawning
// a process on every capture.
type NiriActive struct {
	// Socket is $NIRI_SOCKET.
	Socket string
	// Timeout bounds the request; zero means 200 ms.
	Timeout time.Duration
}

func (n NiriActive) Active(ctx context.Context, _ contracts.Topology) (contracts.DisplayID, error) {
	if n.Socket == "" {
		return "", errors.New("niri: no socket")
	}
	timeout := n.Timeout
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", n.Socket)
	if err != nil {
		return "", fmt.Errorf("niri: connect: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write([]byte("\"FocusedOutput\"\n")); err != nil {
		return "", fmt.Errorf("niri: write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return "", fmt.Errorf("niri: read: %w", err)
	}
	return parseNiriFocused(line)
}

// parseNiriFocused decodes {"Ok":{"FocusedOutput":{"name":...}}}. niri
// answers {"Ok":{"FocusedOutput":null}} when no output is focused and
// {"Err":"..."} on failure; the error text is not kept.
func parseNiriFocused(b []byte) (contracts.DisplayID, error) {
	var reply struct {
		Ok *struct {
			FocusedOutput *struct {
				Name string `json:"name"`
			}
		}
		Err *string
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		return "", errors.New("niri: malformed reply")
	}
	switch {
	case reply.Err != nil:
		return "", errors.New("niri: request failed")
	case reply.Ok == nil || reply.Ok.FocusedOutput == nil || reply.Ok.FocusedOutput.Name == "":
		return "", errors.New("niri: no focused output")
	}
	return contracts.DisplayID(reply.Ok.FocusedOutput.Name), nil
}
