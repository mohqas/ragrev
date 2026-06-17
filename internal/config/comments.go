package config

import (
	"bytes"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// marshalWithComments serializes Config to YAML with concise head comments
// on top-level fields. Backend fields share a single documentation block on
// the 'backends' key rather than repeating per entry.
func marshalWithComments(cfg Config) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}

	add := func(key, comment string, value interface{}) error {
		k := &yaml.Node{Kind: yaml.ScalarNode, Value: key, HeadComment: comment}
		v := &yaml.Node{}
		if err := v.Encode(value); err != nil {
			return err
		}
		root.Content = append(root.Content, k, v)
		return nil
	}

	entries := []struct {
		key, comment string
		value        interface{}
	}{
		{"repo", "GitHub 'owner/repo'. Blank = auto-detect from origin.", cfg.Repo},
		{"base_branch", "Target branch for PR lookups.", cfg.BaseBranch},
		{"authors", "Trusted reviewer usernames to ingest. Empty = all.", cfg.Authors},
		{"reviewers", "Reviewer usernames to surface examples from. Empty = any ingested.", cfg.Reviewers},
		{"max_examples", "Max similar past reviews included per chunk.", cfg.MaxExamples},
		{"min_similarity_score", "Minimum vector-similarity score (0.0–1.0) to include a DB result as review context.", cfg.MinSimilarityScore},
		{"min_comment_len", "Skip past comments shorter than this many chars.", cfg.MinCommentLen},
		{"rate_delay", "Delay between PRs while ingesting (Go duration: 500ms, 1s, 2m).", time.Duration(cfg.RateDelay).String()},
		{"review_scope", "Context per review: pr | file | chunk.", cfg.ReviewScope},
		{"backend", "Active backend; must match a key under 'backends'.", cfg.Backend},
	}
	for _, e := range entries {
		if err := add(e.key, e.comment, e.value); err != nil {
			return nil, err
		}
	}

	backendsKey := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: "backends",
		HeadComment: "LLM backends. Each entry accepts:\n" +
			"  model             - model id (e.g. gpt-4o-mini, gemini-2.0-flash)\n" +
			"  url               - API endpoint\n" +
			"  token_env         - env var to read the token from\n" +
			"  token_cmd         - shell command to obtain the token if env is empty\n" +
			"  max_prompt_tokens - prompt cap; 0 = auto (~75% of model context)",
	}
	backendsVal, err := encodeBackends(cfg.Backends)
	if err != nil {
		return nil, err
	}
	root.Content = append(root.Content, backendsKey, backendsVal)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeBackends produces a MappingNode of backends in sorted order, with no
// per-field comments (documented once on the 'backends' header).
func encodeBackends(backends map[string]BackendConfig) (*yaml.Node, error) {
	mapping := &yaml.Node{Kind: yaml.MappingNode}

	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		nameNode := &yaml.Node{Kind: yaml.ScalarNode, Value: name}
		bcNode, err := encodeBackendConfig(backends[name])
		if err != nil {
			return nil, err
		}
		mapping.Content = append(mapping.Content, nameNode, bcNode)
	}
	return mapping, nil
}

// encodeBackendConfig emits a backend entry with all fields present (so
// users discover optional ones like max_prompt_tokens) but no per-field
// comments.
func encodeBackendConfig(bc BackendConfig) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}

	push := func(key string, value interface{}) error {
		k := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
		v := &yaml.Node{}
		if err := v.Encode(value); err != nil {
			return err
		}
		node.Content = append(node.Content, k, v)
		return nil
	}

	if err := push("model", bc.Model); err != nil {
		return nil, err
	}
	if err := push("url", bc.URL); err != nil {
		return nil, err
	}
	if err := push("token_env", bc.TokenEnv); err != nil {
		return nil, err
	}
	if err := push("token_cmd", bc.TokenCmd); err != nil {
		return nil, err
	}
	if err := push("max_prompt_tokens", bc.MaxPromptTokens); err != nil {
		return nil, err
	}

	return node, nil
}

