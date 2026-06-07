package dynacat

import (
	"testing"
	"time"
)

func TestBuildTwitchCategories(t *testing.T) {
	categories := buildTwitchCategories([]twitchCategoryEdge{
		{Node: twitchCategory{
			Slug:            "excluded",
			Name:            "Excluded",
			AvatarUrl:       "https://static-cdn.example/285x380.jpg",
			GameReleaseDate: time.Now().UTC().Format(time.RFC3339),
		}},
		{Node: twitchCategory{
			Slug:            "included",
			Name:            "Included",
			AvatarUrl:       "https://static-cdn.example/285x380.jpg",
			GameReleaseDate: time.Now().UTC().Format(time.RFC3339),
			Tags: []twitchCategoryTag{
				{Name: "One"},
				{Name: "Two"},
				{Name: "Three"},
			},
		}},
		{Node: twitchCategory{
			Slug:      "limited-out",
			Name:      "Limited Out",
			AvatarUrl: "https://static-cdn.example/285x380.jpg",
		}},
	}, []string{"excluded"}, 1)

	if len(categories) != 1 {
		t.Fatalf("Expected 1 category, got %d", len(categories))
	}
	if categories[0].Slug != "included" {
		t.Fatalf("Expected included category, got %q", categories[0].Slug)
	}
	if categories[0].AvatarUrl != "https://static-cdn.example/144x192.jpg" {
		t.Fatalf("Expected resized avatar URL, got %q", categories[0].AvatarUrl)
	}
	if len(categories[0].Tags) != 2 {
		t.Fatalf("Expected 2 tags, got %d", len(categories[0].Tags))
	}
	if !categories[0].IsNew {
		t.Fatal("Expected recent category to be marked new")
	}
}
