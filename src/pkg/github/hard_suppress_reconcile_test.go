package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v72/github"
)

type hardSuppressFixture struct {
	number   int
	label    string
	events   []map[string]any
	comments []wireComment
	timeline []map[string]any
}

func labelEvent(kind, actor, at, label string) map[string]any {
	return map[string]any{
		"event": kind, "created_at": at,
		"actor": map[string]string{"login": actor},
		"label": map[string]string{"name": label},
	}
}

// crossRefMergedPR is a merged (or, with mergedAt "", open) hive-authored PR
// in the fixture repo cross-referencing the issue.
func crossRefMergedPR(number int, mergedAt string) map[string]any {
	return crossRefMergedPRFrom("testorg/console", testHiveAppBotLogin, number, mergedAt)
}

func crossRefMergedPRFrom(repoFullName, author string, number int, mergedAt string) map[string]any {
	pr := map[string]any{"url": "https://api.github.com/repos/" + repoFullName + "/pulls/" + fmt.Sprint(number)}
	if mergedAt != "" {
		pr["merged_at"] = mergedAt
	}
	return map[string]any{
		"event":      "cross-referenced",
		"created_at": "2026-10-14T19:00:00Z",
		"source": map[string]any{
			"type": "issue",
			"issue": map[string]any{
				"number":         number,
				"pull_request":   pr,
				"user":           map[string]string{"login": author},
				"repository_url": "https://api.github.com/repos/" + repoFullName,
				"repository":     map[string]any{"full_name": repoFullName},
			},
		},
	}
}

