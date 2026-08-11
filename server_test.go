package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func authenticatedRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHealthz(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\\n")
	}
}

func TestNewServerRejectsMissingAPIToken(t *testing.T) {
	if _, err := NewServer(Config{DataDir: t.TempDir()}); err == nil {
		t.Fatal("NewServer accepted an empty API token")
	}
}

func TestStaticExcalidrawFrontendIsServedAtRoot(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("excalidraw frontend"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token", StaticDir: staticDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/some-client-route"} {
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "excalidraw frontend") {
			t.Fatalf("GET %s: status=%d body=%q", path, res.Code, res.Body.String())
		}
	}
}

func TestCreateWireframeRequiresAuthentication(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/wireframes", strings.NewReader(`{"title":"Login","document":{"type":"excalidraw","version":2,"elements":[]}}`))
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestCreateWireframeRejectsRawTokenWithoutBearerScheme(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/wireframes", strings.NewReader(`{"title":"Login","document":{"type":"excalidraw","version":2,"elements":[]}}`))
	req.Header.Set("Authorization", "test-token")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestCreateWireframeAccepts160UnicodeCharacters(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"title":    strings.Repeat("á", 160),
		"document": map[string]any{"type": "excalidraw", "version": 2, "elements": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusCreated, res.Body.String())
	}
}

func TestCreateWireframeRejectsTrailingJSON(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Login","document":{"type":"excalidraw","version":2,"elements":[]}} {}`)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestCreateWireframeRejectsUnsupportedVisibleElements(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Image","document":{"type":"excalidraw","version":2,"elements":[{"id":"image","type":"image"}]}}`)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "unsupported element type") {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestCreateWireframeRejectsPathologicalGeometry(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Huge","document":{"type":"excalidraw","version":2,"elements":[{"id":"line","type":"line","x":1e100,"points":[[0]]}]}}`)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
}

