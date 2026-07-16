// rate-limit prints the GitHub API rate-limit budget for the configured token.
// The /rate_limit endpoint itself does not consume the core budget.
//
// Usage:
//   GITHUB_TOKEN=ghp_xxx go run ./cmd/rate-limit
//   go run ./cmd/rate-limit -token ghp_xxx
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

type rateLimitResponse struct {
	Resources map[string]bucket `json:"resources"`
	Rate      bucket            `json:"rate"`
}

type bucket struct {
	Limit     int   `json:"limit"`
	Used      int   `json:"used"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

func main() {
	tokenFlag := flag.String("token", "", "GitHub token (default $GITHUB_TOKEN)")
	flag.Parse()

	token := *tokenFlag
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "ERROR: GITHUB_TOKEN env or -token flag required")
		os.Exit(2)
	}

	req, err := http.NewRequest("GET", "https://api.github.com/rate_limit", nil)
	if err != nil {
		fail("build request: %v", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fail("request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fail("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		fail("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data rateLimitResponse
	if err := json.Unmarshal(body, &data); err != nil {
		fail("parse json: %v", err)
	}

	names := make([]string, 0, len(data.Resources))
	for name := range data.Resources {
		names = append(names, name)
	}
	sort.Strings(names)

	now := time.Now()
	fmt.Printf("%-32s %10s %10s %10s   %s\n", "RESOURCE", "USED", "REMAIN", "LIMIT", "RESET")
	for _, name := range names {
		printRow(name, data.Resources[name], now)
	}
}

func printRow(name string, b bucket, now time.Time) {
	reset := time.Unix(b.Reset, 0).Local()
	in := reset.Sub(now).Round(time.Second)
	if in < 0 {
		in = 0
	}
	pct := 100
	if b.Limit > 0 {
		pct = b.Remaining * 100 / b.Limit
	}
	warn := ""
	if pct < 10 {
		warn = " ⚠"
	}
	fmt.Printf("%-32s %10d %10d %10d   %s (in %s)%s\n",
		name, b.Used, b.Remaining, b.Limit,
		reset.Format("15:04:05"), in, warn)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
