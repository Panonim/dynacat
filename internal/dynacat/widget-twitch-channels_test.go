package dynacat

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestFetchChannelFromTwitchOperationsLive(t *testing.T) {
	channel, err := fetchChannelFromTwitchOperation("Example", twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"Example","profileImageURL":"https://example.com/avatar.png","stream":{"viewersCount":42,"createdAt":"2026-05-16T12:34:56Z","game":{"slug":"science-and-technology","name":"Science & Technology"}},"lastBroadcast":{"title":"Building things"}}},"extensions":{"operationName":"ChannelStatus"}}`))
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !channel.Exists {
		t.Fatal("Expected channel to exist")
	}
	if !channel.IsLive {
		t.Fatal("Expected channel to be live")
	}
	if channel.Login != "example" {
		t.Fatalf("Expected login to be normalized, got %q", channel.Login)
	}
	if channel.Name != "Example" {
		t.Fatalf("Expected display name, got %q", channel.Name)
	}
	if channel.AvatarUrl != "https://example.com/avatar.png" {
		t.Fatalf("Expected avatar URL, got %q", channel.AvatarUrl)
	}
	if channel.ViewersCount != 42 {
		t.Fatalf("Expected viewers count 42, got %d", channel.ViewersCount)
	}
	if channel.StreamTitle != "Building things" {
		t.Fatalf("Expected stream title, got %q", channel.StreamTitle)
	}
	if channel.Category != "Science & Technology" {
		t.Fatalf("Expected category, got %q", channel.Category)
	}
	if channel.CategorySlug != "science-and-technology" {
		t.Fatalf("Expected category slug, got %q", channel.CategorySlug)
	}
	if !channel.LiveSince.Equal(time.Date(2026, 5, 16, 12, 34, 56, 0, time.UTC)) {
		t.Fatalf("Expected live since timestamp, got %v", channel.LiveSince)
	}
}

func TestFetchChannelFromTwitchOperationsMissing(t *testing.T) {
	channel, err := fetchChannelFromTwitchOperation("missing_channel", twitchOperationResponseFromJSON(t, `{"data":{"user":null},"extensions":{"operationName":"ChannelStatus"}}`))
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if channel.Exists {
		t.Fatal("Expected channel not to exist")
	}
	if channel.Name != "missing_channel" {
		t.Fatalf("Expected fallback name, got %q", channel.Name)
	}
	if channel.ViewersCount != -1 {
		t.Fatalf("Expected missing channel viewers count -1, got %d", channel.ViewersCount)
	}
}

func TestFetchChannelFromTwitchOperationRejectsMissingOperationName(t *testing.T) {
	_, err := fetchChannelFromTwitchOperation("example", twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"Example"}}}`))
	if err == nil {
		t.Fatal("Expected missing operation name error")
	}
}

func TestTwitchChannelListSortByViewersKeepsZeroViewerLiveChannelsAboveOffline(t *testing.T) {
	channels := twitchChannelList{
		{Login: "offline-a", ViewersCount: -1},
		{Login: "live", IsLive: true, ViewersCount: 0},
		{Login: "offline-b", ViewersCount: -1},
	}

	channels.sortByViewers()

	if channels[0].Login != "live" {
		t.Fatalf("Expected live zero-viewer channel first, got %q", channels[0].Login)
	}
}

func TestDedupeTwitchChannelLogins(t *testing.T) {
	logins := dedupeTwitchChannelLogins([]string{" Twitch ", "", "\tMonstercat\n", "twitch"})

	if len(logins) != 2 {
		t.Fatalf("Expected 2 logins, got %d", len(logins))
	}
	if logins[0] != "twitch" || logins[1] != "monstercat" {
		t.Fatalf("Expected normalized logins, got %#v", logins)
	}
}

func TestFetchChannelsFromTwitchOperationsPreservesPartialResults(t *testing.T) {
	channels, err := fetchChannelsFromTwitchOperations([]string{"first", "broken", "third"}, []twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]{
		twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"First"}},"extensions":{"operationName":"ChannelStatus"}}`),
		twitchOperationResponseFromJSON(t, `{"errors":[{"message":"failed"}],"extensions":{"operationName":"ChannelStatus"}}`),
		twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"Third"}},"extensions":{"operationName":"ChannelStatus"}}`),
	})
	if !errors.Is(err, errPartialContent) {
		t.Fatalf("Expected partial content error, got %v", err)
	}

	if len(channels) != 2 {
		t.Fatalf("Expected 2 channels, got %d", len(channels))
	}
	if channels[0].Login != "first" || channels[1].Login != "third" {
		t.Fatalf("Expected successful channels to be preserved, got %#v", channels)
	}
}

func TestFetchChannelsFromTwitchOperationsMapsResponsesByRequestOrder(t *testing.T) {
	channels, err := fetchChannelsFromTwitchOperations([]string{"first", "second"}, []twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]{
		twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"First"}},"extensions":{"operationName":"ChannelStatus"}}`),
		twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"Second"}},"extensions":{"operationName":"ChannelStatus"}}`),
	})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if channels[0].Login != "first" || channels[0].Name != "First" {
		t.Fatalf("Expected first response to map to first login, got %#v", channels[0])
	}
	if channels[1].Login != "second" || channels[1].Name != "Second" {
		t.Fatalf("Expected second response to map to second login, got %#v", channels[1])
	}
}

func TestFetchChannelsFromTwitchOperationsRejectsMismatchedResponses(t *testing.T) {
	_, err := fetchChannelsFromTwitchOperations([]string{"first", "second"}, []twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]{
		twitchOperationResponseFromJSON(t, `{"data":{"user":{"displayName":"First"}},"extensions":{"operationName":"ChannelStatus"}}`),
	})
	if err == nil {
		t.Fatal("Expected response count mismatch error")
	}
}

func TestCollectTwitchChannelBatchResultsPreservesPartialBatchResults(t *testing.T) {
	channels, failed := collectTwitchChannelBatchResults(
		[][]string{{"one", "two"}, {"three", "four"}},
		[]twitchChannelList{{{Login: "one"}}, {{Login: "three"}, {Login: "four"}}},
		[]error{errPartialContent, nil},
	)

	if failed != 1 {
		t.Fatalf("Expected 1 failed channel, got %d", failed)
	}
	if len(channels) != 3 {
		t.Fatalf("Expected 3 channels, got %d", len(channels))
	}
	if channels[0].Login != "one" || channels[1].Login != "three" || channels[2].Login != "four" {
		t.Fatalf("Expected successful channels from both batches, got %#v", channels)
	}
}

func twitchOperationResponseFromJSON(t *testing.T, data string) twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse] {
	t.Helper()

	var response twitchGraphQLOperationResponse[twitchChannelStatusOperationResponse]
	if err := json.Unmarshal([]byte(data), &response); err != nil {
		t.Fatalf("Failed to unmarshal Twitch operation response: %v", err)
	}
	return response
}
