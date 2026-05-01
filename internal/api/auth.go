package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GetTokenFromCredentials authenticates with Moodle and returns an API token.
// It uses the moodle_mobile_app service which is universally available.
//
// Credentials are POSTed as form-urlencoded body — putting them in the URL
// query string would expose them in server access logs, reverse-proxy logs,
// and browser history.
func GetTokenFromCredentials(ctx context.Context, baseURL, username, password string) (string, error) {
	endpoint := fmt.Sprintf("%s/login/token.php", baseURL)

	params := url.Values{
		"username": {username},
		"password": {password},
		"service":  {"moodle_mobile_app"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return "", fmt.Errorf("creating auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("authenticating with Moodle: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading auth response: %w", err)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing auth response: %w", err)
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidCredentials, tokenResp.Error)
	}

	if tokenResp.Token == "" {
		return "", fmt.Errorf("received empty token from Moodle")
	}

	return tokenResp.Token, nil
}
