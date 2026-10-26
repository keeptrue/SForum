package bootstrap

import (
	"context"
	"sync"
	"testing"

	forumcontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Forum"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

type adapterSearchEngine struct{}

func (adapterSearchEngine) Probe(context.Context) error                        { return nil }
func (adapterSearchEngine) EnsureIndex(context.Context) error                  { return nil }
func (adapterSearchEngine) Index(context.Context, search.TopicSearchDoc) error { return nil }
func (adapterSearchEngine) Delete(context.Context, int64) error                { return nil }
func (adapterSearchEngine) Search(_ context.Context, input search.SearchInput) (search.SearchResult, error) {
	ids := []int64{2, 1}
	if input.Query == "one" {
		ids = []int64{1}
	}
	if input.Query == "two" {
		ids = []int64{2}
	}
	items := make([]search.TopicSearchDoc, 0, len(ids))
	for _, id := range ids {
		items = append(items, search.TopicSearchDoc{ID: id, Status: forum.TopicStatusActive, Title: "stale"})
	}
	return search.SearchResult{Items: items, Total: int64(len(items)), Page: input.Page, PerPage: input.PerPage}, nil
}

type countingForumSearchHitStore struct {
	mu    sync.Mutex
	calls int
	docs  map[int64]forum.TopicSummary
}

func (s *countingForumSearchHitStore) ListPublicTopicSearchHits(_ context.Context, ids []int64) (map[int64]forum.TopicSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	out := make(map[int64]forum.TopicSummary, len(ids))
	for _, id := range ids {
		if doc, ok := s.docs[id]; ok {
			out[id] = doc
		}
	}
	return out, nil
}

func (s *countingForumSearchHitStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestSearchServiceAdapterUsesOneForumBatchAndPreservesEngineOrder(t *testing.T) {
	store := &countingForumSearchHitStore{docs: map[int64]forum.TopicSummary{
		1: {ID: 1, Title: "one", Status: forum.TopicStatusActive, Tags: []forum.TopicTagSummary{{ID: 11, Slug: "go", Name: "Go"}}},
		2: {
			ID: 2, Title: "two", Status: forum.TopicStatusActive,
			Author:          &forum.UserSummary{ID: 8, Username: "author"},
			LastReplyAuthor: &forum.UserSummary{ID: 9, Username: "reply"},
			Tags:            []forum.TopicTagSummary{{ID: 12, Slug: "search", Name: "Search"}},
		},
	}}
	adapter := searchServiceAdapter{inner: search.NewService(adapterSearchEngine{}, nil), store: store}
	result, err := adapter.Search(t.Context(), forumcontroller.SearchInput{Query: "ordered", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if store.callCount() != 1 {
		t.Fatalf("Forum summary batch calls = %d, want 1", store.callCount())
	}
	if len(result.Items) != 2 || result.Items[0].ID != 2 || result.Items[1].ID != 1 {
		t.Fatalf("engine ordering lost: %#v", result.Items)
	}
	if result.Items[0].LastReplyAuthor == nil || len(result.Items[0].Tags) != 1 || result.Items[0].Tags[0].Slug != "search" {
		t.Fatalf("Forum list fields lost: %#v", result.Items[0])
	}
}

func TestSearchServiceAdapterKeepsLiveBatchesRequestScoped(t *testing.T) {
	store := &countingForumSearchHitStore{docs: map[int64]forum.TopicSummary{
		1: {ID: 1, Title: "one", Status: forum.TopicStatusActive},
		2: {ID: 2, Title: "two", Status: forum.TopicStatusActive},
	}}
	adapter := searchServiceAdapter{inner: search.NewService(adapterSearchEngine{}, nil), store: store}
	type outcome struct {
		id  int64
		err error
	}
	results := make(chan outcome, 2)
	for _, query := range []string{"one", "two"} {
		query := query
		go func() {
			result, err := adapter.Search(context.Background(), forumcontroller.SearchInput{Query: query, Page: 1, PerPage: 20})
			if err != nil || len(result.Items) != 1 {
				results <- outcome{err: err}
				return
			}
			results <- outcome{id: result.Items[0].ID}
		}()
	}
	seen := map[int64]bool{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		seen[result.id] = true
	}
	if !seen[1] || !seen[2] || store.callCount() != 2 {
		t.Fatalf("request batches leaked: ids=%#v calls=%d", seen, store.callCount())
	}
}