func runHardSuppressFixture(t *testing.T, appBot string, fx hardSuppressFixture) (*HardSuppressReconcileResult, []string, []string) {
	t.Helper()
	org, repo := "testorg", "console"
	var mu sync.Mutex
	var removed, posted []string
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/issues", org, repo), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") != fx.label {
			w.Write([]byte(`[]`))
			return
		}
		w.Write(mustMarshal(t, []wireIssue{{Number: fx.number, Title: "parked", CreatedAt: "2026-10-14T17:00:00Z", User: wireUser{"maintainer"}, Labels: []wireLabel{{Name: fx.label}}}}))
	})
	base := fmt.Sprintf("/repos/%s/%s/issues/%d", org, repo, fx.number)
	mux.HandleFunc(base+"/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(mustMarshal(t, fx.events))
	})
	mux.HandleFunc(base+"/timeline", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(mustMarshal(t, fx.timeline))
	})
	mux.HandleFunc(base+"/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			mu.Lock()
			posted = append(posted, "comment")
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1}`))
			return
		}
		w.Write(mustMarshal(t, fx.comments))
	})
	mux.HandleFunc(base+"/labels/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			removed = append(removed, strings.TrimPrefix(r.URL.Path, base+"/labels/"))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	c := newTestClient(t, server, org, []string{repo})
	c.SetAppBotLogin(appBot)
	res, err := c.ReconcileHiveHardSuppress(context.Background(), HardSuppressReconcileConfig{Repos: []string{repo}, AIAuthor: "hive-ai"})
	if err != nil {
		t.Fatalf("ReconcileHiveHardSuppress: %v", err)
	}
	return res, removed, posted
}

func TestReconcileHiveHardSuppress(t *testing.T) {
	shaNotice := wireComment{User: wireUser{testHiveAppBotLogin}, Body: shaHoldComment, CreatedAt: "2026-10-13T12:00:01Z"}
	cases := []struct {
		name        string
		appBot      string
		fx          hardSuppressFixture
		wantRemoved []string
		wantLifted  int
	}{
		{
			name:   "sha hold lifted after referencing PR merged (#11732)",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 11732, label: "hold",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-13T12:00:00Z", "hold")},
				comments: []wireComment{shaNotice},
				timeline: []map[string]any{crossRefMergedPR(11736, "2026-10-14T20:00:00Z")},
			},
			wantRemoved: []string{"hold"},
			wantLifted:  1,
		},
		{
			name:   "app-applied blocked lifted after PRs merged since (#11854)",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 11854, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPR(11990, "2026-10-15T01:00:00Z")},
			},
			wantRemoved: []string{"blocked"},
			wantLifted:  1,
		},
		{
			name:   "human-applied blocked is never lifted",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", "alice", "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPR(2, "2026-10-15T01:00:00Z")},
			},
		},
		{
			name:   "human re-applied after hive is never lifted",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events: []map[string]any{
					labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:00:00Z", "blocked"),
					labelEvent("unlabeled", "alice", "2026-10-14T18:30:00Z", "blocked"),
					labelEvent("labeled", "alice", "2026-10-14T19:00:00Z", "blocked"),
				},
				timeline: []map[string]any{crossRefMergedPR(2, "2026-10-15T01:00:00Z")},
			},
		},
		{
			name:   "app hold without the sha-hold notice is not the sha sweep's",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "hold",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-13T12:00:00Z", "hold")},
				timeline: []map[string]any{crossRefMergedPR(2, "2026-10-14T20:00:00Z")},
			},
		},
		{
			name:   "PR merged before the park does not lift",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPR(2, "2026-10-14T10:00:00Z")},
			},
		},
		{
			name:   "open referencing PR does not lift",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPR(2, "")},
			},
		},
		{
			name:   "AI-author PR in the configured repo lifts",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPRFrom("testorg/console", "hive-ai", 2, "2026-10-15T01:00:00Z")},
			},
			wantRemoved: []string{"blocked"},
			wantLifted:  1,
		},
		{
			name:   "merged PR from an outside author does not lift",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPRFrom("testorg/console", "mallory", 2, "2026-10-15T01:00:00Z")},
			},
		},
		{
			name:   "merged PR in a foreign repo does not lift",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPRFrom("mallory/fork", testHiveAppBotLogin, 2, "2026-10-15T01:00:00Z")},
			},
		},
		{
			name:   "merged PR with no repo or author data fails closed",
			appBot: testHiveAppBotLogin,
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events: []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{{
					"event": "cross-referenced", "created_at": "2026-10-14T19:00:00Z",
					"source": map[string]any{"type": "issue", "issue": map[string]any{
						"number": 2, "pull_request": map[string]any{"merged_at": "2026-10-15T01:00:00Z"},
					}},
				}},
			},
		},
		{
			name:   "no app bot login fails closed",
			appBot: "",
			fx: hardSuppressFixture{
				number: 1, label: "blocked",
				events:   []map[string]any{labelEvent("labeled", testHiveAppBotLogin, "2026-10-14T18:12:08Z", "blocked")},
				timeline: []map[string]any{crossRefMergedPR(2, "2026-10-15T01:00:00Z")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, removed, posted := runHardSuppressFixture(t, tc.appBot, tc.fx)
			if res.Lifted != tc.wantLifted {
				t.Errorf("Lifted = %d, want %d (result %+v)", res.Lifted, tc.wantLifted, res)
			}
			if fmt.Sprint(removed) != fmt.Sprint(tc.wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, tc.wantRemoved)
			}
			if len(posted) != tc.wantLifted {
				t.Errorf("notes posted = %d, want %d", len(posted), tc.wantLifted)
			}
		})
	}
}

func TestCrossRefRepoFullNameFallsBackToRepositoryURL(t *testing.T) {
	issue := &gh.Issue{RepositoryURL: gh.Ptr("https://api.github.com/repos/testorg/console")}
	if got := crossRefRepoFullName(issue); got != "testorg/console" {
		t.Errorf("crossRefRepoFullName = %q, want testorg/console", got)
	}
	if got := crossRefRepoFullName(&gh.Issue{}); got != "" {
		t.Errorf("crossRefRepoFullName(empty) = %q, want \"\"", got)
	}
}

// The SHA-hold sweep's hard path must not re-park an issue the reconcile just
// lifted: the lift note outranks the still-missing SHA until a newer SHA-hold
// notice is posted (review of hivecommons/hive#12261).
func TestEnforceSHAHold_HonoursHardSuppressLiftNote(t *testing.T) {
	cases := []struct {
		name     string
		comments []wireComment
		wantHeld int
	}{
		{
			name: "lift note newer than the sha-hold notice skips the re-hold",
			comments: []wireComment{
				{User: wireUser{testHiveAppBotLogin}, Body: shaHoldComment, CreatedAt: "2026-10-13T12:00:01Z"},
				{User: wireUser{testHiveAppBotLogin}, Body: hardSuppressLiftedNote("hold", time.Time{}, "#2"), CreatedAt: "2026-10-15T01:00:00Z"},
			},
		},
		{
			name: "sha-hold notice newer than the lift note holds again",
			comments: []wireComment{
				{User: wireUser{testHiveAppBotLogin}, Body: hardSuppressLiftedNote("hold", time.Time{}, "#2"), CreatedAt: "2026-10-13T12:00:01Z"},
				{User: wireUser{testHiveAppBotLogin}, Body: shaHoldComment, CreatedAt: "2026-10-15T01:00:00Z"},
			},
			wantHeld: 1,
		},
		{
			name: "lift note from a non-app account is ignored",
			comments: []wireComment{
				{User: wireUser{"mallory"}, Body: hardSuppressLiftedNote("hold", time.Time{}, "#2"), CreatedAt: "2026-10-15T01:00:00Z"},
			},
			wantHeld: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			org, repo := "testorg", "console"
			var mu sync.Mutex
			var holds, notices int
			mux := http.NewServeMux()
			mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/issues", org, repo), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(mustMarshal(t, []wireIssue{{Number: 100, Title: "Bug: crash", User: wireUser{"external-user"}, Labels: []wireLabel{{Name: "kind/bug"}}, CreatedAt: "2026-10-13T11:00:00Z"}}))
			})
			mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/issues/100/labels", org, repo), func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					mu.Lock()
					holds++
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`[{"name":"hold"}]`))
			})
			mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/issues/100/comments", org, repo), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					mu.Lock()
					notices++
					mu.Unlock()
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"id":1}`))
					return
				}
				w.Write(mustMarshal(t, tc.comments))
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			c := newTestClient(t, server, org, []string{repo})
			c.SetAppBotLogin(testHiveAppBotLogin)
			result, err := c.EnforceSHAHold(context.Background(), SHAHoldConfig{PrimaryRepo: repo, AIAuthor: "hive-bot"})
			if err != nil {
				t.Fatalf("EnforceSHAHold: %v", err)
			}
			if result.Held != tc.wantHeld || holds != tc.wantHeld || notices != tc.wantHeld {
				t.Errorf("Held=%d label adds=%d notices=%d, want all %d", result.Held, holds, notices, tc.wantHeld)
			}
		})
	}
}

func TestHardSuppressLiftedNoteCarriesMarker(t *testing.T) {
	at, err := time.Parse(time.RFC3339, "2026-10-14T18:12:08Z")
	if err != nil {
		t.Fatal(err)
	}
	note := hardSuppressLiftedNote("blocked", at, "#11990")
	for _, want := range []string{hardSuppressLiftedMarker, "`blocked`", "#11990", "2026-10-14T18:12:08Z"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q: %s", want, note)
		}
	}
}
