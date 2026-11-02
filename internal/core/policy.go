package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
)

// Actions the permission broker understands.
const (
	ActionShell   = "shell"   // target: the command line
	ActionWrite   = "write"   // target: a file path
	ActionNetwork = "network" // target: a host name
	ActionSecret  = "secret"  // target: a secret's name
	ActionTool    = "tool"    // target: a tool name, for anything else
)

// Effects, from weakest to strongest.
const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

var effectRank = map[string]int{Allow: 0, Ask: 1, Deny: 2}

// Rule matches an action and a target glob. In Match, "*" matches any run of
// characters, including spaces and slashes.
type Rule struct {
	Action string `json:"action"`
	Match  string `json:"match"`
	Effect string `json:"effect"`
}

// Policy is an agent's permissions. When several rules match, the strictest
// effect wins. When none match, Default applies.
type Policy struct {
	Default string `json:"default"`
	Rules   []Rule `json:"rules"`
}

// DefaultPolicy is what a new agent gets: free to work inside its task, but
// pushing, privilege escalation and secrets go through you.
func DefaultPolicy() Policy {
	p := Policy{
		Default: Allow,
		Rules: []Rule{
			{ActionShell, "git push --force*", Deny},
			{ActionShell, "git push -f*", Deny},
			{ActionShell, "git push*", Ask},
			{ActionShell, "sudo *", Ask},
			{ActionShell, "curl * | sh*", Deny},
			{ActionShell, "curl * | bash*", Deny},
			{ActionShell, "rm -rf /", Deny},
			{ActionShell, "rm -rf ~*", Deny},
			{ActionSecret, "*", Ask},
		},
	}
	p.Rules = append(p.Rules, PlatformRules()...)
	return p
}

// PlatformRules send the commands that change production or delete things on
// GitHub, Vercel and Cloudflare to your queue. Reading logs, listing
// resources and preview deploys stay free. A leading "*" also catches the
// command after "cd dir &&" or "npx".
func PlatformRules() []Rule {
	ask := func(m ...string) []Rule {
		var r []Rule
		for _, x := range m {
			r = append(r, Rule{ActionShell, x, Ask})
		}
		return r
	}
	var r []Rule
	// GitHub: merges, deletions, releases, secrets, visibility, raw writes.
	r = append(r, ask(
		"*git push*", // also after "git commit ... &&"
		"*gh pr merge*",
		"*gh repo delete*",
		"*gh repo edit*--visibility*",
		"*gh repo archive*",
		"*gh release create*",
		"*gh release delete*",
		"*gh secret set*",
		"*gh secret delete*",
		"*gh variable delete*",
		"*gh api*-X DELETE*",
		"*gh api*--method DELETE*",
	)...)
	// Vercel: production, promotion, rollback, deletions, secrets, DNS.
	r = append(r, ask(
		"*vercel*--prod*",
		"*vercel promote*",
		"*vercel rollback*",
		"*vercel remove*",
		"*vercel rm *",
		"*vercel project rm*",
		"*vercel project remove*",
		"*vercel env rm*",
		"*vercel env remove*",
		"*vercel env add*production*",
		"*vercel domains rm*",
		"*vercel domains remove*",
		"*vercel dns rm*",
		"*vercel dns remove*",
		"*vercel alias rm*",
		"*vercel alias set*",
		"*vercel certs rm*",
	)...)
	// Cloudflare: Worker deploys, rollbacks, deletions, secrets, remote SQL.
	r = append(r, ask(
		"*wrangler deploy*",
		"*wrangler publish*",
		"*wrangler rollback*",
		"*wrangler versions deploy*",
		"*wrangler delete*",
		"*wrangler * delete*",
		"*wrangler secret put*",
		"*wrangler secret bulk*",
		"*wrangler pages deploy*--branch*main*",
		"*wrangler pages deploy*--branch*production*",
		"*wrangler d1 execute*--remote*",
		"*wrangler d1 migrations apply*--remote*",
	)...)
	// Raw API writes with curl to any of them.
	r = append(r, ask(
		"*curl*-X DELETE*",
		"*curl*--request DELETE*",
	)...)
	return r
}

