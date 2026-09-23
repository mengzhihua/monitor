// monitorctl talks to a running monitord. It does not collect or store metrics.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "monitorctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("monitorctl", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:19999", "agent or hub base URL")
	token := fs.String("token", "", "bearer token (web password or API key)")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := "info"
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
	}
	path := map[string]string{
		"info":       "/api/v1/info",
		"health":     "/healthz",
		"alarms":     "/api/v1/alarms",
		"collectors": "/api/v1/collectors",
		"nodes":      "/api/v1/nodes",
	}[cmd]
	if path == "" {
		return fmt.Errorf("unknown command %q (info, health, alarms, collectors, nodes)", cmd)
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(*url, "/")+path, nil)
	if err != nil {
		return err
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, bytesTrim(body))
	}
	if cmd == "health" {
		fmt.Println(string(body))
		return nil
	}
	var pretty bytesPretty
	if json.Unmarshal(body, &pretty.v) == nil {
		out, _ := json.MarshalIndent(pretty.v, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	fmt.Println(string(body))
	return nil
}

type bytesPretty struct{ v any }

func bytesTrim(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
