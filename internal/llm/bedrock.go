package llm

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// awsCreds holds a resolved set of AWS credentials (static or temporary).
type awsCreds struct {
	KeyID        string
	SecretKey    string
	SessionToken string    // non-empty for temporary credentials from IAM roles
	Expiry       time.Time // zero for static credentials
}

// credCache caches resolved credentials to avoid hitting the metadata endpoint
// on every request. Refreshes when credentials are within 30s of expiry.
type credCache struct {
	mu    sync.Mutex
	creds *awsCreds
}

func (c *credCache) get() *awsCreds {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creds == nil {
		return nil
	}
	if c.creds.Expiry.IsZero() {
		return c.creds // static creds never expire
	}
	if time.Now().Before(c.creds.Expiry.Add(-30 * time.Second)) {
		return c.creds
	}
	return nil // expired or about to expire
}

func (c *credCache) set(creds *awsCreds) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creds = creds
}

type bedrockProvider struct {
	region  string
	modelID string
	baseURL string // for testing
	cfg     BackendConfig
	cache   credCache
	http    *http.Client
}

func newBedrockProvider(cfg BackendConfig, hc *http.Client) (*bedrockProvider, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("llm: bedrock requires region")
	}
	model := cfg.Model
	if model == "" {
		model = "anthropic.claude-3-5-sonnet-20241022-v2:0"
	}
	return &bedrockProvider{
		region:  cfg.Region,
		modelID: model,
		baseURL: cfg.BaseURL,
		cfg:     cfg,
		http:    hc,
	}, nil
}

// Summarize calls the Bedrock Converse API, which provides a unified interface
// across all Bedrock-hosted models.
func (p *bedrockProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	url := p.baseURL
	if url == "" {
		url = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", p.region)
	}
	url = fmt.Sprintf("%s/model/%s/converse", url, p.modelID)

	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]string{
					{"type": "text", "text": prompt},
				},
			},
		},
		"inferenceConfig": map[string]any{
			"maxTokens": 512,
		},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := p.signRequest(ctx, req, body); err != nil {
		return "", fmt.Errorf("bedrock sign: %w", err)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("bedrock request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bedrock error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Output struct {
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("bedrock parse: %w", err)
	}
	if len(result.Output.Message.Content) == 0 {
		return "", fmt.Errorf("bedrock returned no content")
	}
	return result.Output.Message.Content[0].Text, nil
}