func TestCreateWireframeRejectsTooManyElements(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	elements := make([]json.RawMessage, maxDocumentElements+1)
	for i := range elements {
		elements[i] = json.RawMessage(`{"id":"element","type":"rectangle"}`)
	}
	payload, err := json.Marshal(map[string]any{
		"title":    "Too complex",
		"document": map[string]any{"type": "excalidraw", "version": 2, "elements": elements},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "too many elements") {
		t.Fatalf("body = %q, want element limit error", res.Body.String())
	}
}

func TestCreateWireframeRejectsTooManyPoints(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	points := make([][2]float64, maxDocumentPoints+1)
	element := map[string]any{"id": "line", "type": "line", "points": points}
	payload, err := json.Marshal(map[string]any{
		"title":    "Too complex",
		"document": map[string]any{"type": "excalidraw", "version": 2, "elements": []any{element}},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "too many points") {
		t.Fatalf("body = %q, want point limit error", res.Body.String())
	}
}

func TestCreateWireframeRejectsOversizedRenderedSVG(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"title": "Too large to preview",
		"document": map[string]any{
			"type":    "excalidraw",
			"version": 2,
			"elements": []any{map[string]any{
				"id": "large-text", "type": "text", "text": strings.Repeat("'", maxRenderedSVGBytes/2),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.ServeHTTP(res, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "too complex to render") {
		t.Fatalf("body = %q, want rendered preview limit error", res.Body.String())
	}
}

func TestCreateViewDownloadAndReloadWireframe(t *testing.T) {
	dataDir := t.TempDir()
	srv, err := NewServer(Config{DataDir: dataDir, APIToken: "test-token", PublicBaseURL: "https://excalidraw.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{
		"title":"Login Mobile",
		"slug":"login-mobile",
		"document":{
			"type":"excalidraw","version":2,"source":"test",
			"elements":[{"id":"rect-1","type":"rectangle","x":10,"y":20,"width":200,"height":100,"backgroundColor":"#a5d8ff","strokeColor":"#1e1e1e"}],
			"appState":{"viewBackgroundColor":"#ffffff"},"files":{}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/wireframes", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", res.Code, res.Body.String())
	}
	var created struct {
		ID          string `json:"id"`
		ViewURL     string `json:"viewUrl"`
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "login-mobile-") {
		t.Fatalf("id = %q, want login-mobile-*", created.ID)
	}
	if created.ViewURL != "https://excalidraw.example.com/w/"+created.ID {
		t.Fatalf("view URL = %q", created.ViewURL)
	}

	// A new server process must be able to render the persisted document.
	srv, err = NewServer(Config{DataDir: dataDir, APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	viewReq := httptest.NewRequest(http.MethodGet, "/w/"+created.ID, nil)
	viewRes := httptest.NewRecorder()
	srv.ServeHTTP(viewRes, viewReq)
	if viewRes.Code != http.StatusOK {
		t.Fatalf("view status = %d, body = %s", viewRes.Code, viewRes.Body.String())
	}
	for _, expected := range []string{"Login Mobile", "<svg", "rect-1", "#a5d8ff"} {
		if !strings.Contains(viewRes.Body.String(), expected) {
			t.Fatalf("viewer missing %q", expected)
		}
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/w/"+created.ID+".excalidraw", nil)
	downloadRes := httptest.NewRecorder()
	srv.ServeHTTP(downloadRes, downloadReq)
	if downloadRes.Code != http.StatusOK {
		t.Fatalf("download status = %d", downloadRes.Code)
	}
	var downloaded struct {
		Type     string            `json:"type"`
		Version  int               `json:"version"`
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(downloadRes.Body.Bytes(), &downloaded); err != nil {
		t.Fatal(err)
	}
	if downloaded.Type != "excalidraw" || downloaded.Version != 2 || len(downloaded.Elements) != 1 {
		t.Fatalf("unexpected document: %+v", downloaded)
	}
}

func TestPublicPreviewUsesPersistedAtomicRecord(t *testing.T) {
	dataDir := t.TempDir()
	srv, err := NewServer(Config{DataDir: dataDir, APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Cached preview","slug":"cached","document":{"type":"excalidraw","version":2,"elements":[{"id":"cached-rect","type":"rectangle","width":100,"height":100}]}}`)
	createdRes := httptest.NewRecorder()
	srv.ServeHTTP(createdRes, authenticatedRequest(http.MethodPost, "/api/wireframes", payload))
	if createdRes.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdRes.Code, createdRes.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdRes.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	recordData, err := os.ReadFile(filepath.Join(dataDir, created.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record wireframeRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(record.PreviewSVG, "cached-rect") || len(record.Document) == 0 {
		t.Fatalf("atomic record does not contain both preview and document: %+v", record)
	}

	reloaded, err := NewServer(Config{DataDir: dataDir, APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	viewed := httptest.NewRecorder()
	reloaded.ServeHTTP(viewed, httptest.NewRequest(http.MethodGet, "/w/"+created.ID, nil))
	if viewed.Code != http.StatusOK {
		t.Fatalf("view status = %d, body = %s", viewed.Code, viewed.Body.String())
	}
	for _, expected := range []string{"Cached preview", "cached-rect", "<svg"} {
		if !strings.Contains(viewed.Body.String(), expected) {
			t.Fatalf("cached viewer missing %q", expected)
		}
	}
}

func TestRenderConcurrencyIsBounded(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 2)
	renderer := boundedRenderer{
		slots: make(chan struct{}, 1),
		render: func(json.RawMessage) (template.HTML, error) {
			started <- struct{}{}
			<-gate
			return template.HTML("<svg></svg>"), nil
		},
	}

	done := make(chan error, 2)
	go func() { _, err := renderer.renderSVG(nil); done <- err }()
	<-started
	go func() { _, err := renderer.renderSVG(nil); done <- err }()
	select {
	case <-started:
		t.Fatal("second render started before the first render released its slot")
	case <-time.After(50 * time.Millisecond):
	}
	gate <- struct{}{}
	<-started
	gate <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateWireframeReplacesDocumentAndKeepsShareURL(t *testing.T) {
	dataDir := t.TempDir()
	srv, err := NewServer(Config{DataDir: dataDir, APIToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/wireframes", strings.NewReader(`{"title":"Versão 1","slug":"fluxo","document":{"type":"excalidraw","version":2,"elements":[]}}`))
	create.Header.Set("Authorization", "Bearer test-token")
	created := httptest.NewRecorder()
	srv.ServeHTTP(created, create)
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	updateBody := `{"title":"Versão 2","document":{"type":"excalidraw","version":2,"elements":[{"id":"safe-text","type":"text","x":20,"y":30,"text":"<script>alert(1)</script>","fontSize":20}]}}`
	update := httptest.NewRequest(http.MethodPut, "/api/wireframes/"+result.ID, strings.NewReader(updateBody))
	update.Header.Set("Authorization", "Bearer test-token")
	updated := httptest.NewRecorder()
	srv.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}

	view := httptest.NewRequest(http.MethodGet, "/w/"+result.ID, nil)
	viewed := httptest.NewRecorder()
	srv.ServeHTTP(viewed, view)
	if !strings.Contains(viewed.Body.String(), "Versão 2") {
		t.Fatal("viewer did not show updated title")
	}
	if strings.Contains(viewed.Body.String(), "<script>alert(1)</script>") || !strings.Contains(viewed.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("viewer did not safely escape element text")
	}
}

func TestWriteSlotRejectsInsteadOfQueuing(t *testing.T) {
	srv := &server{writeSlots: make(chan struct{}, 1)}
	if !srv.acquireWriteSlot() {
		t.Fatal("first write slot acquisition failed")
	}
	defer srv.releaseWriteSlot()
	if srv.acquireWriteSlot() {
		t.Fatal("second acquisition queued instead of being rejected")
	}
}

func TestReadSlotRejectsInsteadOfQueuing(t *testing.T) {
	srv := &server{readSlots: make(chan struct{}, 1)}
	if !srv.acquireReadSlot() {
		t.Fatal("first read slot acquisition failed")
	}
	defer srv.releaseReadSlot()
	if srv.acquireReadSlot() {
		t.Fatal("second acquisition queued instead of being rejected")
	}
}

func TestPublicReadReturnsServiceUnavailableWhenSlotsAreFull(t *testing.T) {
	srv := &server{cfg: Config{DataDir: t.TempDir()}, readSlots: make(chan struct{}, 1)}
	if !srv.acquireReadSlot() {
		t.Fatal("could not occupy read slot")
	}
	defer srv.releaseReadSlot()
	req := httptest.NewRequest(http.MethodGet, "/w/missing", nil)
	req.SetPathValue("id", "missing")
	res := httptest.NewRecorder()
	srv.viewWireframe(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
}

func TestLoadRejectsOversizedPersistedRecord(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "oversized.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPersistedRecordBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	srv := &server{cfg: Config{DataDir: dataDir}}
	if _, err := srv.load("oversized"); !errors.Is(err, errPersistedRecordTooLarge) {
		t.Fatalf("load error = %v, want %v", err, errPersistedRecordTooLarge)
	}
}
