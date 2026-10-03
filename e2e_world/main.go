// Command e2e_world is a disposable, self-contained stand-in environment for
// driving the real crypto-telegram-notificator binary end-to-end without any
// real external service. It provides:
//
//   - a minimal Firestore v1 gRPC server (the FIRESTORE_EMULATOR_HOST seam),
//   - a MITM HTTPS proxy (HTTPS_PROXY + SSL_CERT_FILE) faking api.telegram.org
//     and pro-api.coinmarketcap.com,
//   - a fake OpenAI-compatible /v1/chat/completions endpoint for LLM intents,
//   - a miniredis server,
//   - /_evidence/* dumps of recorded traffic and persisted state, and
//     /_control/* endpoints to steer the world (prices, failures, seeding).
//
// Everything is in-memory and bound to 127.0.0.1; it exists only for this run.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Fake Firestore v1 gRPC server
// ---------------------------------------------------------------------------

type fakeFirestore struct {
	pb.UnimplementedFirestoreServer
	mu   sync.Mutex
	docs map[string]*pb.Document // full resource name -> document
}

func newFakeFirestore() *fakeFirestore {
	return &fakeFirestore{docs: map[string]*pb.Document{}}
}

// createTime reports the stored document's creation time, or now for a
// document being created by this commit.
func createTime(s *fakeFirestore, name string) *timestamppb.Timestamp {
	if old, ok := s.docs[name]; ok && old.CreateTime != nil {
		return old.CreateTime
	}
	return timestamppb.Now()
}

func (s *fakeFirestore) Commit(ctx context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &pb.CommitResponse{CommitTime: timestamppb.Now()}
	for _, w := range req.Writes {
		switch op := w.Operation.(type) {
		case *pb.Write_Update:
			name := op.Update.Name
			_, exists := s.docs[name]
			if pre := w.CurrentDocument; pre != nil {
				if e, ok := pre.ConditionType.(*pb.Precondition_Exists); ok {
					if e.Exists && !exists {
						return nil, status.Errorf(codes.NotFound, "document %q not found", name)
					}
					if !e.Exists && exists {
						return nil, status.Errorf(codes.AlreadyExists, "document %q already exists", name)
					}
				}
			}
			fields := map[string]*pb.Value{}
			if mask := w.UpdateMask; mask != nil && len(mask.FieldPaths) > 0 {
				if old, ok := s.docs[name]; ok {
					for k, v := range old.Fields {
						fields[k] = v
					}
				}
				for _, p := range mask.FieldPaths {
					if v, ok := op.Update.Fields[p]; ok {
						fields[p] = v
					} else {
						delete(fields, p)
					}
				}
			} else {
				for k, v := range op.Update.Fields {
					fields[k] = v
				}
			}
			s.docs[name] = &pb.Document{Name: name, Fields: fields, CreateTime: createTime(s, name), UpdateTime: timestamppb.Now()}
		case *pb.Write_Delete:
			delete(s.docs, op.Delete)
		default:
			return nil, status.Error(codes.Unimplemented, "write op not supported by e2e world")
		}
		resp.WriteResults = append(resp.WriteResults, &pb.WriteResult{UpdateTime: timestamppb.Now()})
	}
	return resp, nil
}

