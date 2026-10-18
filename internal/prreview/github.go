package prreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type GitHub struct {
	Token  string
	Client *http.Client
	API    string
}
type PullRequest struct {
	State string `json:"state"`
	Head  struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

func (g GitHub) request(ctx context.Context, method, path string, body, result any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	api := g.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, method, api+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub %s: HTTP %d", method, res.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(result)
	}
	return nil
}

func (g GitHub) Check(ctx context.Context, repo string, pr int, base, head string) error {
	if !repoPattern.MatchString(repo) || pr <= 0 {
		return fmt.Errorf("invalid repository/PR")
	}
	var p PullRequest
	if err := g.request(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, pr), nil, &p); err != nil {
		return err
	}
	if p.State != "open" || p.Head.SHA != head || p.Base.SHA != base || p.Head.Repo.FullName != repo {
		return fmt.Errorf("PR is stale, closed or from a fork; refusing publication")
	}
	return nil
}

func (g GitHub) Publish(ctx context.Context, r *Report) error {
	if r.Status == "failed" {
		return fmt.Errorf("failed review cannot be published as successful")
	}
	body := Markdown(r)
	if len(body) > 60000 {
		return fmt.Errorf("review exceeds GitHub comment budget")
	}
	prefix := fmt.Sprintf("/repos/%s/issues/%d/comments", r.Repository, r.PR)
	var id int64
	// Only update this workflow's bot comment, never an author's text.
	for page := 1; page <= 20; page++ {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
		}
		if err := g.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", prefix, page), nil, &comments); err != nil {
			return err
		}
		for _, c := range comments {
			if c.User.Login == "github-actions[bot]" && c.User.Type == "Bot" && len(c.Body) >= len(Marker) && c.Body[:len(Marker)] == Marker {
				id = c.ID
				break
			}
		}
		if id != 0 || len(comments) < 100 {
			break
		}
		if page == 20 {
			return fmt.Errorf("comment pagination limit; refusing duplicate publication")
		}
	}
	// The model can be slow: check both pinned SHAs immediately before writing.
	if err := g.Check(ctx, r.Repository, r.PR, r.Input.Base, r.Input.Head); err != nil {
		return err
	}
	method, target := "POST", prefix
	if id != 0 {
		method = "PATCH"
		target = fmt.Sprintf("/repos/%s/issues/comments/%d", r.Repository, id)
	}
	return g.request(ctx, method, target, map[string]string{"body": body}, nil)
}
