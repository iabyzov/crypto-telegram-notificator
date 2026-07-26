package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
)

// IngestStats reports what happened during an ingestion run. The caller
// (alert-creation goroutine, logs) uses it to report progress.
type IngestStats struct {
	Symbols  int // symbols successfully processed
	Chunks   int // text chunks produced
	Embedded int // vectors produced (== chunks unless something was skipped)
	Upserted int // points upserted into the store
	Skipped  int // symbols skipped due to fetch/empty errors
}

// Ingester orchestrates the RAG ingestion pipeline:
//
//	GetAssetInfo -> chunk by field -> EmbedBatch -> Upsert.
//
// It is safe to call with one symbol (alert-creation trigger) or many
// (backfill). Re-ingesting the same symbol upserts (deterministic IDs)
// instead of duplicating.
type Ingester struct {
	info  *AssetInfoService
	embed EmbeddingsClient
	store adapters.VectorStore
}

func NewIngester(info *AssetInfoService, embed EmbeddingsClient, store adapters.VectorStore) *Ingester {
	return &Ingester{info: info, embed: embed, store: store}
}

// chunk is one unit of text to be embedded, tagged with the source field
// so Research can cite where a retrieved fact came from.
type chunk struct {
	Text  string
	Field string
}

// Ingest fetches asset info for each symbol, chunks it, embeds the chunks,
// and upserts the vectors. Errors for a single symbol are logged and that
// symbol is skipped — one bad symbol must not abort the whole batch.
func (i *Ingester) Ingest(ctx context.Context, symbols []string) (IngestStats, error) {
	var st IngestStats

	for _, sym := range symbols {
		info, err := i.info.GetAssetInfo(ctx, sym)
		if err != nil {
			log.Printf("ingest: skip %s: %v", sym, err)
			st.Skipped++
			continue
		}

		chunks := chunksFor(info)
		if len(chunks) == 0 {
			log.Printf("ingest: skip %s: no chunks", sym)
			st.Skipped++
			continue
		}

		// One embedding request per symbol (not per chunk) — the efficiency win.
		texts := make([]string, len(chunks))
		for j, c := range chunks {
			texts[j] = c.Text
		}
		vectors, err := i.embed.EmbedBatch(ctx, texts)
		if err != nil {
			log.Printf("ingest: skip %s: embed: %v", sym, err)
			st.Skipped++
			continue
		}
		if len(vectors) != len(chunks) {
			log.Printf("ingest: skip %s: embed count mismatch: got %d want %d", sym, len(vectors), len(chunks))
			st.Skipped++
			continue
		}

		// Build points with deterministic IDs: re-ingest updates, never duplicates.
		points := make([]adapters.VectorPoint, len(chunks))
		for j, c := range chunks {
			points[j] = adapters.VectorPoint{
				ID:     pointID(sym, c.Field, j),
				Vector: vectors[j],
				Metadata: map[string]string{
					"symbol": sym,
					"name":   info.Name,
					"field":  c.Field,
					"text":   c.Text,
				},
			}
		}

		if err := i.store.Upsert(ctx, points); err != nil {
			// A store error is likely to affect all remaining symbols too,
			// but we still try the rest — the trigger model ingests one at a
			// time in practice.
			log.Printf("ingest: upsert %s: %v", sym, err)
			st.Skipped++
			continue
		}

		st.Symbols++
		st.Chunks += len(chunks)
		st.Embedded += len(vectors)
		st.Upserted += len(points)
	}

	return st, nil
}

// chunksFor turns one AssetInfo into embeddable text chunks, one per
// non-empty top-level field. Splitting by field gives the vector query a
// natural signal: a question about "what blockchain" matches the platform
// chunk; "max supply" matches the description chunk.
func chunksFor(info *AssetInfo) []chunk {
	var out []chunk

	// description: window if long so a single huge blob doesn't dominate.
	if d := strings.TrimSpace(info.Description); d != "" {
		for _, w := range windowText(d, 500, 100) {
			out = append(out, chunk{Text: w, Field: "description"})
		}
	}
	if info.Platform != "" {
		out = append(out, chunk{Text: "Platform: " + info.Platform, Field: "platform"})
	}
	if len(info.Tags) > 0 {
		out = append(out, chunk{Text: "Tags: " + strings.Join(info.Tags, ", "), Field: "tags"})
	}
	if info.Category != "" {
		out = append(out, chunk{Text: "Category: " + info.Category, Field: "category"})
	}
	if len(info.URLs) > 0 {
		var b strings.Builder
		for k, v := range info.URLs {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
		out = append(out, chunk{Text: strings.TrimSpace(b.String()), Field: "urls"})
	}
	return out
}

// windowText splits s into windows of at most size chars, overlapping by
// overlap chars. Only windows if len(s) > size; otherwise returns the whole
// string as a single window. The overlap keeps sentences from being cut
// mid-phrase at the boundary.
func windowText(s string, size, overlap int) []string {
	if len(s) <= size {
		return []string{s}
	}
	step := size - overlap
	if step <= 0 {
		step = size // degenerate: no overlap
	}
	var windows []string
	for start := 0; start < len(s); start += step {
		end := start + size
		if end > len(s) {
			end = len(s)
		}
		windows = append(windows, s[start:end])
		if end == len(s) {
			break
		}
	}
	return windows
}

// pointID is a deterministic, collision-resistant id for a chunk. Using
// symbol+field+index means re-ingesting the same coin upserts in place
// instead of creating duplicate vectors.
func pointID(symbol, field string, idx int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", symbol, field, idx)))
	return hex.EncodeToString(h[:])
}
