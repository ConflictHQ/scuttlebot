// scuttlectl is the CLI for observing and managing a scuttlebot instance.
//
// Usage:
//
//	scuttlectl [--url URL] [--token TOKEN] [--json] <command> [args]
//
// Environment variables:
//
//	SCUTTLEBOT_URL    API base URL (default: http://localhost:8080)
//	SCUTTLEBOT_TOKEN  API bearer token
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/conflicthq/scuttlebot/cmd/scuttlectl/internal/apiclient"
)

var version = "dev"

func main() {
	// Global flags.
	urlFlag := flag.String("url", envOr("SCUTTLEBOT_URL", "http://localhost:8080"), "scuttlebot API base URL")
	tokenFlag := flag.String("token", os.Getenv("SCUTTLEBOT_TOKEN"), "API bearer token")
	jsonFlag := flag.Bool("json", false, "output raw JSON")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	if *tokenFlag == "" {
		fmt.Fprintln(os.Stderr, "error: API token required (set SCUTTLEBOT_TOKEN or use --token)")
		os.Exit(1)
	}

	api := apiclient.New(*urlFlag, *tokenFlag)

	switch args[0] {
	case "status":
		cmdStatus(api, *jsonFlag)
	case "agents", "agent":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl %s <subcommand>\n", args[0])
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdAgentList(api, *jsonFlag)
		case "get":
			requireArgs(args, 3, "scuttlectl agent get <nick>")
			cmdAgentGet(api, args[2], *jsonFlag)
		case "register":
			requireArgs(args, 3, "scuttlectl agent register <nick> [--type worker] [--channels #a,#b]")
			cmdAgentRegister(api, args[2:], *jsonFlag)
		case "revoke":
			requireArgs(args, 3, "scuttlectl agent revoke <nick>")
			cmdAgentRevoke(api, args[2])
		case "rotate":
			requireArgs(args, 3, "scuttlectl agent rotate <nick>")
			cmdAgentRotate(api, args[2], *jsonFlag)
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[1])
			os.Exit(1)
		}
	case "channels":
		if len(args) < 2 || args[1] == "list" {
			fmt.Fprintln(os.Stderr, "channels list: not yet implemented (requires #12 discovery)")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "unknown subcommand: channels %s\n", args[1])
		os.Exit(1)
	case "logs":
		fmt.Fprintln(os.Stderr, "logs tail: not yet implemented (requires scribe HTTP endpoint)")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(1)
	}
}

func cmdStatus(api *apiclient.Client, asJSON bool) {
	raw, err := api.Status()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var s struct {
		Status  string `json:"status"`
		Uptime  string `json:"uptime"`
		Agents  int    `json:"agents"`
		Started string `json:"started"`
	}
	must(json.Unmarshal(raw, &s))

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "status\t%s\n", s.Status)
	fmt.Fprintf(tw, "uptime\t%s\n", s.Uptime)
	fmt.Fprintf(tw, "agents\t%d\n", s.Agents)
	fmt.Fprintf(tw, "started\t%s\n", s.Started)
	tw.Flush()
}

