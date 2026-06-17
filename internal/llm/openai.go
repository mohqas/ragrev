package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/canonical/ragrev/internal/logger"
)

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type openaiClient struct {
	url       string
	model     string
	token     string
	maxTokens int
	log       *logger.Logger
}

func newOpenAIClient(url, model, token string, maxTokens int, log *logger.Logger) *openaiClient {
	return &openaiClient{
		url:       url,
		model:     model,
		token:     token,
		maxTokens: maxTokens,
		log:       log,
	}
}

func (c *openaiClient) MaxPromptTokens() int { return c.maxTokens }

func (c *openaiClient) Complete(ctx context.Context, messages []Message) (string, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	maxRetries := 5
	backoff := 22 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(b))
		if err != nil {
			return "", err
		}

		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", fmt.Errorf("API error: rate limit exceeded after %d retries", maxRetries)
			}
			delay := backoff
			if headerVal := resp.Header.Get("Retry-After"); headerVal != "" {
				var waitSecs int
				if _, err := fmt.Sscanf(headerVal, "%d", &waitSecs); err == nil && waitSecs > 0 {
					delay = time.Duration(waitSecs) * time.Second
				}
			}
			delay += time.Duration(rand.Float64() * float64(time.Second))
			c.log.Debug("  Rate limit reached (429/503). Retrying in %v...\n", delay.Round(time.Second))
			select {
			case <-time.After(delay):
				backoff *= 2
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if strings.Contains(string(body), "RateLimitReached") && attempt < maxRetries {
				delay := backoff
				var waitSecs int
				if _, err := fmt.Sscanf(string(body), "Please wait %d seconds", &waitSecs); err == nil && waitSecs > 0 {
					delay = time.Duration(waitSecs) * time.Second
				}
				delay += time.Duration(rand.Float64() * float64(time.Second))
				c.log.Debug("  Rate limit reached. Retrying in %v...\n", delay.Round(time.Second))
				select {
				case <-time.After(delay):
					backoff *= 2
					continue
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}

			if strings.Contains(string(body), "tokens_limit_reached") {
				return "", fmt.Errorf("tokens_limit_reached: %s", string(body))
			}

			return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}

		var res chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()

		if len(res.Choices) == 0 {
			return "", fmt.Errorf("no response choices returned")
		}
		return res.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("max retries exceeded without a valid response")
}
