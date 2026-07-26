package services

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
)

type Source struct {
	Symbol string
	Field  string
	Name   string
}

type ResearchService interface {
	Research(ctx context.Context, symbol, question string) (answer string, sources []Source, err error)
}

type RAGService struct {
	embed    EmbeddingsClient
	store    adapters.VectorStore
	llm      *OpenAIClient
	ingester *Ingester // nil = no lazy backfill
}

func NewRAGService(embed EmbeddingsClient, store adapters.VectorStore, llm *OpenAIClient, ingester *Ingester) *RAGService {
	return &RAGService{embed: embed, store: store, llm: llm, ingester: ingester}
}

func (r *RAGService) Research(ctx context.Context, symbol, question string) (string, []Source, error) {
	filter := map[string]string{"symbol": symbol}

	vec, err := r.embed.Embed(ctx, question)
	if err != nil {
		return "", nil, fmt.Errorf("research: embed question: %w", err)
	}

	hits, err := r.store.Query(ctx, vec, 5, filter)
	if err != nil {
		return "", nil, fmt.Errorf("research: query: %w", err)
	}

	// Lazy backfill: nothing indexed for this symbol -> ingest once, then re-query.
	if len(hits) == 0 && r.ingester != nil {
		stats, err := r.ingester.Ingest(ctx, []string{symbol})
		if err != nil {
			log.Printf("research: ingest %s: %v", symbol, err)
		} else if stats.Chunks > 0 {
			hits, err = r.store.Query(ctx, vec, 5, filter)
			if err != nil {
				return "", nil, fmt.Errorf("research: re-query: %w", err)
			}
		}
	}

	if len(hits) == 0 {
		return fmt.Sprintf("No info is indexed for %s. Set an alert for it to ingest.", symbol), nil, nil
	}

	// Context from ALL hits (multiple description windows are all useful to the model).
	var ctxParts []string
	for _, h := range hits {
		ctxParts = append(ctxParts, fmt.Sprintf(`<context field=%q>%s</context>`,
			h.Metadata["field"], h.Metadata["text"]))
	}
	userMsg := fmt.Sprintf("Asset: %s\n\n%s\n\nQuestion: %s",
		symbol, strings.Join(ctxParts, "\n"), question)

	answer, err := r.llm.Chat(ctx, userMsg)
	if err != nil {
		return "", nil, fmt.Errorf("research: chat: %w", err)
	}

	return answer, sourcesFrom(hits), nil
}

func sourcesFrom(hits []adapters.ScoredPoint) []Source {
	seen := make(map[string]struct{})
	var srcs []Source
	for _, h := range hits {
		field := h.Metadata["field"]
		if field == "" {
			continue
		}
		if _, dup := seen[field]; dup {
			continue
		}
		seen[field] = struct{}{}
		srcs = append(srcs, Source{
			Symbol: h.Metadata["symbol"],
			Field:  field,
			Name:   h.Metadata["name"],
		})
	}
	return srcs
}
