// Package shepherd implements a goal-directed agent coordination bot.
//
// Shepherd monitors channels, tracks agent activity, assigns work from
// configured goal sources, checks in on progress, and reports status.
// It uses an LLM to reason about priorities, detect blockers, and
// generate summaries.
//
// Commands (via DM or channel mention):
//
//	GOAL <text>           — set a goal for the current channel
//	STATUS                — report progress on current goals
//	ASSIGN <nick> <task>  — manually assign a task to an agent
//	CHECKIN               — trigger a check-in round
//	PLAN                  — generate a work plan from current goals
package shepherd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/cmdparse"
)

const defaultNick = "shepherd"

// LLMProvider calls a language model for reasoning.
type LLMProvider interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// Config controls shepherd's behaviour.
type Config struct {
	IRCAddr  string
	Nick     string
	Password string

	// Channels to join and monitor.
	Channels []string

	// ReportChannel is where status reports go (e.g. "#ops").
	ReportChannel string

	// CheckinInterval is how often to check in on agents. 0 = disabled.
	CheckinInterval time.Duration

	// StatePath is the JSON file shepherd writes goals/assignments/activity
	// to so they survive bot restarts. Empty = disabled (in-memory only).
	StatePath string
}

// Goal is a tracked objective.
type Goal struct {
	ID          string    `json:"id"`
	Channel     string    `json:"channel"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
	Status      string    `json:"status"` // "active", "done", "blocked"
}

// Assignment tracks which agent is working on what.
type Assignment struct {
	Nick       string    `json:"nick"`
	Task       string    `json:"task"`
	Channel    string    `json:"channel"`
	AssignedAt time.Time `json:"assigned_at"`
	LastUpdate time.Time `json:"last_update"`
}

// Bot is the shepherd bot.
type Bot struct {
	cfg    Config
	llm    LLMProvider
	log    *slog.Logger
	client *girc.Client

	mu          sync.Mutex
	goals       map[string][]Goal      // channel → goals
	assignments map[string]*Assignment // nick → assignment
	activity    map[string]time.Time   // nick → last message time
	history     map[string][]string    // channel → recent messages for LLM context
}

// New creates a shepherd bot. If cfg.StatePath is set and the file exists,
// goals/assignments/activity are restored from it (#175).
func New(cfg Config, llm LLMProvider, log *slog.Logger) *Bot {
	if cfg.Nick == "" {
		cfg.Nick = defaultNick
	}
	b := &Bot{
		cfg:         cfg,
		llm:         llm,
		log:         log,
		goals:       make(map[string][]Goal),
		assignments: make(map[string]*Assignment),
		activity:    make(map[string]time.Time),
		history:     make(map[string][]string),
	}
	b.loadState()
	return b
}

// shepherdState is the on-disk snapshot shape; intentionally a sibling type
// so adding new fields to Bot doesn't change the JSON format unintentionally.
type shepherdState struct {
	Goals       map[string][]Goal      `json:"goals"`
	Assignments map[string]*Assignment `json:"assignments"`
	Activity    map[string]time.Time   `json:"activity"`
}

// loadState reads the JSON file at cfg.StatePath and merges into the bot's
// in-memory maps. Missing or unreadable file is a soft fallback to empty
// state — shepherd starts fresh rather than failing.
func (b *Bot) loadState() {
	if b.cfg.StatePath == "" {
		return
	}
	data, err := os.ReadFile(b.cfg.StatePath)
	if err != nil {
		return
	}
	var s shepherdState
	if err := json.Unmarshal(data, &s); err != nil {
		if b.log != nil {
			b.log.Warn("shepherd: state load: invalid JSON, starting fresh", "err", err)
		}
		return
	}
	if s.Goals != nil {
		b.goals = s.Goals
	}
	if s.Assignments != nil {
		b.assignments = s.Assignments
	}
	if s.Activity != nil {
		b.activity = s.Activity
	}
	if b.log != nil {
		b.log.Info("shepherd: state restored", "path", b.cfg.StatePath, "goal_channels", len(b.goals), "assignments", len(b.assignments))
	}
}

// saveState snapshots the bot's mutable maps to disk. Atomic via tmp-rename
// so a crash mid-write doesn't corrupt the file. Caller must hold b.mu.
func (b *Bot) saveState() {
	if b.cfg.StatePath == "" {
		return
	}
	s := shepherdState{Goals: b.goals, Assignments: b.assignments, Activity: b.activity}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.cfg.StatePath), 0o755); err != nil {
		if b.log != nil {
			b.log.Warn("shepherd: state save: mkdir", "err", err)
		}
		return
	}
	tmp := b.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		if b.log != nil {
			b.log.Warn("shepherd: state save: write", "err", err)
		}
		return
	}
	_ = os.Rename(tmp, b.cfg.StatePath)
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return b.cfg.Nick }

// Start connects to IRC and begins shepherding. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.cfg.IRCAddr)
	if err != nil {
		return fmt.Errorf("shepherd: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.cfg.Nick,
		User:        b.cfg.Nick,
		Name:        "scuttlebot shepherd",
		SASL:        &girc.SASLPlain{User: b.cfg.Nick, Pass: b.cfg.Password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
	})

	router := cmdparse.NewRouter(b.cfg.Nick)
	router.Register(cmdparse.Command{
		Name:        "goal",
		Usage:       "GOAL <description>",
		Description: "set a goal for the current channel",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handleGoal(cmdCtx.Channel, cmdCtx.Nick, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "report progress on current goals",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handleStatus(ctx, cmdCtx.Channel)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "assign",
		Usage:       "ASSIGN <nick> <task>",
		Description: "manually assign a task to an agent",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handleAssign(cmdCtx.Channel, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "checkin",
		Usage:       "CHECKIN",
		Description: "trigger a check-in round with all assigned agents",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			b.runCheckin(c)
			return "check-in round started"
		},
	})
	router.Register(cmdparse.Command{
		Name:        "plan",
		Usage:       "PLAN",
		Description: "generate a work plan from current goals using LLM",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handlePlan(ctx, cmdCtx.Channel)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "goals",
		Usage:       "GOALS",
		Description: "list all active goals for this channel",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handleListGoals(cmdCtx.Channel)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "done",
		Usage:       "DONE <goal-id>",
		Description: "mark a goal as completed",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.handleDone(cmdCtx.Channel, args)
		},
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.cfg.Channels {
			cl.Cmd.Join(ch)
		}
		// Request voice on all channels after a short delay (let JOINs complete).
		go func() {
			time.Sleep(3 * time.Second)
			for _, ch := range b.cfg.Channels {
				cl.Cmd.Message("ChanServ", "VOICE "+ch)
			}
			if b.cfg.ReportChannel != "" {
				cl.Cmd.Message("ChanServ", "VOICE "+b.cfg.ReportChannel)
			}
		}()
		if b.cfg.ReportChannel != "" {
			cl.Cmd.Join(b.cfg.ReportChannel)
		}
		if b.log != nil {
			b.log.Info("shepherd connected", "channels", b.cfg.Channels)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	// Request +v on every channel we join — including ones added at runtime
	// via INVITE/manager fan-out — not just the static initial set. Without
	// this hook, shepherd was muted on any channel provisioned after its
	// startup (#172).
	c.Handlers.AddBg(girc.JOIN, func(cl *girc.Client, e girc.Event) {
		if e.Source == nil || e.Source.Name != cl.GetNick() {
			return
		}
		if len(e.Params) < 1 {
			return
		}
		ch := e.Params[0]
		if !strings.HasPrefix(ch, "#") {
			return
		}
		cl.Cmd.Message("ChanServ", "VOICE "+ch)
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		nick := e.Source.Name
		target := e.Params[0]
		text := strings.TrimSpace(e.Last())

		// Track activity.
		b.mu.Lock()
		b.activity[nick] = time.Now()
		if strings.HasPrefix(target, "#") {
			hist := b.history[target]
			hist = append(hist, fmt.Sprintf("[%s] %s", nick, text))
			if len(hist) > 100 {
				hist = hist[len(hist)-100:]
			}
			b.history[target] = hist
		}
		b.mu.Unlock()

		// Dispatch commands.
		if reply := router.Dispatch(nick, target, text); reply != nil {
			cl.Cmd.Message(reply.Target, reply.Text)
		}
	})

	b.mu.Lock()
	b.client = c
	b.mu.Unlock()

	// Start periodic check-in if configured.
	if b.cfg.CheckinInterval > 0 {
		go b.checkinLoop(ctx, c)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := c.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		c.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("shepherd: irc: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

// --- Command handlers ---

func (b *Bot) handleGoal(channel, nick, desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "usage: GOAL <description>"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := fmt.Sprintf("G%d", len(b.goals[channel])+1)
	b.goals[channel] = append(b.goals[channel], Goal{
		ID:          id,
		Channel:     channel,
		Description: desc,
		CreatedAt:   time.Now(),
		CreatedBy:   nick,
		Status:      "active",
	})
	b.saveState()
	return fmt.Sprintf("goal %s set: %s", id, desc)
}

func (b *Bot) handleListGoals(channel string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	goals := b.goals[channel]
	if len(goals) == 0 {
		return "no goals set for " + channel
	}
	var lines []string
	for _, g := range goals {
		lines = append(lines, fmt.Sprintf("[%s] %s (%s) — %s", g.ID, g.Description, g.Status, g.CreatedBy))
	}
	return strings.Join(lines, " | ")
}

func (b *Bot) handleDone(channel, goalID string) string {
	goalID = strings.TrimSpace(goalID)
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, g := range b.goals[channel] {
		if strings.EqualFold(g.ID, goalID) {
			b.goals[channel][i].Status = "done"
			b.saveState()
			return fmt.Sprintf("goal %s marked done: %s", g.ID, g.Description)
		}
	}
	return fmt.Sprintf("goal %q not found in %s", goalID, channel)
}

func (b *Bot) handleAssign(channel, args string) string {
	parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
	if len(parts) < 2 {
		return "usage: ASSIGN <nick> <task>"
	}
	nick, task := parts[0], parts[1]
	b.mu.Lock()
	b.assignments[nick] = &Assignment{
		Nick:       nick,
		Task:       task,
		Channel:    channel,
		AssignedAt: time.Now(),
		LastUpdate: time.Now(),
	}
	b.saveState()
	b.mu.Unlock()
	return fmt.Sprintf("assigned %s to %s", nick, task)
}

func (b *Bot) handleStatus(ctx context.Context, channel string) string {
	b.mu.Lock()
	goals := b.goals[channel]
	var active, done int
	for _, g := range goals {
		if g.Status == "done" {
			done++
		} else {
			active++
		}
	}
	hist := b.history[channel]
	var assignments []string
	for _, a := range b.assignments {
		if a.Channel == channel {
			assignments = append(assignments, fmt.Sprintf("%s: %s", a.Nick, a.Task))
		}
	}
	b.mu.Unlock()

	summary := fmt.Sprintf("goals: %d active, %d done", active, done)
	if len(assignments) > 0 {
		summary += " | assignments: " + strings.Join(assignments, ", ")
	}

	// Use LLM for richer summary if available and there's context.
	if b.llm != nil && len(hist) > 5 {
		prompt := fmt.Sprintf("Summarize the current status of work in %s. "+
			"Goals: %d active, %d done. Assignments: %s. "+
			"Recent conversation:\n%s\n\n"+
			"Give a brief status report (2-3 sentences).",
			channel, active, done, strings.Join(assignments, ", "),
			strings.Join(hist[max(0, len(hist)-30):], "\n"))
		if llmSummary, err := b.llm.Summarize(ctx, prompt); err == nil {
			return llmSummary
		}
	}

	return summary
}

func (b *Bot) handlePlan(ctx context.Context, channel string) string {
	b.mu.Lock()
	goals := b.goals[channel]
	var goalDescs []string
	for _, g := range goals {
		if g.Status == "active" {
			goalDescs = append(goalDescs, g.Description)
		}
	}
	hist := b.history[channel]
	b.mu.Unlock()

	if len(goalDescs) == 0 {
		return "no active goals to plan from. Use GOAL <description> to set one."
	}

	if b.llm == nil {
		return "LLM not configured — cannot generate plan"
	}

	prompt := fmt.Sprintf("You are a project coordinator. Generate a brief work plan for these goals:\n\n"+
		"%s\n\nRecent context:\n%s\n\n"+
		"Output a numbered action list (max 5 items). Each item should be concrete and assignable.",
		strings.Join(goalDescs, "\n- "),
		strings.Join(hist[max(0, len(hist)-20):], "\n"))

	plan, err := b.llm.Summarize(ctx, prompt)
	if err != nil {
		return "plan generation failed: " + err.Error()
	}
	return plan
}

// --- Check-in loop ---

func (b *Bot) checkinLoop(ctx context.Context, c *girc.Client) {
	ticker := time.NewTicker(b.cfg.CheckinInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.runCheckin(c)
		}
	}
}

func (b *Bot) runCheckin(c *girc.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	for nick, a := range b.assignments {
		// Check if agent has been active recently.
		lastActive, ok := b.activity[nick]
		if !ok || now.Sub(lastActive) > 10*time.Minute {
			// Agent appears idle — nudge them.
			msg := fmt.Sprintf("[shepherd] %s — checking in: how's progress on %q?", nick, a.Task)
			c.Cmd.Message(a.Channel, msg)
		}
	}
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	return host, port, nil
}
