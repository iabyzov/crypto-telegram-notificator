package adapters

import (
	"context"
	"fmt"
	"strings"

	"github.com/upstash/vector-go"
)

type VectorPoint struct {
	ID       string
	Vector   []float32
	Metadata map[string]string
}

type ScoredPoint struct {
	ID       string
	Vector   []float32
	Score    float32
	Metadata map[string]string
}

type VectorStore interface {
	Upsert(ctx context.Context, points []VectorPoint) error
	Query(ctx context.Context, vec []float32, topK int, filter map[string]string) ([]ScoredPoint, error)
}

type UpstashVectorStore struct {
	index *vector.Index
}

func NewUpstashVectorStore(url, token string) *UpstashVectorStore {
	opts := vector.Options{
		Url:   url,
		Token: token,
	}

	return &UpstashVectorStore{index: vector.NewIndexWith(opts)}
}

func (p *UpstashVectorStore) Upsert(ctx context.Context, points []VectorPoint) error {
	ups := make([]vector.Upsert, len(points))
	for i, pt := range points {
		md := make(map[string]any, len(pt.Metadata))
		for k, v := range pt.Metadata {
			md[k] = v
		}
		ups[i] = vector.Upsert{Id: pt.ID, Vector: pt.Vector, Metadata: md}
	}
	return p.index.UpsertMany(ups)
}

func (p *UpstashVectorStore) Query(ctx context.Context, vec []float32, topK int, filter map[string]string) ([]ScoredPoint, error) {
	filterConds := []string{}
	for k, v := range filter {
		filterConds = append(filterConds, fmt.Sprintf("%s = '%s'", k, v))
	}
	scores, err := p.index.Query(vector.Query{
		Vector:          vec,
		TopK:            topK,
		Filter:          strings.Join(filterConds, "AND"),
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, err
	}
	res := make([]ScoredPoint, len(scores))
	for i, s := range scores {
		md := make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			md[k] = fmt.Sprint(v)
		}
		res[i] = ScoredPoint{ID: s.Id, Score: s.Score, Metadata: md}
	}
	return res, nil
}
