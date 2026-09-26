package runed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// writeLog drops one turn's record beside the group's other logs: what was
// mounted, how the guest exited, and its stderr. The operator reads it after a
// failure, so an error or LOG_LEVEL=debug gets the whole envelope and a clean
// turn gets the summary.
func writeLog(
	path string, t Turn,
	dur time.Duration, code int,
	timedOut, framed bool, stderr string,
) {
	// A turn with NO exit frame is an error however innocent its code looks. The
	// guest's own code is the only word on how the agent ended, and when the frame
	// is missing `code` is QEMU's status — which was 0 while the agent had exited 1,
	// so this file recorded a clean turn with no stderr for the one failure that
	// needed reading (BUGS `L375`).
	isErr := code != 0 || timedOut || !framed
	lvl := os.Getenv("LOG_LEVEL")
	verbose := lvl == "debug" || lvl == "trace"

	suffix := ""
	if timedOut {
		suffix = " (TIMEOUT)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== Container Run Log%s ===\n", suffix)
	fmt.Fprintf(&b, "Timestamp: %s\nGroup: %s\nContainer: %s\nDuration: %s\nExit Code: %d",
		time.Now().Format(time.RFC3339), t.Folder, t.Handle, dur, code)
	if framed {
		b.WriteString("\n")
	} else {
		// Said in the file, because the number beside it is not the agent's.
		b.WriteString(" (QEMU's — the guest sent no exit frame)\n")
	}

	b.WriteString("\n=== Mounts ===\n")
	for _, m := range t.Mounts {
		ro := ""
		if m.RO {
			ro = " (ro)"
		}
		if verbose || isErr {
			fmt.Fprintf(&b, "%s -> %s%s\n", m.Host, m.Container, ro)
		} else {
			fmt.Fprintf(&b, "%s%s\n", m.Container, ro)
		}
	}

	frame, ferr := scrubbedPayload(t.Stdin)
	if verbose || isErr {
		fmt.Fprintf(&b, "\n=== Input ===\n")
		if ferr != nil {
			fmt.Fprintf(&b, "<payload not readable as JSON: %v>", ferr)
		} else {
			ij, _ := json.MarshalIndent(frame, "", "  ")
			b.Write(ij)
		}
		fmt.Fprintf(&b, "\n\n=== Stderr ===\n%s\n", stderr)
	} else {
		prompt, _ := frame["prompt"].(string)
		sid, _ := frame["sessionId"].(string)
		if sid == "" {
			sid = "new"
		}
		fmt.Fprintf(&b, "\n=== Input Summary ===\nPrompt length: %d chars\nSession ID: %s\n",
			len(prompt), sid)
	}

	os.WriteFile(path, []byte(b.String()), 0o644)
	slog.Debug("container log written", "logFile", path, "verbose", verbose)
}

// scrubbedPayload decodes the guest's stdin frame with its secrets removed, so
// the turn log can name what was sent without writing model credentials to
// disk. A frame it cannot decode yields the error and NO bytes: the raw frame
// carries the secrets this drops.
func scrubbedPayload(stdin []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(stdin, &m); err != nil {
		return nil, err
	}
	delete(m, "secrets")
	return m, nil
}