func (s *fakeFirestore) RunQuery(req *pb.RunQueryRequest, stream pb.Firestore_RunQueryServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sq := req.GetStructuredQuery()
	coll := "alerts"
	if len(sq.GetFrom()) > 0 {
		coll = sq.GetFrom()[0].GetCollectionId()
	}
	parent := req.GetParent() // "projects/P/databases/(default)/documents"
	prefix := parent + "/" + coll + "/"

	var names []string
	for name := range s.docs {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		doc := s.docs[name]
		if !matchesFilter(sq.GetWhere(), doc) {
			continue
		}
		if err := stream.Send(&pb.RunQueryResponse{Document: doc, ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return nil
}

func matchesFilter(where *pb.StructuredQuery_Filter, doc *pb.Document) bool {
	if where == nil {
		return true
	}
	switch f := where.FilterType.(type) {
	case *pb.StructuredQuery_Filter_FieldFilter:
		ff := f.FieldFilter
		field := ff.GetField().GetFieldPath()
		val, ok := doc.Fields[field]
		if !ok {
			return false
		}
		switch ff.GetOp() {
		case pb.StructuredQuery_FieldFilter_EQUAL:
			return valueEqual(val, ff.GetValue())
		default:
			return false
		}
	default:
		return false
	}
}

func valuePlainJSON(v *pb.Value) any {
	switch k := v.GetValueType().(type) {
	case *pb.Value_IntegerValue:
		return k.IntegerValue
	case *pb.Value_StringValue:
		return k.StringValue
	case *pb.Value_DoubleValue:
		return k.DoubleValue
	case *pb.Value_BooleanValue:
		return k.BooleanValue
	case *pb.Value_NullValue:
		return nil
	default:
		return v.String()
	}
}

func valueEqual(a, b *pb.Value) bool {
	if a == nil || b == nil {
		return a == b
	}
	return valuePlainJSON(a) == valuePlainJSON(b)
}

func (s *fakeFirestore) BatchGetDocuments(req *pb.BatchGetDocumentsRequest, stream pb.Firestore_BatchGetDocumentsServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range req.GetDocuments() {
		if doc, ok := s.docs[name]; ok {
			if err := stream.Send(&pb.BatchGetDocumentsResponse{
				Result:   &pb.BatchGetDocumentsResponse_Found{Found: doc},
				ReadTime: timestamppb.Now(),
			}); err != nil {
				return err
			}
		} else {
			if err := stream.Send(&pb.BatchGetDocumentsResponse{
				Result:   &pb.BatchGetDocumentsResponse_Missing{Missing: name},
				ReadTime: timestamppb.Now(),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// World state: recorded traffic, steering knobs
// ---------------------------------------------------------------------------

type telegramMsg struct {
	Time   string `json:"time"`
	Method string `json:"method"`
	Token  string `json:"token"`
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type world struct {
	mu sync.Mutex

	botToken     string
	telegramFail bool // when true, sendMessage answers 500

	prices map[string]float64

	llmIntent map[string]any // the content JSON for /v1/chat/completions

	telegramMessages []telegramMsg
	telegramAttempts int
	telegramFailures int
	getMeCalls       int

	cmcRequests []map[string]any
	llmRequests []map[string]any

	fs *fakeFirestore
	mr *miniredis.Miniredis
}

var w *world

func (w *world) lock() *sync.Mutex { return &w.mu }

// ---------------------------------------------------------------------------
// MITM proxy + plain-HTTP world endpoints
// ---------------------------------------------------------------------------

type singleListener struct {
	conn net.Conn
	done bool
}

func (l *singleListener) Accept() (net.Conn, error) {
	if l.done {
		return nil, io.EOF
	}
	l.done = true
	return l.conn, nil
}
func (l *singleListener) Close() error   { return nil }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }

type caBundle struct {
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	certPEM []byte
	mu      sync.Mutex
	leaves  map[string]*tls.Certificate
}

func newCA() (*caBundle, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2e-world-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &caBundle{caCert: cert, caKey: key, certPEM: pemBytes, leaves: map[string]*tls.Certificate{}}, nil
}

func (c *caBundle) leafFor(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cert, ok := c.leaves[host]; ok {
		return cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	c.leaves[host] = cert
	return cert, nil
}

type replayConn struct {
	net.Conn
	buf []byte
	off int
}

func (c *replayConn) Read(b []byte) (int, error) {
	if c.off < len(c.buf) {
		n := copy(b, c.buf[c.off:])
		c.off += n
		return n, nil
	}
	return c.Conn.Read(b)
}

func serveConn(h http.Handler, c net.Conn, replay []byte) {
	srv := &http.Server{Handler: h}
	if replay != nil {
		c = &replayConn{Conn: c, buf: replay}
	}
	_ = srv.Serve(&singleListener{conn: c})
}

func proxyLoop(ln net.Listener, h http.Handler, ca *caBundle) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			br := bufio.NewReader(conn)
			line, err := br.ReadString('\n')
			if err != nil {
				conn.Close()
				return
			}
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "CONNECT") {
				// Drain the remaining request headers.
				for {
					hdr, err := br.ReadString('\n')
					if err != nil {
						conn.Close()
						return
					}
					if strings.TrimSpace(hdr) == "" {
						break
					}
				}
				fields := strings.Fields(strings.TrimSpace(line))
				target := ""
				if len(fields) >= 2 {
					target = fields[1]
				}
				host := strings.TrimSuffix(target, ":443")
				if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
					conn.Close()
					return
				}
				tlsCfg := &tls.Config{
					GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
						name := hello.ServerName
						if name == "" {
							name = host
						}
						return ca.leafFor(name)
					},
					NextProtos: []string{"http/1.1"}, // pin HTTP/1.1; h2 handled by grpc server separately
				}
				tlsConn := tls.Server(conn, tlsCfg)
				serveConn(h, tlsConn, nil)
				return
			}
			// Plain HTTP request (the world's own endpoints / fake LLM).
			// The bufio.Reader consumed the whole request into its buffer when
			// reading the first line, so the replay must restore not just that
			// line but every byte that followed it in the buffer; anything past
			// the buffer is still in the socket and arrives via the live conn.
			rest, _ := br.Peek(br.Buffered())
			replay := append([]byte(line), rest...)
			serveConn(h, conn, replay)
		}(conn)
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers for the fakes
// ---------------------------------------------------------------------------

func main() {
	evDir := os.Getenv("E2E_EVIDENCE_DIR")
	if evDir == "" {
		log.Fatal("E2E_EVIDENCE_DIR not set")
	}

	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	mr, err := miniredis.Run()
	if err != nil {
		log.Fatal(err)
	}

	fs := newFakeFirestore()
	w = &world{
		botToken: "123456:e2e-test-token",
		prices:   map[string]float64{"BTC": 60000.0},
		fs:       fs,
		mr:       mr,
	}

	grpcSrv := grpc.NewServer()
	pb.RegisterFirestoreServer(grpcSrv, fs)
	go func() {
		if err := grpcSrv.Serve(grpcLn); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	ca, err := newCA()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(evDir+"/world-ca.pem", ca.certPEM, 0o644); err != nil {
		log.Fatal(err)
	}

	handler := http.NewServeMux()

	// --- api.telegram.org (Bot API) ---
	// The Bot API path is /bot<token>/<method> — there is no slash between
	// "bot" and the token, so the handler is a catch-all that dispatches on
	// the /bot prefix.
	handler.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot") {
			http.NotFound(rw, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/bot")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			http.Error(rw, "bad bot api path", http.StatusNotFound)
			return
		}
		token, method := parts[0], parts[1]
		w.mu.Lock()
		w.telegramAttempts++
		fail := w.telegramFail
		w.mu.Unlock()
		switch method {
		case "getMe":
			w.mu.Lock()
			w.getMeCalls++
			w.mu.Unlock()
			writeJSON(rw, map[string]any{
				"ok": true,
				"result": map[string]any{
					"id": 7, "is_bot": true, "first_name": "e2e_bot", "username": "e2e_test_bot", "can_join_groups": true,
				},
			})
			return
		case "sendMessage":
			if err := r.ParseForm(); err != nil {
				http.Error(rw, "bad form", http.StatusBadRequest)
				return
			}
			chatID := r.Form.Get("chat_id")
			text := r.Form.Get("text")
			w.mu.Lock()
			if fail {
				w.telegramFailures++
				w.mu.Unlock()
				rw.WriteHeader(http.StatusInternalServerError)
				writeJSON(rw, map[string]any{"ok": false, "error_code": 500, "description": "e2e world: telegram down"})
				return
			}
			w.telegramMessages = append(w.telegramMessages, telegramMsg{
				Time: time.Now().Format(time.RFC3339Nano), Method: "sendMessage", Token: token,
				ChatID: chatID, Text: text,
			})
			w.mu.Unlock()
			// chat.id must be a JSON number: the BotAPI client unmarshals it
			// into int64 and treats a parse failure as a failed send.
			cid, _ := strconv.ParseInt(chatID, 10, 64)
			writeJSON(rw, map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": len(w.telegramMessages), "text": text,
					"chat": map[string]any{"id": cid, "type": "private"}, "date": time.Now().Unix(),
				},
			})
			return
		default:
			writeJSON(rw, map[string]any{"ok": true, "result": map[string]any{}})
			return
		}
	})

	// --- pro-api.coinmarketcap.com ---
	handler.HandleFunc("/v1/cryptocurrency/quotes/latest", func(rw http.ResponseWriter, r *http.Request) {
		symbolsParam := r.URL.Query().Get("symbol")
		symbols := []string{}
		if symbolsParam != "" {
			for _, s := range strings.Split(symbolsParam, ",") {
				symbols = append(symbols, strings.TrimSpace(s))
			}
		}
		w.mu.Lock()
		apiKey := r.Header.Get("X-CMC_PRO_API_KEY")
		w.cmcRequests = append(w.cmcRequests, map[string]any{
			"time": time.Now().Format(time.RFC3339Nano), "symbols": symbols, "api_key": apiKey,
		})
		prices := w.prices
		w.mu.Unlock()
		data := map[string]any{}
		for _, s := range symbols {
			data[s] = map[string]any{"quote": map[string]any{"USD": map[string]any{"price": prices[s]}}}
		}
		writeJSON(rw, map[string]any{"data": data})
	})

	// --- fake OpenAI-compatible LLM ---
	handler.HandleFunc("/v1/chat/completions", func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		intent := w.llmIntent
		w.llmRequests = append(w.llmRequests, map[string]any{
			"time": time.Now().Format(time.RFC3339Nano), "body": body,
		})
		w.mu.Unlock()
		if intent == nil {
			intent = map[string]any{
				"symbol": "BTC", "target_price": 100000.0, "direction": "above",
				"confidence": 0.9, "explanation": "user wants an alert when Bitcoin rises above $100,000",
			}
		}
		content, _ := json.Marshal(intent)
		writeJSON(rw, map[string]any{
			"id": "chatcmpl-e2e", "object": "chat.completion", "created": time.Now().Unix(),
			"model": "e2e-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": string(content)},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	})

	// --- evidence dumps ---
	handler.HandleFunc("/_evidence/telegram", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{
			"messages": w.telegramMessages, "attempts": w.telegramAttempts,
			"failures": w.telegramFailures, "getMe_calls": w.getMeCalls,
		})
	})
	handler.HandleFunc("/_evidence/cmc", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{"requests": w.cmcRequests, "count": len(w.cmcRequests)})
	})
	handler.HandleFunc("/_evidence/llm", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{"requests": w.llmRequests, "count": len(w.llmRequests)})
	})
	handler.HandleFunc("/_evidence/redis", func(rw http.ResponseWriter, r *http.Request) {
		keys := map[string]any{}
		for _, k := range w.mr.Keys() {
			v, _ := w.mr.Get(k)
			ttl := w.mr.TTL(k).String()
			keys[k] = map[string]any{"value": v, "ttl": ttl}
		}
		writeJSON(rw, map[string]any{"keys": keys})
	})
	handler.HandleFunc("/_evidence/firestore", func(rw http.ResponseWriter, r *http.Request) {
		w.fs.mu.Lock()
		defer w.fs.mu.Unlock()
		docs := map[string]map[string]any{}
		for name, doc := range w.fs.docs {
			fields := map[string]any{}
			for k, v := range doc.Fields {
				fields[k] = valuePlainJSON(v)
			}
			docs[name] = fields
		}
		writeJSON(rw, map[string]any{"docs": docs})
	})
	handler.HandleFunc("/_evidence/all", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{
			"telegram_messages": w.telegramMessages, "telegram_attempts": w.telegramAttempts,
			"telegram_failures": w.telegramFailures, "getMe_calls": w.getMeCalls,
			"cmc_requests": w.cmcRequests, "llm_requests": w.llmRequests,
		})
	})

	// --- control knobs ---
	handler.HandleFunc("/_control/telegram", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Fail *bool `json:"fail"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Fail == nil {
			http.Error(rw, `{"fail": true|false}`, http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.telegramFail = *body.Fail
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"ok": true, "telegram_fail": *body.Fail})
	})
	handler.HandleFunc("/_control/prices", func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]float64
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.prices = body
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"ok": true, "prices": body})
	})
	handler.HandleFunc("/_control/llm", func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.llmIntent = body
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"ok": true, "llm_intent": body})
	})
	handler.HandleFunc("/_control/seed", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			DocID              string  `json:"doc_id"`
			UserID             int64   `json:"user_id"`
			CoinID             string  `json:"coin_id"`
			TargetPrice        float64 `json:"target_price"`
			Type               string  `json:"type"`
			DeliveryFailedAtMs int64   `json:"delivery_failed_at_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		name := "projects/no-mistakes-e2e/databases/(default)/documents/alerts/" + body.DocID
		fields := map[string]*pb.Value{
			"user_id":      {ValueType: &pb.Value_IntegerValue{IntegerValue: body.UserID}},
			"coin_id":      {ValueType: &pb.Value_StringValue{StringValue: body.CoinID}},
			"target_price": {ValueType: &pb.Value_DoubleValue{DoubleValue: body.TargetPrice}},
			"created_at":   {ValueType: &pb.Value_IntegerValue{IntegerValue: time.Now().Unix()}},
			"type":         {ValueType: &pb.Value_StringValue{StringValue: body.Type}},
		}
		if body.DeliveryFailedAtMs != 0 {
			fields["delivery_failed_at"] = &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: body.DeliveryFailedAtMs}}
		}
		w.fs.mu.Lock()
		w.fs.docs[name] = &pb.Document{Name: name, Fields: fields, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}
		w.fs.mu.Unlock()
		writeJSON(rw, map[string]any{"ok": true, "seeded": name})
	})
	handler.HandleFunc("/_control/clear", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.telegramMessages = nil
		w.telegramAttempts = 0
		w.telegramFailures = 0
		w.telegramFail = false
		w.getMeCalls = 0
		w.cmcRequests = nil
		w.llmRequests = nil
		w.mu.Unlock()
		w.fs.mu.Lock()
		w.fs.docs = map[string]*pb.Document{}
		w.fs.mu.Unlock()
		w.mr.FlushAll()
		writeJSON(rw, map[string]any{"ok": true, "cleared": "telegram+cmc+llm+firestore+redis"})
	})

	go proxyLoop(proxyLn, handler, ca)

	worldFile := map[string]any{
		"proxy":     proxyLn.Addr().String(),
		"firestore": grpcLn.Addr().String(),
		"redis":     mr.Addr(),
		"redis_url": "redis://" + mr.Addr(),
		"ca_pem":    evDir + "/world-ca.pem",
		"bot_token": w.botToken,
		"proxy_url": "http://" + proxyLn.Addr().String(),
	}
	raw, _ := json.MarshalIndent(worldFile, "", "  ")
	if err := os.WriteFile(evDir+"/world.json", raw, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("e2e world ready: %s", raw)
	select {} // serve forever
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}
