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
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openaiClient struct {
	url          string
	model        string
	token        string
	maxTokens    int
	extraHeaders map[string]string
	log          *logger.Logger
}

func newOpenAIClient(url, model, token string, maxTokens int, extraHeaders map[string]string, log *logger.Logger) *openaiClient {
	return &openaiClient{
		url:          url,
		model:        model,
		token:        token,
		maxTokens:    maxTokens,
		extraHeaders: extraHeaders,
		log:          log,
	}
}

func (c *openaiClient) MaxPromptTokens() int { return c.maxTokens }

func (c *openaiClient) Model() string { return c.model }

func (c *openaiClient) Complete(ctx context.Context, messages []Message) (string, Usage, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, err
	}

	maxRetries := 5
	backoff := 22 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(b))
		if err != nil {
			return "", Usage{}, err
		}

		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		for k, v := range c.extraHeaders {
			req.Header.Set(k, v)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", Usage{}, err
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", Usage{}, fmt.Errorf("API error: rate limit exceeded after %d retries", maxRetries)
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
				return "", Usage{}, ctx.Err()
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
					return "", Usage{}, ctx.Err()
				}
			}

			if strings.Contains(string(body), "tokens_limit_reached") {
				return "", Usage{}, fmt.Errorf("tokens_limit_reached: %s", string(body))
			}

			return "", Usage{}, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}

		var res chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			return "", Usage{}, err
		}
		resp.Body.Close()

		if len(res.Choices) == 0 {
			return "", Usage{}, fmt.Errorf("no response choices returned")
		}
		content := res.Choices[0].Message.Content
		usage := Usage{
			PromptTokens:     res.Usage.PromptTokens,
			CompletionTokens: res.Usage.CompletionTokens,
			TotalTokens:      res.Usage.TotalTokens,
		}
		// Fall back to estimates if the provider omitted usage data.
		if usage.PromptTokens == 0 {
			usage.PromptTokens = EstimateMessagesTokens(messages)
			usage.Estimated = true
		}
		if usage.CompletionTokens == 0 {
			usage.CompletionTokens = EstimateTokens(content)
			usage.Estimated = true
		}
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		return content, usage, nil
	}

	return "", Usage{}, fmt.Errorf("max retries exceeded without a valid response")
}
