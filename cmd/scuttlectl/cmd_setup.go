package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// cmdSetup runs an interactive wizard that writes scuttlebot.yaml.
// It does not require a running server or an API token.
func cmdSetup(path string) {
	s := newSetupScanner()

	fmt.Println()
	fmt.Println("  scuttlebot setup wizard")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println("  Answers in [brackets] are the default — press Enter to accept.")
	fmt.Println()

	// Check for existing file.
	if _, err := os.Stat(path); err == nil {
		if !s.confirm(fmt.Sprintf("  %s already exists — overwrite?", path), false) {
			fmt.Println("  Aborted.")
			os.Exit(0)
		}
	}

	cfg := buildConfig(s)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error encoding yaml:", err)
		os.Exit(1)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "error writing config:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("  ✓ wrote %s\n", path)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("    ./run.sh start          # start scuttlebot")
	fmt.Println("    ./run.sh token          # print API token")
	fmt.Println("    open http://localhost:8080/ui/")
	fmt.Println()
}

func buildConfig(s *setupScanner) map[string]any {
	cfg := map[string]any{}

	// ── network ──────────────────────────────────────────────────────────────
	printSection("IRC / network")

	networkName := s.ask("  IRC network name", "scuttlebot")
	serverName := s.ask("  IRC server hostname", "irc.scuttlebot.local")
	ircAddr := s.ask("  IRC listen address", "127.0.0.1:6667")
	apiAddr := s.ask("  HTTP API listen address", ":8080")

	cfg["ergo"] = map[string]any{
		"network_name": networkName,
		"server_name":  serverName,
		"irc_addr":     ircAddr,
	}
	cfg["api_addr"] = apiAddr

	// ── TLS ──────────────────────────────────────────────────────────────────
	printSection("TLS / HTTPS  (skip for local/dev)")

	if s.confirm("  Enable Let's Encrypt TLS?", false) {
		domain := s.ask("  Domain name", "")
		email := s.ask("  Email for cert expiry notices", "")
		cfg["tls"] = map[string]any{
			"domain":         domain,
			"email":          email,
			"allow_insecure": true,
		}
	}

	// ── bridge ───────────────────────────────────────────────────────────────
	printSection("web chat bridge")

	channels := s.ask("  Default channels (comma-separated)", "#general")
	chList := splitComma(channels)
	cfg["bridge"] = map[string]any{
		"enabled":     true,
		"channels":    chList,
		"buffer_size": 200,
	}

	// ── LLM backends ─────────────────────────────────────────────────────────
	printSection("LLM backends  (for oracle summarisation)")

	var backends []map[string]any
	for {
		if !s.confirm("  Add an LLM backend?", len(backends) == 0) {
			break
		}
		b := buildBackend(s)
		backends = append(backends, b)
	}
	if len(backends) > 0 {
		cfg["llm"] = map[string]any{"backends": backends}
	}

	// ── logging ───────────────────────────────────────────────────────────────
	printSection("message logging  (scribe bot)")

	if s.confirm("  Enable scribe message logging?", true) {
		logDir := s.ask("  Log directory", "./data/logs/scribe")
		format := s.choice("  Format", []string{"jsonl", "csv", "text"}, "jsonl")
		rotatef := s.choice("  Rotation", []string{"none", "daily", "weekly", "monthly", "size"}, "daily")
		// Stored as scribe bot policy — just print a note, actual policy is in policies.json
		_ = logDir
		_ = format
		_ = rotatef
		fmt.Printf("\n  Note: scribe is enabled via the web UI (settings → system behaviors).\n")
		fmt.Printf("  Set dir=%s format=%s rotation=%s in oracle's behavior config.\n\n", logDir, format, rotatef)
	}

	return cfg
}

