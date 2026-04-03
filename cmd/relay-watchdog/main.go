// relay-watchdog monitors a scuttlebot server and signals relay processes
// to reconnect when the server restarts or becomes unreachable.
//
// Usage: relay-watchdog --url https://irc.scuttlebot.net --token <token> --signal <pid>
//
// It polls the server's /v1/status endpoint every 10 seconds. When the
// server's start time changes (restart) or the API is unreachable for 60
// seconds (network issue), it sends SIGUSR1 to the specified PID (or all
// relay processes if --signal 0).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if os.Getenv(k) == "" { // don't override explicit env
				os.Setenv(k, v)
			}
		}
	}
}

func main() {
	// Load the shared relay config.
	home, _ := os.UserHomeDir()
	if home != "" {
		loadEnvFile(home + "/.config/scuttlebot-relay.env")
	}

	url := flag.String("url", os.Getenv("SCUTTLEBOT_URL"), "scuttlebot API URL")
	token := flag.String("token", os.Getenv("SCUTTLEBOT_TOKEN"), "API token")
	interval := flag.Duration("interval", 10*time.Second, "poll interval")
	flag.Parse()

	if *url == "" || *token == "" {
		fmt.Fprintf(os.Stderr, "relay-watchdog: SCUTTLEBOT_URL and SCUTTLEBOT_TOKEN required\n")
		os.Exit(1)
	}

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var lastStart string
	failures := 0
	client := &http.Client{Timeout: 5 * time.Second}

	fmt.Fprintf(os.Stderr, "relay-watchdog: monitoring %s every %s\n", *url, *interval)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "relay-watchdog: shutting down\n")
			return
		case <-ticker.C:
		}

		start := getStart(client, *url, *token)
		if start == "" {
			failures++
			fmt.Fprintf(os.Stderr, "relay-watchdog: API unreachable (%d)\n", failures)
			if failures >= 6 { // 60s at 10s interval
				fmt.Fprintf(os.Stderr, "relay-watchdog: extended outage, will signal relays on recovery\n")
			}
			continue
		}

		if failures >= 6 {
			// We were down for a while and just came back.
			fmt.Fprintf(os.Stderr, "relay-watchdog: API recovered after %d failures, killing relays\n", failures)
			killRelays()
			lastStart = start
			failures = 0
			continue
		}

		if lastStart == "" {
			lastStart = start
			failures = 0
			continue
		}

		if start != lastStart {
			fmt.Fprintf(os.Stderr, "relay-watchdog: server restarted (was %s, now %s), killing relays\n", lastStart, start)
			killRelays()
			lastStart = start
		}
		failures = 0
	}
}

func getStart(client *http.Client, url, token string) string {
	req, err := http.NewRequest(http.MethodGet, url+"/v1/status", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var s struct {
		Started string `json:"started"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&s)
	return s.Started
}

func killRelays() {
	// Find relay processes and send SIGUSR1 to trigger IRC reconnection.
	// The relay handles SIGUSR1 by tearing down and rebuilding the IRC
	// connection without killing the Claude subprocess.
	out, err := exec.Command("pgrep", "-f", "(claude|codex|gemini)-relay").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay-watchdog: no relay processes found\n")
		return
	}
	pids := strings.Fields(strings.TrimSpace(string(out)))
	myPid := fmt.Sprintf("%d", os.Getpid())
	for _, pid := range pids {
		if pid == myPid {
			continue
		}
		fmt.Fprintf(os.Stderr, "relay-watchdog: signaling relay pid %s (SIGUSR1)\n", pid)
		_ = exec.Command("kill", "-USR1", pid).Run()
	}
}