func cmdAgentList(api *apiclient.Client, asJSON bool) {
	raw, err := api.ListAgents()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var body struct {
		Agents []struct {
			Nick     string   `json:"nick"`
			Type     string   `json:"type"`
			Channels []string `json:"channels"`
			Revoked  bool     `json:"revoked"`
		} `json:"agents"`
	}
	must(json.Unmarshal(raw, &body))

	if len(body.Agents) == 0 {
		fmt.Println("no agents registered")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NICK\tTYPE\tCHANNELS\tSTATUS")
	for _, a := range body.Agents {
		status := "active"
		if a.Revoked {
			status = "revoked"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Nick, a.Type, strings.Join(a.Channels, ","), status)
	}
	tw.Flush()
}

func cmdAgentGet(api *apiclient.Client, nick string, asJSON bool) {
	raw, err := api.GetAgent(nick)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var a struct {
		Nick     string   `json:"nick"`
		Type     string   `json:"type"`
		Channels []string `json:"channels"`
		Revoked  bool     `json:"revoked"`
	}
	must(json.Unmarshal(raw, &a))

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	status := "active"
	if a.Revoked {
		status = "revoked"
	}
	fmt.Fprintf(tw, "nick\t%s\n", a.Nick)
	fmt.Fprintf(tw, "type\t%s\n", a.Type)
	fmt.Fprintf(tw, "channels\t%s\n", strings.Join(a.Channels, ", "))
	fmt.Fprintf(tw, "status\t%s\n", status)
	tw.Flush()
}

func cmdAgentRegister(api *apiclient.Client, args []string, asJSON bool) {
	nick := args[0]
	agentType := "worker"
	var channels []string

	// Parse optional --type and --channels from remaining args.
	fs := flag.NewFlagSet("agent register", flag.ExitOnError)
	typeFlag := fs.String("type", "worker", "agent type (worker, orchestrator, observer)")
	channelsFlag := fs.String("channels", "", "comma-separated list of channels to join")
	_ = fs.Parse(args[1:])
	agentType = *typeFlag
	if *channelsFlag != "" {
		for _, ch := range strings.Split(*channelsFlag, ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				channels = append(channels, ch)
			}
		}
	}

	raw, err := api.RegisterAgent(nick, agentType, channels)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var body struct {
		Credentials struct {
			Nick     string `json:"nick"`
			Password string `json:"password"`
			Server   string `json:"server"`
		} `json:"credentials"`
		Payload struct {
			Token     string `json:"token"`
			Signature string `json:"signature"`
		} `json:"payload"`
	}
	must(json.Unmarshal(raw, &body))

	fmt.Printf("Agent registered: %s\n\n", nick)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREDENTIAL\tVALUE")
	fmt.Fprintf(tw, "nick\t%s\n", body.Credentials.Nick)
	fmt.Fprintf(tw, "password\t%s\n", body.Credentials.Password)
	fmt.Fprintf(tw, "server\t%s\n", body.Credentials.Server)
	tw.Flush()
	fmt.Println("\nStore these credentials — the password will not be shown again.")
}

func cmdAgentRevoke(api *apiclient.Client, nick string) {
	die(api.RevokeAgent(nick))
	fmt.Printf("Agent revoked: %s\n", nick)
}

func cmdAgentRotate(api *apiclient.Client, nick string, asJSON bool) {
	raw, err := api.RotateAgent(nick)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var creds struct {
		Nick     string `json:"nick"`
		Password string `json:"password"`
		Server   string `json:"server"`
	}
	must(json.Unmarshal(raw, &creds))

	fmt.Printf("Credentials rotated for: %s\n\n", nick)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREDENTIAL\tVALUE")
	fmt.Fprintf(tw, "nick\t%s\n", creds.Nick)
	fmt.Fprintf(tw, "password\t%s\n", creds.Password)
	fmt.Fprintf(tw, "server\t%s\n", creds.Server)
	tw.Flush()
	fmt.Println("\nStore this password — it will not be shown again.")
}

func usage() {
	fmt.Fprintf(os.Stderr, `scuttlectl %s — scuttlebot management CLI

Usage:
  scuttlectl [flags] <command> [subcommand] [args]

Global flags:
  --url     API base URL (default: $SCUTTLEBOT_URL or http://localhost:8080)
  --token   API bearer token (default: $SCUTTLEBOT_TOKEN)
  --json    output raw JSON
  --version print version and exit

Commands:
  status                        daemon + ergo health
  agents list                   list all registered agents
  agent get <nick>              get a single agent
  agent register <nick>         register a new agent, print credentials
    [--type worker|orchestrator|observer]
    [--channels #a,#b,#c]
  agent revoke <nick>           revoke agent credentials
  agent rotate <nick>           rotate agent password
  channels list                 list provisioned channels (requires #12)
  logs tail                     tail scribe log (coming soon)
`, version)
}

func printJSON(raw json.RawMessage) {
	var buf []byte
	buf, _ = json.MarshalIndent(raw, "", "  ")
	fmt.Println(string(buf))
}

func requireArgs(args []string, n int, usage string) {
	if len(args) < n {
		fmt.Fprintf(os.Stderr, "usage: %s\n", usage)
		os.Exit(1)
	}
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "internal error:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
