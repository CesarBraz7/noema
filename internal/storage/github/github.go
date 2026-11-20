// Package github implements the subset of the GitHub Contents API used by Noema.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxFileSize = 768 << 10

var (
	ErrConflict   = errors.New("conflito ao gravar arquivo no GitHub")
	ErrPermission = errors.New("GitHub recusou a autorização do repositório")
)

type Repository struct {
	base   string
	token  string
	client *http.Client
}

func New(base, owner, name, token string) *Repository {
	return &Repository{
		base:  strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/",
		token: token,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

type File struct {
	Data        []byte
	SHA         string
	ETag        string
	Exists      bool
	NotModified bool
}

func (r *Repository) Get(ctx context.Context, path, etag string) (File, error) {
	resp, err := r.request(ctx, http.MethodGet, path, etag, nil)
	if err != nil {
		return File{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return File{NotModified: true, ETag: etag}, nil
	case http.StatusNotFound:
		return File{}, nil
	case http.StatusOK:
	default:
		return File{}, statusError(resp)
	}
	var raw struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*MaxFileSize+1))
	if err != nil || len(body) > 2*MaxFileSize || json.Unmarshal(body, &raw) != nil {
		return File{}, errors.New("resposta de conteúdo inválida do GitHub")
	}
	if raw.Encoding != "base64" || raw.SHA == "" {
		return File{}, errors.New("resposta de conteúdo incompleta do GitHub")
	}
	data, err := base64.StdEncoding.DecodeString(raw.Content)
	if err != nil || len(data) > MaxFileSize {
		return File{}, errors.New("arquivo do GitHub inválido ou acima do limite")
	}
	return File{Data: data, SHA: raw.SHA, ETag: resp.Header.Get("ETag"), Exists: true}, nil
}

// Put creates a file if previousSHA is empty, or uses the observed SHA as a compare-and-swap.
func (r *Repository) Put(ctx context.Context, path, message string, data []byte, previousSHA string) error {
	if len(data) > MaxFileSize {
		return errors.New("arquivo acima do limite de publicação")
	}
	body, err := json.Marshal(struct {
		Message string `json:"message"`
		Content string `json:"content"`
		SHA     string `json:"sha,omitempty"`
	}{message, base64.StdEncoding.EncodeToString(data), previousSHA})
	if err != nil {
		return err
	}
	resp, err := r.request(ctx, http.MethodPut, path, "", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return ErrConflict
	default:
		return statusError(resp)
	}
}

func (r *Repository) request(ctx context.Context, method, path, etag string, body []byte) (*http.Response, error) {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+strings.Join(segments, "/"), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("requisição inválida ao GitHub")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+r.token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errors.New("falha na conexão com o GitHub")
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode == http.StatusForbidden &&
		resp.Header.Get("X-RateLimit-Remaining") != "0" && resp.Header.Get("Retry-After") == "") {
		return ErrPermission
	}
	return fmt.Errorf("GitHub respondeu com HTTP %d", resp.StatusCode)
}
