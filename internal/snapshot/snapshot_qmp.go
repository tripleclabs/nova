package snapshot

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// tryQMPHMPCommand connects to the QMP socket in machineDir and runs an HMP
// (Human Monitor Protocol) command via QEMU's human-monitor-command passthrough.
// Returns a non-nil error if the socket is absent (VM not running), the
// connection fails, or QEMU reports an error executing the command.
func tryQMPHMPCommand(machineDir, hmpCmd string) error {
	sockPath := filepath.Join(machineDir, "qmp.sock")
	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		return err // socket absent — VM is not running
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	// Consume the QMP greeting banner.
	if err := skipToReturn(dec); err != nil {
		return fmt.Errorf("reading QMP greeting: %w", err)
	}

	// Enter command mode.
	if err := enc.Encode(map[string]any{"execute": "qmp_capabilities"}); err != nil {
		return fmt.Errorf("QMP capabilities: %w", err)
	}
	if err := skipToReturn(dec); err != nil {
		return fmt.Errorf("QMP capabilities response: %w", err)
	}

	// Issue the HMP command.
	if err := enc.Encode(map[string]any{
		"execute":   "human-monitor-command",
		"arguments": map[string]string{"command-line": hmpCmd},
	}); err != nil {
		return fmt.Errorf("sending HMP command %q: %w", hmpCmd, err)
	}

	return readHMPResult(dec, hmpCmd)
}

// skipToReturn reads QMP messages until it finds one with a "return" key,
// discarding async events along the way.
func skipToReturn(dec *json.Decoder) error {
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, isEvent := raw["event"]; isEvent {
			continue
		}
		return nil // got return or error — caller doesn't need the value
	}
}

// readHMPResult reads the QMP response to a human-monitor-command, skipping
// async events. Non-empty HMP output is treated as an error.
func readHMPResult(dec *json.Decoder, hmpCmd string) error {
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("reading HMP response for %q: %w", hmpCmd, err)
		}
		if _, isEvent := raw["event"]; isEvent {
			continue
		}
		if errMsg, hasError := raw["error"]; hasError {
			var qmpErr struct {
				Desc string `json:"desc"`
			}
			json.Unmarshal(errMsg, &qmpErr) //nolint:errcheck
			return fmt.Errorf("%s: %s", hmpCmd, qmpErr.Desc)
		}
		if ret, hasReturn := raw["return"]; hasReturn {
			var returnStr string
			json.Unmarshal(ret, &returnStr) //nolint:errcheck
			if out := strings.TrimSpace(returnStr); out != "" {
				// HMP output on success is empty; non-empty is an error message.
				return fmt.Errorf("%s: %s", hmpCmd, out)
			}
			return nil
		}
	}
}
