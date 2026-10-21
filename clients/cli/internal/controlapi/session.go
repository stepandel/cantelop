package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Credentials struct {
	ConsoleURL   string `json:"console_url"`
	ControlURL   string `json:"control_url"`
	RefreshToken string `json:"refresh_token"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	ControlURL  string `json:"control_url"`
}

type Session struct {
	Client      *http.Client
	Credentials Credentials
	Token       TokenResponse
	ExpiresAt   time.Time
}

func (s *Session) Request(ctx context.Context, method, path, contentType string, body io.Reader, output any) error {
	return s.RequestWithHeaders(ctx, method, path, contentType, body, output)
}

func (s *Session) RequestWithHeaders(ctx context.Context, method, path, contentType string, body io.Reader, output any, additionalHeaders ...string) error {
	token, err := s.accessToken(ctx)
	if err != nil {
		return err
	}
	requestContext := ctx
	cancel := func() {}
	if contentType != "application/octet-stream" {
		requestContext, cancel = context.WithTimeout(ctx, 30*time.Second)
	}
	defer cancel()
	headers := []string{"Authorization", "Bearer " + token}
	if contentType == "application/octet-stream" {
		headers = append(headers, "Content-Encoding", "gzip")
	}
	headers = append(headers, additionalHeaders...)
	return RequestJSON(requestContext, s.Client, method, s.Credentials.ControlURL+path, contentType, body, output, headers...)
}

func (s *Session) accessToken(ctx context.Context) (string, error) {
	if s.Token.AccessToken == "" || time.Now().After(s.ExpiresAt) {
		exchangeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		token, err := ExchangeTenantToken(exchangeContext, s.Client, s.Credentials)
		cancel()
		if err != nil {
			return "", fmt.Errorf("refresh login: %w", err)
		}
		s.Token = token
		s.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn-10) * time.Second)
	}
	return s.Token.AccessToken, nil
}

func ExchangeTenantToken(ctx context.Context, client *http.Client, credentials Credentials) (TokenResponse, error) {
	document, _ := json.Marshal(map[string]string{"refresh_token": credentials.RefreshToken})
	var token TokenResponse
	if err := RequestJSON(ctx, client, http.MethodPost, credentials.ConsoleURL+"/api/cli/token", "application/json", bytes.NewReader(document), &token); err != nil {
		return token, err
	}
	controlURL, err := ValidatePublicOrigin(token.ControlURL)
	if err != nil || controlURL != credentials.ControlURL || token.TokenType != "Bearer" || token.ExpiresIn < 20 || token.AccessToken == "" {
		return token, errors.New("console returned an invalid tenant token")
	}
	return token, nil
}
