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

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type geminiClient struct {
	url   string
	model string
	token string
	log   *logger.Logger
}

func newGeminiClient(url, model, token string, log *logger.Logger) *geminiClient {
	return &geminiClient{
		url:   url,
		model: model,
		token: token,
		log:   log,
	}
}

func (c *geminiClient) Complete(ctx context.Context, messages []Message) (string, error) {
	var reqBody geminiRequest

	for _, m := range messages {
		role := m.Role
		// Gemini roles: "user" or "model" ("system" goes in SystemInstruction)
		if role == "assistant" {
			role = "model"
		}

		if role == "system" {
			reqBody.SystemInstruction = &geminiContent{
				Role:  "system",
				Parts: []geminiPart{{Text: m.Content}},
			}
			continue
		}

		reqBody.Contents = append(reqBody.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// build endpoint URL. e.g. https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=...
	// We'll strip the path from the base URL if needed, but assuming url is just the base or full path.
	// For simplicity, if url contains generateContent, use it as is; otherwise construct it.
	endpoint := c.url
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions") // strip OpenAI compatibility suffix if leftover in config
	if !strings.HasSuffix(endpoint, ":generateContent") {
		// Usually URL in config is either base or the full path. Let's just assume we need to replace or append.
		// If base is https://generativelanguage.googleapis.com/v1beta
		endpoint = strings.TrimSuffix(endpoint, "/") + "/models/" + c.model + ":generateContent"
	}
	endpoint += "?key=" + c.token

	maxRetries := 5
	backoff := 22 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
		if err != nil {
			return "", err
		}

		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", fmt.Errorf("gemini API error: rate limit exceeded after %d retries", maxRetries)
			}
			delay := backoff
			delay += time.Duration(rand.Float64() * float64(time.Second))
			c.log.Print("  Rate limit reached (429/503). Retrying in %v...\n", delay.Round(time.Second))
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
			return "", fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
		}

		var res geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()

		if len(res.Candidates) == 0 || len(res.Candidates[0].Content.Parts) == 0 {
			return "", fmt.Errorf("no response candidates/parts returned")
		}
		return res.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("max retries exceeded without a valid response from Gemini")
}