// DiscoverModels lists Bedrock foundation models available in the configured region.
func (p *bedrockProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	url := p.baseURL
	if url == "" {
		url = fmt.Sprintf("https://bedrock.%s.amazonaws.com", p.region)
	}
	url = fmt.Sprintf("%s/foundation-models", url)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if err := p.signRequest(ctx, req, nil); err != nil {
		return nil, fmt.Errorf("bedrock sign: %w", err)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock models request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock models error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		ModelSummaries []struct {
			ModelID   string `json:"modelId"`
			ModelName string `json:"modelName"`
		} `json:"modelSummaries"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("bedrock models parse: %w", err)
	}

	models := make([]ModelInfo, len(result.ModelSummaries))
	for i, m := range result.ModelSummaries {
		models[i] = ModelInfo{ID: m.ModelID, Name: m.ModelName}
	}
	return models, nil
}

// signRequest resolves credentials (with caching) and applies SigV4 headers.
func (p *bedrockProvider) signRequest(ctx context.Context, r *http.Request, body []byte) error {
	creds := p.cache.get()
	if creds == nil {
		var err error
		creds, err = resolveAWSCreds(ctx, p.cfg, p.http)
		if err != nil {
			return fmt.Errorf("resolve credentials: %w", err)
		}
		p.cache.set(creds)
	}
	return signSigV4(r, body, creds, p.region, "bedrock")
}

// --- AWS credential resolution chain ---

// resolveAWSCreds resolves credentials using the standard AWS chain:
//  1. Static credentials in BackendConfig (AWSKeyID + AWSSecretKey)
//  2. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN env vars
//  3. ECS task role via AWS_CONTAINER_CREDENTIALS_RELATIVE_URI or _FULL_URI
//  4. EC2/EKS instance profile via IMDSv2
func resolveAWSCreds(ctx context.Context, cfg BackendConfig, hc *http.Client) (*awsCreds, error) {
	// 1. Static config credentials.
	if cfg.AWSKeyID != "" && cfg.AWSSecretKey != "" {
		return &awsCreds{KeyID: cfg.AWSKeyID, SecretKey: cfg.AWSSecretKey}, nil
	}

	// 2. Environment variables.
	if id := os.Getenv("AWS_ACCESS_KEY_ID"); id != "" {
		return &awsCreds{
			KeyID:        id,
			SecretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken: os.Getenv("AWS_SESSION_TOKEN"),
		}, nil
	}

	// 3. ECS container credentials.
	if rel := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); rel != "" {
		return fetchContainerCreds(ctx, "http://169.254.170.2"+rel, "", hc)
	}
	if full := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI"); full != "" {
		token := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
		return fetchContainerCreds(ctx, full, token, hc)
	}

	// 4. EC2 / EKS instance metadata (IMDSv2).
	return fetchIMDSCreds(ctx, hc)
}

// fetchContainerCreds fetches temporary credentials from the ECS task metadata endpoint.
func fetchContainerCreds(ctx context.Context, url, token string, hc *http.Client) (*awsCreds, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("bedrock ecs creds: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return parseTempCreds(hc, req, "ECS container credentials")
}

// fetchIMDSCreds fetches temporary credentials via EC2 IMDSv2 (also works for EKS).
func fetchIMDSCreds(ctx context.Context, hc *http.Client) (*awsCreds, error) {
	const imdsBase = "http://169.254.169.254/latest"

	// Step 1: obtain IMDSv2 session token.
	tokenReq, err := http.NewRequestWithContext(ctx, "PUT", imdsBase+"/api/token", nil)
	if err != nil {
		return nil, fmt.Errorf("bedrock imds token request: %w", err)
	}
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	tokenResp, err := hc.Do(tokenReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock imds: not running on EC2/EKS or IMDS unreachable: %w", err)
	}
	defer tokenResp.Body.Close()
	tokenBytes, _ := io.ReadAll(tokenResp.Body)
	if tokenResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock imds: token request failed (%d)", tokenResp.StatusCode)
	}
	imdsToken := strings.TrimSpace(string(tokenBytes))

	// Step 2: get the IAM role name.
	roleReq, _ := http.NewRequestWithContext(ctx, "GET", imdsBase+"/meta-data/iam/security-credentials/", nil)
	roleReq.Header.Set("X-aws-ec2-metadata-token", imdsToken)
	roleResp, err := hc.Do(roleReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock imds: get role name: %w", err)
	}
	defer roleResp.Body.Close()
	roleBytes, _ := io.ReadAll(roleResp.Body)
	if roleResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock imds: no IAM role attached to instance")
	}
	role := strings.TrimSpace(string(roleBytes))

	// Step 3: fetch credentials for the role.
	credsReq, _ := http.NewRequestWithContext(ctx, "GET", imdsBase+"/meta-data/iam/security-credentials/"+role, nil)
	credsReq.Header.Set("X-aws-ec2-metadata-token", imdsToken)
	return parseTempCreds(hc, credsReq, "EC2 instance metadata")
}

func parseTempCreds(hc *http.Client, req *http.Request, source string) (*awsCreds, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock %s: %w", source, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock %s error %d: %s", source, resp.StatusCode, string(data))
	}
	var result struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		Token           string `json:"Token"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("bedrock %s parse: %w", source, err)
	}
	creds := &awsCreds{
		KeyID:        result.AccessKeyID,
		SecretKey:    result.SecretAccessKey,
		SessionToken: result.Token,
	}
	if result.Expiration != "" {
		if t, err := time.Parse(time.RFC3339, result.Expiration); err == nil {
			creds.Expiry = t
		}
	}
	return creds, nil
}

// --- SigV4 signing ---

// signSigV4 adds AWS Signature Version 4 authentication headers to r.
// Both bedrock.*.amazonaws.com and bedrock-runtime.*.amazonaws.com use service "bedrock".
func signSigV4(r *http.Request, body []byte, creds *awsCreds, region, service string) error {
	now := time.Now().UTC()
	dateTime := now.Format("20060102T150405Z")
	date := now.Format("20060102")

	var bodyBytes []byte
	if body != nil {
		bodyBytes = body
	}
	bodyHash := sha256Hex(bodyBytes)

	r.Header.Set("x-amz-date", dateTime)
	r.Header.Set("x-amz-content-sha256", bodyHash)
	if creds.SessionToken != "" {
		r.Header.Set("x-amz-security-token", creds.SessionToken)
	}
	if r.Host == "" {
		r.Host = r.URL.Host
	}

	canonHeaders, signedHeaders := buildHeaders(r)
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	canonReq := strings.Join([]string{
		r.Method,
		path,
		r.URL.RawQuery,
		canonHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	credScope := strings.Join([]string{date, region, service, "aws4_request"}, "/")
	strToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		dateTime,
		credScope,
		sha256Hex([]byte(canonReq)),
	}, "\n")

	sigKey := deriveSigningKey(creds.SecretKey, date, region, service)
	sig := hex.EncodeToString(hmacSHA256(sigKey, []byte(strToSign)))

	r.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		creds.KeyID, credScope, signedHeaders, sig,
	))
	return nil
}

func buildHeaders(r *http.Request) (canonical, signed string) {
	seen := map[string]bool{}
	var names []string
	for k := range r.Header {
		lk := strings.ToLower(k)
		if !seen[lk] {
			seen[lk] = true
			names = append(names, lk)
		}
	}
	if !seen["host"] {
		names = append(names, "host")
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, h := range names {
		if h == "host" {
			sb.WriteString("host:" + r.Host + "\n")
		} else {
			vals := r.Header[http.CanonicalHeaderKey(h)]
			sb.WriteString(h + ":" + strings.TrimSpace(strings.Join(vals, ",")) + "\n")
		}
	}
	return sb.String(), strings.Join(names, ";")
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}
