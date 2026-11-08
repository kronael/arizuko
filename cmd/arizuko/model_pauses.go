package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kronael/arizuko/resreg/resources"
)

func cmdModelPauses(args []string) {
	fs := flag.NewFlagSet("model-pauses", flag.ContinueOnError)
	base, token := os.Getenv("ROUTER_URL"), os.Getenv("ARIZUKO_TOKEN")
	var folder string
	fs.StringVar(&folder, "folder", "", "folder to clear")
	fs.StringVar(&base, "url", base, "routd base URL")
	fs.StringVar(&base, "u", base, "routd base URL")
	fs.StringVar(&token, "token", token, "operator bearer")
	fs.StringVar(&token, "t", token, "operator bearer")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 1 || base == "" {
		usage(err, "arizuko model-pauses <list|clear> --url|-u URL --token|-t BEARER")
	}
	if err := runModelPauses(&http.Client{Timeout: 15 * time.Second}, base, token, fs.Arg(0), folder, os.Stdout); err != nil {
		die("Failed: %v", err)
	}
}

func runModelPauses(client *http.Client, base, token, action, folder string, output io.Writer) error {
	if action != "list" && action != "clear" {
		return fmt.Errorf("model-pauses requires list or clear")
	}
	if action == "clear" && folder == "" {
		return fmt.Errorf("clear requires --folder")
	}
	call := func(method, path string, body io.Reader) ([]byte, error) {
		req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("model pauses: %s: %s", resp.Status, raw)
		}
		return raw, err
	}
	raw, err := call(http.MethodGet, "/v1/model_pauses", nil)
	if err != nil {
		return err
	}
	if action == "clear" {
		var rows []resources.ModelPauseRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		var selected *resources.ModelPauseRow
		for i := range rows {
			if rows[i].Folder == folder {
				selected = &rows[i]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("no model pause for folder %s", folder)
		}
		body, err := json.Marshal(map[string]string{"folder": folder, "opened_at": selected.OpenedAt,
			"probe_attempt_id": selected.ProbeAttemptID})
		if err != nil {
			return err
		}
		raw, err = call(http.MethodPost, "/v1/model_pauses/clear", bytes.NewReader(body))
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output, string(raw))
	return err
}
