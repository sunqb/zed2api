package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed webui/dist
var webuiFS embed.FS

func runServer(port int) error {
	initProxy()

	mgr := newAccountManager()
	if err := mgr.loadFromFile(); err != nil {
		fmt.Printf("[server] warning: could not load accounts.json: %v\n", err)
	}

	mux := http.NewServeMux()

	// ── API routes ────────────────────────────────────────────────────────────
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleCompletions(w, r, mgr, false)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handleCompletions(w, r, mgr, true)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		handleModels(w, r, mgr)
	})
	mux.HandleFunc("/zed/accounts", func(w http.ResponseWriter, r *http.Request) {
		handleAccounts(w, r, mgr)
	})
	mux.HandleFunc("/zed/billing", func(w http.ResponseWriter, r *http.Request) {
		handleBilling(w, r, mgr)
	})

	// ── WebUI static files ────────────────────────────────────────────────────
	distFS, err := fs.Sub(webuiFS, "webui/dist")
	if err != nil {
		return fmt.Errorf("embed webui/dist: %w", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	mux.Handle("/", fileServer)

	// ── CORS + logging middleware ─────────────────────────────────────────────
	handler := corsMiddleware(loggingMiddleware(mux))

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("[zed2api] listening on http://0.0.0.0%s\n", addr)
	return http.ListenAndServe(addr, handler)
}

// ── Middleware ────────────────────────────────────────────────────────────────

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, anthropic-version, anthropic-beta")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lw, r)
		fmt.Printf("[server] %s %s %d %s\n", r.Method, r.URL.Path, lw.status, time.Since(start))
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.status = code
	lw.ResponseWriter.WriteHeader(code)
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// handleCompletions handles both /v1/chat/completions (isAnthropic=false)
// and /v1/messages (isAnthropic=true).
func handleCompletions(w http.ResponseWriter, r *http.Request, mgr *AccountManager, isAnthropic bool) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Detect streaming
	var peek struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &peek)

	if peek.Stream {
		handleStreamProxy(w, body, isAnthropic, mgr)
		return
	}

	// Non-streaming: try accounts with failover
	accounts := mgr.getOrderedAccounts()
	if len(accounts) == 0 {
		jsonError(w, "no accounts configured", http.StatusBadRequest)
		return
	}

	payload, err := buildZedPayload(body, isAnthropic)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to build payload: %v", err), http.StatusBadRequest)
		return
	}

	model := extractModelFromBody(body)
	model = normalizeModelName(model)

	for _, acc := range accounts {
		jwt, err := getToken(acc)
		if err != nil {
			fmt.Printf("[server] getToken for '%s' failed: %v\n", acc.Name, err)
			continue
		}

		respBytes, status, err := doZedRequest(jwt, payload)
		if err != nil || status != http.StatusOK {
			fmt.Printf("[server] account '%s' returned %d: %v\n", acc.Name, status, err)
			continue
		}

		// Convert response
		var converted []byte
		if isAnthropic {
			converted, err = convertToAnthropic(string(respBytes), model)
		} else {
			converted, err = convertToOpenAI(string(respBytes), model)
		}
		if err != nil {
			fmt.Printf("[server] conversion error: %v\n", err)
			continue
		}

		mgr.failover(acc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(converted)
		return
	}

	jsonError(w, "all accounts failed", http.StatusBadGateway)
}

// handleModels returns available models from the current account.
func handleModels(w http.ResponseWriter, r *http.Request, mgr *AccountManager) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	acc := mgr.getCurrent()
	if acc == nil {
		// Return a minimal static list
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []any{},
		})
		return
	}

	data, err := fetchModels(acc)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to fetch models: %v", err), http.StatusBadGateway)
		return
	}

	// Wrap raw Zed model list into OpenAI format if needed
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw["data"] == nil {
		// Zed returns array directly; wrap it
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleAccounts returns the account list as JSON.
func handleAccounts(w http.ResponseWriter, r *http.Request, mgr *AccountManager) {
	switch r.Method {
	case http.MethodGet:
		data, err := mgr.listJSON()
		if err != nil {
			jsonError(w, "failed to list accounts", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)

	case http.MethodPost:
		// Switch active account: POST /zed/accounts {"name": "foo"}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
			jsonError(w, "missing 'name' field", http.StatusBadRequest)
			return
		}
		if !mgr.switchTo(req.Name) {
			jsonError(w, fmt.Sprintf("account '%s' not found", req.Name), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "current": req.Name})

	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleBilling returns billing usage for the current account.
func handleBilling(w http.ResponseWriter, r *http.Request, mgr *AccountManager) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	acc := mgr.getCurrent()
	if acc == nil {
		jsonError(w, "no accounts configured", http.StatusBadRequest)
		return
	}
	data, err := fetchBillingUsage(acc)
	if err != nil {
		jsonError(w, fmt.Sprintf("billing fetch failed: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    strings.ToLower(strings.ReplaceAll(http.StatusText(code), " ", "_")),
		},
	})
	w.Write(enc)
}
