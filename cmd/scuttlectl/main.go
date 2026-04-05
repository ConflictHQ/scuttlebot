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

	switch args[0] {
	case "setup":
		cfgPath := "scuttlebot.yaml"
		if len(args) > 1 {
			cfgPath = args[1]
		}
		cmdSetup(cfgPath)
		return
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
		case "delete":
			requireArgs(args, 3, "scuttlectl agent delete <nick>")
			cmdAgentDelete(api, args[2])
		case "rotate":
			requireArgs(args, 3, "scuttlectl agent rotate <nick>")
			cmdAgentRotate(api, args[2], *jsonFlag)
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[1])
			os.Exit(1)
		}
	case "admin":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl admin <subcommand>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdAdminList(api, *jsonFlag)
		case "add":
			requireArgs(args, 3, "scuttlectl admin add <username>")
			cmdAdminAdd(api, args[2])
		case "remove":
			requireArgs(args, 3, "scuttlectl admin remove <username>")
			cmdAdminRemove(api, args[2])
		case "passwd":
			requireArgs(args, 3, "scuttlectl admin passwd <username>")
			cmdAdminPasswd(api, args[2])
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: admin %s\n", args[1])
			os.Exit(1)
		}
	case "api-key", "api-keys":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl api-key <list|create|revoke>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdAPIKeyList(api, *jsonFlag)
		case "create":
			requireArgs(args, 3, "scuttlectl api-key create --name <name> --scopes <scope1,scope2>")
			cmdAPIKeyCreate(api, args[2:], *jsonFlag)
		case "revoke":
			requireArgs(args, 3, "scuttlectl api-key revoke <id>")
			cmdAPIKeyRevoke(api, args[2])
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: api-key %s\n", args[1])
			os.Exit(1)
		}
	case "channels", "channel":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl channels <list|users <channel>>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdChannelList(api, *jsonFlag)
		case "users":
			requireArgs(args, 3, "scuttlectl channels users <channel>")
			cmdChannelUsers(api, args[2], *jsonFlag)
		case "delete", "rm":
			requireArgs(args, 3, "scuttlectl channels delete <channel>")
			cmdChannelDelete(api, args[2])
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: channels %s\n", args[1])
			os.Exit(1)
		}
	case "backend", "backends":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl backend <list|get|delete|rename> [args]\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdBackendList(api, *jsonFlag)
		case "get":
			requireArgs(args, 3, "scuttlectl backend get <name>")
			cmdBackendGet(api, args[2], *jsonFlag)
		case "delete", "rm":
			requireArgs(args, 3, "scuttlectl backend delete <name>")
			cmdBackendDelete(api, args[2])
		case "rename":
			requireArgs(args, 4, "scuttlectl backend rename <old-name> <new-name>")
			cmdBackendRename(api, args[2], args[3])
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: backend %s\n", args[1])
			os.Exit(1)
		}
	case "topology", "topo":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl topology <list|provision|drop>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list", "show":
			cmdTopologyList(api, *jsonFlag)
		case "provision", "create":
			requireArgs(args, 3, "scuttlectl topology provision #channel")
			cmdTopologyProvision(api, args[2], *jsonFlag)
		case "drop", "rm":
			requireArgs(args, 3, "scuttlectl topology drop #channel")
			cmdTopologyDrop(api, args[2])
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: topology %s\n", args[1])
			os.Exit(1)
		}
	case "config":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl config <show|history>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "show", "get":
			cmdConfigShow(api, *jsonFlag)
		case "history":
			cmdConfigHistory(api, *jsonFlag)
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: config %s\n", args[1])
			os.Exit(1)
		}
	case "bot", "bots":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: scuttlectl bot <list>\n")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			cmdBotList(api, *jsonFlag)
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: bot %s\n", args[1])
			os.Exit(1)
		}
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
	var channels []string

	// Parse optional --type and --channels from remaining args.
	fs := flag.NewFlagSet("agent register", flag.ExitOnError)
	typeFlag := fs.String("type", "worker", "agent type (worker, orchestrator, observer)")
	channelsFlag := fs.String("channels", "", "comma-separated list of channels to join")
	_ = fs.Parse(args[1:])
	agentType := *typeFlag
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