func buildBackend(s *setupScanner) map[string]any {
	backends := []string{
		"openai", "anthropic", "gemini", "bedrock", "ollama",
		"openrouter", "groq", "together", "fireworks", "mistral",
		"deepseek", "xai", "cerebras", "litellm", "lmstudio", "vllm", "localai",
	}
	backendType := s.choice("  Backend type", backends, "openai")
	name := s.ask("  Backend name (identifier)", backendType+"-1")

	b := map[string]any{
		"name":    name,
		"backend": backendType,
	}

	switch backendType {
	case "bedrock":
		b["region"] = s.ask("  AWS region", "us-east-1")
		if s.confirm("  Use static AWS credentials? (No = IAM role auto-detected)", false) {
			b["aws_key_id"] = s.ask("  AWS access key ID", "")
			b["aws_secret_key"] = s.ask("  AWS secret access key", "")
		} else {
			fmt.Println("  → credentials will be resolved from env vars or instance/task role")
		}
		b["model"] = s.ask("  Default model", "anthropic.claude-3-5-sonnet-20241022-v2:0")

	case "ollama":
		b["base_url"] = s.ask("  Ollama base URL", "http://localhost:11434")
		b["model"] = s.ask("  Default model", "llama3.2")

	case "anthropic":
		b["api_key"] = s.secret("  API key")
		b["model"] = s.ask("  Default model", "claude-3-5-sonnet-20241022")

	case "gemini":
		b["api_key"] = s.secret("  API key")
		b["model"] = s.ask("  Default model", "gemini-1.5-flash")

	default:
		b["api_key"] = s.secret("  API key")
		b["model"] = s.ask("  Default model", defaultModelFor(backendType))
	}

	if s.confirm("  Add model allow/block regex filters?", false) {
		allow := s.ask("  Allow patterns (comma-separated regex)", "")
		block := s.ask("  Block patterns (comma-separated regex)", "")
		if allow != "" {
			b["allow"] = splitComma(allow)
		}
		if block != "" {
			b["block"] = splitComma(block)
		}
	}

	if s.confirm("  Mark as default backend?", len([]map[string]any{b}) == 0) {
		b["default"] = true
	}

	return b
}

func defaultModelFor(backend string) string {
	defaults := map[string]string{
		"openai":     "gpt-4o-mini",
		"openrouter": "openai/gpt-4o-mini",
		"groq":       "llama-3.3-70b-versatile",
		"together":   "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"fireworks":  "accounts/fireworks/models/llama-v3p3-70b-instruct",
		"mistral":    "mistral-large-latest",
		"deepseek":   "deepseek-chat",
		"xai":        "grok-2",
		"cerebras":   "llama3.3-70b",
		"litellm":    "",
		"lmstudio":   "",
		"vllm":       "",
		"localai":    "",
	}
	if m, ok := defaults[backend]; ok {
		return m
	}
	return ""
}

func printSection(title string) {
	fmt.Printf("\n  ── %s\n\n", title)
}

// setupScanner wraps a line reader with prompt helpers.
type setupScanner struct {
	scanner *bufio.Scanner
}

func newSetupScanner() *setupScanner {
	return &setupScanner{scanner: bufio.NewScanner(os.Stdin)}
}

func (s *setupScanner) readLine() string {
	if s.scanner.Scan() {
		return strings.TrimSpace(s.scanner.Text())
	}
	return ""
}

func (s *setupScanner) ask(prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	v := s.readLine()
	if v == "" {
		return def
	}
	return v
}

func (s *setupScanner) secret(prompt string) string {
	fmt.Printf("%s: ", prompt)
	return s.readLine()
}

func (s *setupScanner) confirm(prompt string, def bool) bool {
	yn := "y/N"
	if def {
		yn = "Y/n"
	}
	fmt.Printf("%s [%s]: ", prompt, yn)
	v := strings.ToLower(strings.TrimSpace(s.readLine()))
	if v == "" {
		return def
	}
	return v == "y" || v == "yes"
}

func (s *setupScanner) choice(prompt string, options []string, def string) string {
	fmt.Printf("%s\n", prompt)
	for i, o := range options {
		marker := "  "
		if o == def {
			marker = "→ "
		}
		fmt.Printf("    %s%d) %s\n", marker, i+1, o)
	}
	fmt.Printf("  choice [%s]: ", def)
	v := strings.TrimSpace(s.readLine())
	if v == "" {
		return def
	}
	// Accept number or name.
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(options) {
		return options[n-1]
	}
	for _, o := range options {
		if strings.EqualFold(o, v) {
			return o
		}
	}
	return def
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
