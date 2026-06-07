package dynacat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

const twitchGqlEndpoint = "https://gql.twitch.tv/gql"
const twitchGqlClientId = "kimne78kx3ncx6brgo4mv6wki5h1ko"
const twitchMaxOperationsPerRequest = 35

type twitchGraphQLOperationRequest struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query,omitempty"`
	Variables     any            `json:"variables"`
	Extensions    map[string]any `json:"extensions,omitempty"`
}

type twitchGraphQLOperationResponse[T any] struct {
	Data       T                    `json:"data"`
	Errors     []twitchGraphQLError `json:"errors"`
	Extensions struct {
		OperationName string `json:"operationName"`
	} `json:"extensions"`
}

type twitchGraphQLError struct {
	Message string `json:"message"`
}

func newTwitchGraphQLQueryRequest(operationName string, variables any, query string) twitchGraphQLOperationRequest {
	return twitchGraphQLOperationRequest{
		OperationName: operationName,
		Query:         query,
		Variables:     variables,
	}
}

func newTwitchGraphQLPersistedQueryRequest(operationName string, variables any, hash string) twitchGraphQLOperationRequest {
	return twitchGraphQLOperationRequest{
		OperationName: operationName,
		Variables:     variables,
		Extensions: map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	}
}

func decodeJsonFromTwitchGraphQLRequest[T any](ctx context.Context, operations []twitchGraphQLOperationRequest) ([]twitchGraphQLOperationResponse[T], error) {
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := json.Marshal(operations)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, "POST", twitchGqlEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Client-ID", twitchGqlClientId)
	request.Header.Set("Content-Type", "application/json")
	setBrowserUserAgentHeader(request)

	return decodeJsonFromRequest[[]twitchGraphQLOperationResponse[T]](defaultHTTPClient, request)
}