func cmdAdminList(api *apiclient.Client, asJSON bool) {
	raw, err := api.ListAdmins()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var body struct {
		Admins []struct {
			Username string `json:"username"`
			Created  string `json:"created"`
		} `json:"admins"`
	}
	must(json.Unmarshal(raw, &body))

	if len(body.Admins) == 0 {
		fmt.Println("no admin accounts")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tCREATED")
	for _, a := range body.Admins {
		fmt.Fprintf(tw, "%s\t%s\n", a.Username, a.Created)
	}
	tw.Flush()
}

func cmdAdminAdd(api *apiclient.Client, username string) {
	pass := promptPassword()
	_, err := api.AddAdmin(username, pass)
	die(err)
	fmt.Printf("Admin added: %s\n", username)
}

func cmdAdminRemove(api *apiclient.Client, username string) {
	die(api.RemoveAdmin(username))
	fmt.Printf("Admin removed: %s\n", username)
}

func cmdAdminPasswd(api *apiclient.Client, username string) {
	pass := promptPassword()
	die(api.SetAdminPassword(username, pass))
	fmt.Printf("Password updated for: %s\n", username)
}

func promptPassword() string {
	fmt.Fprint(os.Stderr, "password: ")
	var pass string
	_, _ = fmt.Scanln(&pass)
	return pass
}

func cmdAgentRevoke(api *apiclient.Client, nick string) {
	die(api.RevokeAgent(nick))
	fmt.Printf("Agent revoked: %s\n", nick)
}

func cmdAgentDelete(api *apiclient.Client, nick string) {
	die(api.DeleteAgent(nick))
	fmt.Printf("Agent deleted: %s\n", nick)
}

func cmdChannelList(api *apiclient.Client, asJSON bool) {
	raw, err := api.ListChannels()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var body struct {
		Channels []string `json:"channels"`
	}
	must(json.Unmarshal(raw, &body))
	if len(body.Channels) == 0 {
		fmt.Println("no channels")
		return
	}
	for _, ch := range body.Channels {
		fmt.Println(ch)
	}
}

func cmdChannelUsers(api *apiclient.Client, channel string, asJSON bool) {
	raw, err := api.ChannelUsers(channel)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var body struct {
		Users []string `json:"users"`
	}
	must(json.Unmarshal(raw, &body))
	if len(body.Users) == 0 {
		fmt.Printf("no users in %s\n", channel)
		return
	}
	for _, u := range body.Users {
		fmt.Println(u)
	}
}

func cmdChannelDelete(api *apiclient.Client, channel string) {
	die(api.DeleteChannel(channel))
	fmt.Printf("Channel deleted: #%s\n", strings.TrimPrefix(channel, "#"))
}

func cmdBackendList(api *apiclient.Client, asJSON bool) {
	raw, err := api.ListLLMBackends()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var body struct {
		Backends []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"backends"`
	}
	must(json.Unmarshal(raw, &body))
	if len(body.Backends) == 0 {
		fmt.Println("no backends")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROVIDER")
	for _, b := range body.Backends {
		fmt.Fprintf(tw, "%s\t%s\n", b.Name, b.Provider)
	}
	tw.Flush()
}

func cmdBackendGet(api *apiclient.Client, name string, asJSON bool) {
	raw, err := api.GetLLMBackend(name)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var b struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url,omitempty"`
	}
	must(json.Unmarshal(raw, &b))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "name\t%s\n", b.Name)
	fmt.Fprintf(tw, "provider\t%s\n", b.Provider)
	fmt.Fprintf(tw, "model\t%s\n", b.Model)
	if b.BaseURL != "" {
		fmt.Fprintf(tw, "base_url\t%s\n", b.BaseURL)
	}
	tw.Flush()
}

func cmdBackendDelete(api *apiclient.Client, name string) {
	die(api.DeleteLLMBackend(name))
	fmt.Printf("Backend deleted: %s\n", name)
}

func cmdBackendRename(api *apiclient.Client, oldName, newName string) {
	raw, err := api.GetLLMBackend(oldName)
	die(err)

	var cfg map[string]any
	must(json.Unmarshal(raw, &cfg))
	cfg["name"] = newName

	die(api.CreateLLMBackend(cfg))
	die(api.DeleteLLMBackend(oldName))
	fmt.Printf("Backend renamed: %s → %s\n", oldName, newName)
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

// --- api-keys ---

func cmdAPIKeyList(api *apiclient.Client, asJSON bool) {
	raw, err := api.ListAPIKeys()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}

	var keys []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		CreatedAt string   `json:"created_at"`
		LastUsed  *string  `json:"last_used"`
		ExpiresAt *string  `json:"expires_at"`
		Active    bool     `json:"active"`
	}
	must(json.Unmarshal(raw, &keys))

	if len(keys) == 0 {
		fmt.Println("no API keys")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSCOPES\tACTIVE\tLAST USED")
	for _, k := range keys {
		lastUsed := "-"
		if k.LastUsed != nil {
			lastUsed = *k.LastUsed
		}
		status := "yes"
		if !k.Active {
			status = "revoked"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, strings.Join(k.Scopes, ","), status, lastUsed)
	}
	tw.Flush()
}

func cmdAPIKeyCreate(api *apiclient.Client, args []string, asJSON bool) {
	fs := flag.NewFlagSet("api-key create", flag.ExitOnError)
	nameFlag := fs.String("name", "", "key name (required)")
	scopesFlag := fs.String("scopes", "", "comma-separated scopes (required)")
	expiresFlag := fs.String("expires", "", "expiry duration (e.g. 720h for 30 days)")
	_ = fs.Parse(args)

	if *nameFlag == "" || *scopesFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: scuttlectl api-key create --name <name> --scopes <scope1,scope2> [--expires 720h]")
		os.Exit(1)
	}

	scopes := strings.Split(*scopesFlag, ",")
	raw, err := api.CreateAPIKey(*nameFlag, scopes, *expiresFlag)
	die(err)

	if asJSON {
		printJSON(raw)
		return
	}

	var key struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	must(json.Unmarshal(raw, &key))

	fmt.Printf("API key created: %s\n\n", key.Name)
	fmt.Printf("  Token: %s\n\n", key.Token)
	fmt.Println("Store this token — it will not be shown again.")
}