// WithDefaults returns p plus the default rules it does not have yet, so an
// agent made by an older Sheaf picks up new protections without losing your
// own rules. It also returns how many rules it added.
func (p Policy) WithDefaults() (Policy, int) {
	have := map[Rule]bool{}
	for _, r := range p.Rules {
		have[r] = true
	}
	out := Policy{Default: p.Default, Rules: append([]Rule(nil), p.Rules...)}
	added := 0
	for _, r := range DefaultPolicy().Rules {
		if !have[r] {
			out.Rules = append(out.Rules, r)
			have[r] = true
			added++
		}
	}
	return out, added
}

// Validate checks the policy's effects and actions.
func (p Policy) Validate() error {
	if _, ok := effectRank[p.Default]; !ok {
		return invalidf("policy default %q must be allow, ask or deny", p.Default)
	}
	for _, r := range p.Rules {
		if _, ok := effectRank[r.Effect]; !ok {
			return invalidf("rule effect %q must be allow, ask or deny", r.Effect)
		}
		switch r.Action {
		case ActionShell, ActionWrite, ActionNetwork, ActionSecret, ActionTool, "*":
		default:
			return invalidf("unknown rule action %q", r.Action)
		}
	}
	return nil
}

// Evaluate returns the effect for an action and the rule that decided it, if
// any.
func (p Policy) Evaluate(action, target string) (string, *Rule) {
	var best *Rule
	for i := range p.Rules {
		r := &p.Rules[i]
		if (r.Action != action && r.Action != "*") || !glob(r.Match, target) {
			continue
		}
		if best == nil || effectRank[r.Effect] > effectRank[best.Effect] {
			best = r
		}
	}
	if best != nil {
		return best.Effect, best
	}
	return p.Default, nil
}

// glob matches s against a pattern where "*" matches anything.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// Decision is the broker's answer to a permission check.
type Decision struct {
	Effect string `json:"effect"`
	Reason string `json:"reason"`
}

// CheckRequest asks whether the calling agent may do something.
type CheckRequest struct {
	Action string `json:"action"`
	Target string `json:"target"`
	// Cwd resolves relative paths for write checks.
	Cwd string `json:"cwd,omitempty"`
}

// Check evaluates the caller's policy. Writes outside the agent's current
// task worktree are raised to at least "ask". Every check is logged.
func (s *Service) Check(ctx context.Context, c Caller, req CheckRequest) (Decision, error) {
	if c.IsHuman() {
		return Decision{Allow, "the owner is not restricted"}, nil
	}
	a, err := s.getAgent(ctx, s.db, c.Name)
	if err != nil {
		return Decision{}, err
	}
	effect, rule := a.Policy.Evaluate(req.Action, req.Target)
	reason := "policy default"
	if rule != nil {
		reason = "rule " + rule.Action + " " + rule.Match
	}
	if req.Action == ActionWrite && effectRank[effect] < effectRank[Ask] {
		if t, err := s.currentTask(ctx, c.Name); err == nil && t.Worktree != "" {
			p := req.Target
			if !filepath.IsAbs(p) && req.Cwd != "" {
				p = filepath.Join(req.Cwd, p)
			}
			p = filepath.Clean(p)
			if p != t.Worktree && !strings.HasPrefix(p, t.Worktree+string(filepath.Separator)) {
				effect, reason = Ask, "write outside task "+t.ID+" worktree"
			}
		}
	}
	d := Decision{effect, reason}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		return s.emit(tx, c.Name, "policy.checked", req.Action, map[string]string{
			"target": req.Target, "effect": d.Effect, "reason": d.Reason,
		})
	})
	return d, err
}

// SetPolicy replaces an agent's policy.
func (s *Service) SetPolicy(ctx context.Context, c Caller, agent string, p Policy) error {
	if err := requireOwnerToken(c); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := jsonString(p)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE agents SET policy = ?, updated_at = ? WHERE name = ?`, b, s.now(), agent)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return s.emit(tx, c.Name, "agent.policy_set", agent, p)
	})
}

// currentTask is the agent's most recently touched open task.
func (s *Service) currentTask(ctx context.Context, agent string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE owner = ? AND status IN ('open', 'working', 'blocked') ORDER BY updated_at DESC LIMIT 1`, agent)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}
