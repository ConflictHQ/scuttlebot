package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type Agent struct {
	Nick      string    `json:"nick"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
}

type Message struct {
	Nick string    `json:"nick"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

func main() {
	token := os.Getenv("SCUTTLEBOT_TOKEN")
	url := os.Getenv("SCUTTLEBOT_URL")
	if url == "" {
		url = "http://localhost:8080"
	}

	if token == "" {
		log.Fatal("SCUTTLEBOT_TOKEN is required")
	}

	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "map":
		mapFleet(url, token)
	case "broadcast":
		if len(os.Args) < 3 {
			log.Fatal("usage: fleet-cmd broadcast <message>")
		}
		broadcast(url, token, strings.Join(os.Args[2:], " "))
	default:
		usage()
	}
}

func usage() {
	fmt.Println("Usage: fleet-cmd <command> [args]")
	fmt.Println("Commands:")
	fmt.Println("  map          Show all agents and their last activity")
	fmt.Println("  broadcast    Send a message to all agents in #general")
	os.Exit(1)
}

func mapFleet(url, token string) {
	agents := fetchAgents(url, token)
	messages := fetchMessages(url, token, "general")

	// Filter for actual session nicks (ones with suffixes)
	sessions := make(map[string]Message)
	for _, m := range messages {
		if strings.Contains(m.Nick, "-") {
			sessions[m.Nick] = m
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NICK\tTYPE\tLAST ACTIVITY\tTIME")

	// Sort nicks for stable output
	var nicks []string
	for n := range sessions {
		nicks = append(nicks, n)
	}
	sort.Strings(nicks)

	for _, nick := range nicks {
		m := sessions[nick]
		nickType := "unknown"
		for _, a := range agents {
			if strings.HasPrefix(nick, a.Nick) {
				nickType = a.Type
				break
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", nick, nickType, truncate(m.Text, 40), timeSince(m.At))
	}
	w.Flush()
}

func broadcast(url, token, msg string) {
	body, _ := json.Marshal(map[string]string{
		"nick": "commander",
		"text": msg,
	})
	req, _ := http.NewRequest("POST", url+"/v1/channels/general/messages", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("broadcast failed: %s", resp.Status)
	}
	fmt.Printf("Broadcast sent: %s\n", msg)
}

func fetchAgents(url, token string) []Agent {
	req, _ := http.NewRequest("GET", url+"/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	var data struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("fetchAgents: decode: %v", err)
	}
	return data.Agents
}

func fetchMessages(url, token, channel string) []Message {
	req, _ := http.NewRequest("GET", url+"/v1/channels/"+channel+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	var data struct {
		Messages []struct {
			Nick string `json:"nick"`
			Text string `json:"text"`
			At   string `json:"at"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("fetchMessages: decode: %v", err)
	}

	var out []Message
	for _, m := range data.Messages {
		at, _ := time.Parse(time.RFC3339Nano, m.At)
		out = append(out, Message{Nick: m.Nick, Text: m.Text, At: at})
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func timeSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	return d.String() + " ago"
}