func cmdAPIKeyRevoke(api *apiclient.Client, id string) {
	die(api.RevokeAPIKey(id))
	fmt.Printf("API key revoked: %s\n", id)
}

// --- topology ---

func cmdTopologyList(api *apiclient.Client, asJSON bool) {
	raw, err := api.GetTopology()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var data struct {
		StaticChannels []string `json:"static_channels"`
		Types          []struct {
			Name      string   `json:"name"`
			Prefix    string   `json:"prefix"`
			Autojoin  []string `json:"autojoin"`
			Ephemeral bool     `json:"ephemeral"`
			TTL       int64    `json:"ttl_seconds"`
		} `json:"types"`
	}
	must(json.Unmarshal(raw, &data))

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATIC CHANNELS")
	for _, ch := range data.StaticChannels {
		fmt.Fprintf(tw, "  %s\n", ch)
	}
	if len(data.Types) > 0 {
		fmt.Fprintln(tw, "\nCHANNEL TYPES")
		fmt.Fprintln(tw, "  NAME\tPREFIX\tAUTOJOIN\tEPHEMERAL\tTTL")
		for _, t := range data.Types {
			ttl := "—"
			if t.TTL > 0 {
				ttl = fmt.Sprintf("%dh", t.TTL/3600)
			}
			eph := "no"
			if t.Ephemeral {
				eph = "yes"
			}
			fmt.Fprintf(tw, "  %s\t#%s*\t%s\t%s\t%s\n", t.Name, t.Prefix, strings.Join(t.Autojoin, ","), eph, ttl)
		}
	}
	tw.Flush()
}

func cmdTopologyProvision(api *apiclient.Client, channel string, asJSON bool) {
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	raw, err := api.ProvisionChannel(channel)
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	fmt.Printf("Channel provisioned: %s\n", channel)
}

func cmdTopologyDrop(api *apiclient.Client, channel string) {
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	die(api.DropChannel(channel))
	fmt.Printf("Channel dropped: %s\n", channel)
}

// --- config ---

func cmdConfigShow(api *apiclient.Client, asJSON bool) {
	raw, err := api.GetConfig()
	die(err)
	printJSON(raw) // always JSON — config is a complex nested object
}

func cmdConfigHistory(api *apiclient.Client, asJSON bool) {
	raw, err := api.GetConfigHistory()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var data struct {
		Entries []struct {
			Filename string `json:"filename"`
			At       string `json:"at"`
		} `json:"entries"`
	}
	must(json.Unmarshal(raw, &data))
	if len(data.Entries) == 0 {
		fmt.Println("no config history")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SNAPSHOT\tTIME")
	for _, e := range data.Entries {
		fmt.Fprintf(tw, "%s\t%s\n", e.Filename, e.At)
	}
	tw.Flush()
}

// --- bots ---

func cmdBotList(api *apiclient.Client, asJSON bool) {
	raw, err := api.GetSettings()
	die(err)
	if asJSON {
		printJSON(raw)
		return
	}
	var data struct {
		Policies struct {
			Behaviors []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Nick    string `json:"nick"`
				Enabled bool   `json:"enabled"`
			} `json:"behaviors"`
		} `json:"policies"`
	}
	must(json.Unmarshal(raw, &data))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BOT\tNICK\tSTATUS")
	for _, b := range data.Policies.Behaviors {
		status := "disabled"
		if b.Enabled {
			status = "enabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", b.Name, b.Nick, status)
	}
	tw.Flush()
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
  setup [path]                  interactive wizard — write scuttlebot.yaml (no token needed)
  status                        daemon + ergo health
  agents list                   list all registered agents
  agent get <nick>              get a single agent
  agent register <nick>         register a new agent, print credentials
    [--type worker|orchestrator|observer|operator]
    [--channels #a,#b,#c]
  agent revoke <nick>           revoke agent credentials
  agent delete <nick>           permanently remove agent from registry
  agent rotate <nick>           rotate agent password
  channels list                 list active channels
  channels users <channel>      list users in a channel
  channels delete <channel>     part bridge from channel (closes when empty)
  backend list                  list LLM backends
  backend get <name>            show a single backend
  backend delete <name>         remove a backend
  backend rename <old> <new>    rename a backend
  admin list                    list admin accounts
  admin add <username>          add admin (prompts for password)
  admin remove <username>       remove admin
  admin passwd <username>       change admin password (prompts)
  api-key list                  list API keys
  api-key create --name <name> --scopes <s1,s2> [--expires 720h]
  api-key revoke <id>           revoke an API key
  topology list                 show topology (static channels, types)
  topology provision #channel   provision a new channel via ChanServ
  topology drop #channel        drop a channel
  config show                   dump current config (JSON)
  config history                show config change history
  bot list                      show system bot status
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
