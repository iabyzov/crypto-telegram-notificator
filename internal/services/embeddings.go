package services

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ollama/ollama/api"
)

type EmbeddingsClient interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

type OllamaEmbeddings struct {
	client *api.Client
	model  string
}

type bearerTransport struct {
	base http.RoundTripper
	key  string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context()) // don't mutate caller's request
	req.Header.Set("Authorization", "Bearer "+t.key)
	return t.base.RoundTrip(req)
}

func NewOllamaEmbeddings(baseUrl string, apiKey string, model string) (*OllamaEmbeddings, error) {
	base, err := url.Parse(baseUrl)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{Transport: bearerTransport{base: http.DefaultTransport, key: apiKey}}
	return &OllamaEmbeddings{client: api.NewClient(base, httpClient), model: model}, nil
}

func (p *OllamaEmbeddings) Embed(ctx context.Context, text string) ([]float32, error) {
	response, err := p.client.Embed(ctx, &api.EmbedRequest{Model: p.model, Input: text})
	if err != nil || len(response.Embeddings) == 0 {
		if err != nil {
			return nil, err
		} else {
			return nil, fmt.Errorf("empty embeddings response")
		}
	}

	return response.Embeddings[0], nil
}

func (p *OllamaEmbeddings) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	response, err := p.client.Embed(ctx, &api.EmbedRequest{Model: p.model, Input: texts})
	if err != nil || len(response.Embeddings) != len(texts) {
		if err != nil {
			return nil, err
		} else {
			return nil, fmt.Errorf("empty embeddings response")
		}
	}

	return response.Embeddings, nil
}
