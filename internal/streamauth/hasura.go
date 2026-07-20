package streamauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxHasuraResponseBytes = 1 << 20

type hasuraAccessChecker struct {
	endpoint string
	client   *http.Client
}

type hasuraGraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type hasuraGraphQLResponse struct {
	Data struct {
		Users []struct {
			FirebaseUID string `json:"firebase_uid"`
			IsBlocked   *bool  `json:"is_blocked"`
			IsDeleted   *bool  `json:"is_deleted"`
		} `json:"users"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

func newHasuraAccessChecker(endpoint string) (*hasuraAccessChecker, error) {
	endpoint = strings.TrimSpace(endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid HASURA_GRAPHQL_ENDPOINT: %q", endpoint)
	}

	return &hasuraAccessChecker{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}, nil
}

func (h *hasuraAccessChecker) UserCanStream(ctx context.Context, firebaseToken, expectedUID string) (bool, error) {
	payload := hasuraGraphQLRequest{
		Query: `query StreamAccess($firebaseUid: String!) {
  users(where: {firebase_uid: {_eq: $firebaseUid}}, limit: 1) {
    firebase_uid
    is_blocked
    is_deleted
  }
}`,
		Variables: map[string]interface{}{"firebaseUid": expectedUID},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encode Hasura request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("create Hasura request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+firebaseToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("request Hasura: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHasuraResponseBytes))
		return false, fmt.Errorf("Hasura returned HTTP %d", resp.StatusCode)
	}

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHasuraResponseBytes+1))
	if err != nil {
		return false, fmt.Errorf("read Hasura response: %w", err)
	}
	if len(responseBody) > maxHasuraResponseBytes {
		return false, errors.New("Hasura response exceeds size limit")
	}

	var result hasuraGraphQLResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return false, fmt.Errorf("decode Hasura response: %w", err)
	}
	if len(result.Errors) > 0 {
		return false, errors.New("Hasura returned GraphQL errors")
	}
	if len(result.Data.Users) != 1 {
		return false, nil
	}

	user := result.Data.Users[0]
	if user.FirebaseUID != expectedUID || user.IsBlocked == nil || user.IsDeleted == nil {
		return false, nil
	}
	return !*user.IsBlocked && !*user.IsDeleted, nil
}
