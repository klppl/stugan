package previews

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// github summarizes repositories, issues, pull requests and commits via the
// public REST API. Keyless it is limited to 60 requests an hour per IP;
// STUGAN_PREVIEWS_GITHUB_TOKEN (any token, no scopes needed) raises that to
// 5,000. When the limit is hit the provider fails and the chain falls through
// to the generic Open Graph card.
type github struct {
	token string
	api   string // overridable in tests
}

func newGitHub(env Env) *github {
	return &github{token: Setting(env, "github", "token"), api: "https://api.github.com"}
}

func (*github) Name() string { return "github" }

// Top-level paths that are GitHub pages, not owners.
var githubReserved = map[string]bool{
	"about": true, "login": true, "logout": true, "join": true, "settings": true, "features": true,
	"pricing": true, "marketplace": true, "topics": true, "trending": true, "explore": true,
	"sponsors": true, "orgs": true, "organizations": true, "notifications": true, "search": true,
	"pulls": true, "issues": true, "codespaces": true, "apps": true, "enterprise": true,
	"collections": true, "events": true, "readme": true, "security": true, "customer-stories": true,
}

type ghTarget struct {
	owner, repo string
	kind        string // "repo", "issues", "pull", "commit"
	ref         string // number or sha
}

func ghParse(u *url.URL) (ghTarget, bool) {
	if !strings.EqualFold(u.Hostname(), "github.com") && !strings.EqualFold(u.Hostname(), "www.github.com") {
		return ghTarget{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || githubReserved[strings.ToLower(parts[0])] {
		return ghTarget{}, false
	}
	t := ghTarget{owner: parts[0], repo: strings.TrimSuffix(parts[1], ".git"), kind: "repo"}
	switch {
	case len(parts) == 2:
	case len(parts) == 4 && (parts[2] == "issues" || parts[2] == "pull") && isDigits(parts[3]):
		t.kind, t.ref = parts[2], parts[3]
	case len(parts) >= 4 && parts[2] == "pull" && isDigits(parts[3]): // /pull/1/files, /commits…
		t.kind, t.ref = "pull", parts[3]
	case len(parts) == 4 && parts[2] == "commit" && len(parts[3]) >= 7:
		t.kind, t.ref = "commit", parts[3]
	default:
		return ghTarget{}, false // blob/tree/releases/etc: leave to Open Graph
	}
	return t, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (*github) Match(u *url.URL) bool { _, ok := ghParse(u); return ok }

func (g *github) get(ctx context.Context, f *Fetcher, path string, v any) error {
	h := map[string]string{
		"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": apiUA,
	}
	if g.token != "" {
		h["Authorization"] = "Bearer " + g.token
	}
	return f.JSON(ctx, g.api+path, h, v)
}

func (g *github) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	t, _ := ghParse(u)
	base := fmt.Sprintf("/repos/%s/%s", url.PathEscape(t.owner), url.PathEscape(t.repo))
	switch t.kind {
	case "issues":
		return g.issue(ctx, f, t, base)
	case "pull":
		return g.pull(ctx, f, t, base)
	case "commit":
		return g.commit(ctx, f, t, base)
	}
	return g.repo(ctx, f, t, base)
}

func (g *github) repo(ctx context.Context, f *Fetcher, t ghTarget, base string) (*Card, error) {
	var r struct {
		FullName    string `json:"full_name"`
		Description string `json:"description"`
		Language    string `json:"language"`
		Stars       int64  `json:"stargazers_count"`
		Forks       int64  `json:"forks_count"`
		OpenIssues  int64  `json:"open_issues_count"`
		Archived    bool   `json:"archived"`
		PushedAt    string `json:"pushed_at"`
		HTMLURL     string `json:"html_url"`
		License     *struct {
			SPDX string `json:"spdx_id"`
		} `json:"license"`
	}
	if err := g.get(ctx, f, base, &r); err != nil {
		return nil, err
	}
	c := &Card{
		URL: r.HTMLURL, Title: r.FullName, Description: shorten(r.Description, 240), Site: "GitHub",
		Image: fmt.Sprintf("https://opengraph.githubassets.com/1/%s/%s", url.PathEscape(t.owner), url.PathEscape(t.repo)),
	}
	add := detailAdder(c)
	if r.Archived {
		add("archived")
	}
	add(r.Language)
	add(compactCount(r.Stars) + " stars")
	add(compactCount(r.Forks) + " forks")
	if r.License != nil && r.License.SPDX != "" && r.License.SPDX != "NOASSERTION" {
		add(r.License.SPDX)
	}
	add(isoDate(r.PushedAt))
	return c, nil
}

type ghIssue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Comments  int    `json:"comments"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *github) issue(ctx context.Context, f *Fetcher, t ghTarget, base string) (*Card, error) {
	var i ghIssue
	if err := g.get(ctx, f, base+"/issues/"+t.ref, &i); err != nil {
		return nil, err
	}
	c := &Card{
		URL: i.HTMLURL, Title: fmt.Sprintf("#%d %s", i.Number, i.Title),
		Description: shorten(plainText(i.Body), 240), Site: "GitHub · " + t.owner + "/" + t.repo,
	}
	add := detailAdder(c)
	add(i.State)
	add("@" + i.User.Login)
	if i.Comments > 0 {
		add(fmt.Sprintf("%d comments", i.Comments))
	}
	for n, l := range i.Labels {
		if n == 3 {
			break
		}
		add(l.Name)
	}
	add(isoDate(i.CreatedAt))
	return c, nil
}

func (g *github) pull(ctx context.Context, f *Fetcher, t ghTarget, base string) (*Card, error) {
	var p struct {
		ghIssue
		Draft        bool `json:"draft"`
		Merged       bool `json:"merged"`
		Additions    int  `json:"additions"`
		Deletions    int  `json:"deletions"`
		ChangedFiles int  `json:"changed_files"`
	}
	if err := g.get(ctx, f, base+"/pulls/"+t.ref, &p); err != nil {
		return nil, err
	}
	state := p.State
	switch {
	case p.Merged:
		state = "merged"
	case p.Draft:
		state = "draft"
	}
	c := &Card{
		URL: p.HTMLURL, Title: fmt.Sprintf("#%d %s", p.Number, p.Title),
		Description: shorten(plainText(p.Body), 240), Site: "GitHub PR · " + t.owner + "/" + t.repo,
	}
	add := detailAdder(c)
	add(state)
	add("@" + p.User.Login)
	add(fmt.Sprintf("+%d −%d in %d files", p.Additions, p.Deletions, p.ChangedFiles))
	add(isoDate(p.CreatedAt))
	return c, nil
}

func (g *github) commit(ctx context.Context, f *Fetcher, t ghTarget, base string) (*Card, error) {
	var m struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Stats struct {
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
		} `json:"stats"`
	}
	if err := g.get(ctx, f, base+"/commits/"+url.PathEscape(t.ref), &m); err != nil {
		return nil, err
	}
	if m.SHA == "" {
		return nil, errors.New("commit not found")
	}
	first, rest, _ := strings.Cut(m.Commit.Message, "\n")
	c := &Card{URL: m.HTMLURL, Title: first, Description: shorten(plainText(rest), 240), Site: "GitHub commit · " + t.owner + "/" + t.repo}
	add := detailAdder(c)
	add(m.SHA[:7])
	add(m.Commit.Author.Name)
	add(fmt.Sprintf("+%d −%d", m.Stats.Additions, m.Stats.Deletions))
	add(isoDate(m.Commit.Author.Date))
	return c, nil
}

// detailAdder returns a func that appends non-empty strings to c.Details.
func detailAdder(c *Card) func(string) {
	return func(s string) {
		if strings.TrimSpace(s) != "" && s != "@" {
			c.Details = append(c.Details, s)
		}
	}
}

// isoDate renders an RFC 3339 timestamp as YYYY-MM-DD, or "" if unparseable.
func isoDate(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText drops HTML tags (old issues carry raw <pre>/<details>) and
// unescapes entities; shorten() then collapses the whitespace.
func plainText(s string) string {
	return html.UnescapeString(htmlTag.ReplaceAllString(s, " "))
}
