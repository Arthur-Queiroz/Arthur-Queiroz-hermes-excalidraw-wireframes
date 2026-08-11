package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDocumentBytes        = 5 << 20
	maxDocumentElements     = 500
	maxDocumentPoints       = 10_000
	maxRenderedSVGBytes     = 2 << 20
	maxPersistedRecordBytes = maxDocumentBytes + 2*maxRenderedSVGBytes + (64 << 10)
	renderConcurrency       = 1
	readConcurrency         = 2
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,100}$`)
var errPersistedRecordTooLarge = errors.New("persisted wireframe record is too large")

type Config struct {
	DataDir       string
	APIToken      string
	PublicBaseURL string
	StaticDir     string
}

type server struct {
	cfg        Config
	mux        *http.ServeMux
	renderer   boundedRenderer
	writeSlots chan struct{}
	readSlots  chan struct{}
}

type boundedRenderer struct {
	slots  chan struct{}
	render func(json.RawMessage) (template.HTML, error)
}

func (r boundedRenderer) renderSVG(document json.RawMessage) (template.HTML, error) {
	r.slots <- struct{}{}
	defer func() { <-r.slots }()
	return r.render(document)
}

type wireframeRecord struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Document   json.RawMessage `json:"document"`
	PreviewSVG string          `json:"previewSvg"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type createRequest struct {
	Title    string          `json:"title"`
	Slug     string          `json:"slug"`
	Document json.RawMessage `json:"document"`
}

func NewServer(cfg Config) (http.Handler, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.APIToken == "" {
		return nil, errors.New("API token is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := checkDataDirWritable(cfg.DataDir); err != nil {
		return nil, fmt.Errorf("data directory is not writable: %w", err)
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	s := &server{
		cfg:        cfg,
		mux:        http.NewServeMux(),
		writeSlots: make(chan struct{}, renderConcurrency),
		readSlots:  make(chan struct{}, readConcurrency),
		renderer: boundedRenderer{
			slots:  make(chan struct{}, renderConcurrency),
			render: renderSVG,
		},
	}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /api/wireframes", s.createWireframe)
	s.mux.HandleFunc("PUT /api/wireframes/{id}", s.updateWireframe)
	s.mux.HandleFunc("GET /w/{id}", s.viewWireframe)
	if cfg.StaticDir != "" {
		s.mux.Handle("GET /", spaHandler(cfg.StaticDir))
	}
	return securityHeaders(s.mux), nil
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *server) createWireframe(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !s.acquireWriteSlot() {
		writeJSONError(w, http.StatusServiceUnavailable, "wireframe renderer is busy")
		return
	}
	defer s.releaseWriteSlot()
	request, err := decodeCreateRequest(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" || utf8.RuneCountInString(request.Title) > 160 {
		writeJSONError(w, http.StatusBadRequest, "title must contain 1 to 160 characters")
		return
	}
	if err := validateDocument(request.Document); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	svg, err := s.renderer.renderSVG(request.Document)
	if err != nil || len(svg) > maxRenderedSVGBytes {
		writeJSONError(w, http.StatusBadRequest, "document is too complex to render")
		return
	}
	id, err := newWireframeID(request.Slug)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not generate wireframe ID")
		return
	}
	now := time.Now().UTC()
	record := wireframeRecord{ID: id, Title: request.Title, Document: request.Document, CreatedAt: now, UpdatedAt: now}
	if err := s.save(record, svg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not persist wireframe")
		return
	}
	viewPath := "/w/" + id
	writeJSON(w, http.StatusCreated, map[string]string{
		"id": id, "viewUrl": s.cfg.PublicBaseURL + viewPath,
		"downloadUrl": s.cfg.PublicBaseURL + viewPath + ".excalidraw",
	})
}

func (s *server) updateWireframe(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !s.acquireWriteSlot() {
		writeJSONError(w, http.StatusServiceUnavailable, "wireframe renderer is busy")
		return
	}
	defer s.releaseWriteSlot()
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	record, err := s.load(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "could not load wireframe")
		return
	}
	request, err := decodeCreateRequest(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" || utf8.RuneCountInString(request.Title) > 160 {
		writeJSONError(w, http.StatusBadRequest, "title must contain 1 to 160 characters")
		return
	}
	if err := validateDocument(request.Document); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	svg, err := s.renderer.renderSVG(request.Document)
	if err != nil || len(svg) > maxRenderedSVGBytes {
		writeJSONError(w, http.StatusBadRequest, "document is too complex to render")
		return
	}
	record.Title = request.Title
	record.Document = request.Document
	record.UpdatedAt = time.Now().UTC()
	if err := s.save(record, svg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not persist wireframe")
		return
	}
	viewPath := "/w/" + id
	writeJSON(w, http.StatusOK, map[string]string{
		"id": id, "viewUrl": s.cfg.PublicBaseURL + viewPath,
		"downloadUrl": s.cfg.PublicBaseURL + viewPath + ".excalidraw",
	})
}

func (s *server) viewWireframe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	download := strings.HasSuffix(id, ".excalidraw")
	id = strings.TrimSuffix(id, ".excalidraw")
	if !validID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !s.acquireReadSlot() {
		writeJSONError(w, http.StatusServiceUnavailable, "wireframe reader is busy")
		return
	}
	defer s.releaseReadSlot()
	record, err := s.load(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "could not load wireframe", http.StatusInternalServerError)
		return
	}
	if download {
		w.Header().Set("Content-Type", "application/vnd.excalidraw+json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.excalidraw"`, id))
		_, _ = w.Write(record.Document)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := viewerTemplate.Execute(w, map[string]any{
		"Title": record.Title, "SVG": template.HTML(record.PreviewSVG), "DownloadURL": "/w/" + id + ".excalidraw",
	}); err != nil {
		http.Error(w, "could not render page", http.StatusInternalServerError)
	}
}

func (s *server) authorized(r *http.Request) bool {
	if s.cfg.APIToken == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	provided := strings.TrimPrefix(header, "Bearer ")
	return len(provided) == len(s.cfg.APIToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIToken)) == 1
}

func (s *server) acquireWriteSlot() bool {
	select {
	case s.writeSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *server) releaseWriteSlot() {
	<-s.writeSlots
}

func (s *server) acquireReadSlot() bool {
	select {
	case s.readSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *server) releaseReadSlot() {
	<-s.readSlots
}

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request createRequest
	if err := decoder.Decode(&request); err != nil {
		return createRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return createRequest{}, errors.New("request must contain exactly one JSON object")
	}
	return request, nil
}

func (s *server) save(record wireframeRecord, svg template.HTML) error {
	record.PreviewSVG = string(svg)
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return err
	}
	if buffer.Len() > maxPersistedRecordBytes {
		return errPersistedRecordTooLarge
	}
	return atomicWrite(filepath.Join(s.cfg.DataDir, record.ID+".json"), buffer.Bytes())
}

func atomicWrite(destination string, data []byte) error {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".wireframe-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func checkDataDirWritable(directory string) error {
	probe, err := os.CreateTemp(directory, ".writable-probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (s *server) load(id string) (wireframeRecord, error) {
	file, err := os.Open(filepath.Join(s.cfg.DataDir, id+".json"))
	if err != nil {
		return wireframeRecord{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return wireframeRecord{}, err
	}
	if info.Size() > maxPersistedRecordBytes {
		return wireframeRecord{}, errPersistedRecordTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPersistedRecordBytes+1))
	if err != nil {
		return wireframeRecord{}, err
	}
	if len(data) > maxPersistedRecordBytes {
		return wireframeRecord{}, errPersistedRecordTooLarge
	}
	var record wireframeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return wireframeRecord{}, err
	}
	return record, nil
}

func validateDocument(raw json.RawMessage) error {
	var document excalidrawDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return errors.New("document must be valid JSON")
	}
	if document.Type != "excalidraw" || document.Version != 2 || document.Elements == nil {
		return errors.New("document must be an Excalidraw v2 document with an elements array")
	}
	if len(document.Elements) > maxDocumentElements {
		return fmt.Errorf("document has too many elements (maximum %d)", maxDocumentElements)
	}
	totalPoints := 0
	for _, rawElement := range document.Elements {
		var element struct {
			Type      string      `json:"type"`
			IsDeleted bool        `json:"isDeleted"`
			Angle     float64     `json:"angle"`
			X         float64     `json:"x"`
			Y         float64     `json:"y"`
			Width     float64     `json:"width"`
			Height    float64     `json:"height"`
			Stroke    float64     `json:"strokeWidth"`
			FontSize  float64     `json:"fontSize"`
			Opacity   *float64    `json:"opacity"`
			Points    [][]float64 `json:"points"`
		}
		if err := json.Unmarshal(rawElement, &element); err != nil {
			return errors.New("document contains an invalid element")
		}
		totalPoints += len(element.Points)
		if totalPoints > maxDocumentPoints {
			return fmt.Errorf("document has too many points (maximum %d)", maxDocumentPoints)
		}
		if element.IsDeleted {
			continue
		}
		switch element.Type {
		case "rectangle", "ellipse", "diamond", "text", "label", "arrowLabel", "line", "arrow":
		default:
			return fmt.Errorf("unsupported element type %q", element.Type)
		}
		if element.Angle != 0 {
			return errors.New("rotated elements are not supported in previews")
		}
		if !boundedNumber(element.X, -1_000_000, 1_000_000) || !boundedNumber(element.Y, -1_000_000, 1_000_000) ||
			!boundedNumber(element.Width, 0, 1_000_000) || !boundedNumber(element.Height, 0, 1_000_000) ||
			!boundedNumber(element.Stroke, 0, 1_000) || !boundedNumber(element.FontSize, 0, 1_000) {
			return errors.New("element geometry is outside supported bounds")
		}
		if element.Opacity != nil && !boundedNumber(*element.Opacity, 0, 100) {
			return errors.New("element opacity must be between 0 and 100")
		}
		for _, point := range element.Points {
			if len(point) != 2 || !boundedNumber(point[0], -1_000_000, 1_000_000) || !boundedNumber(point[1], -1_000_000, 1_000_000) {
				return errors.New("element contains an invalid point")
			}
		}
	}
	return nil
}

func boundedNumber(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func newWireframeID(requestedSlug string) (string, error) {
	bytes := make([]byte, 10)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	slug := slugify(requestedSlug)
	if slug == "" {
		slug = "wireframe"
	}
	token := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes))
	return slug + "-" + token, nil
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastDash = false
		} else if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
		if builder.Len() >= 48 {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/w/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func spaHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}

var viewerTemplate = template.Must(template.New("viewer").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Wireframe</title><style>
:root{color-scheme:light;font-family:Inter,system-ui,sans-serif;background:#f8f9fa;color:#1b1b1f}*{box-sizing:border-box}body{margin:0}.bar{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:12px 16px;background:#fff;border-bottom:1px solid #e5e7eb;position:sticky;top:0;z-index:2}.bar h1{font-size:16px;margin:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.bar a{background:#6965db;color:#fff;text-decoration:none;padding:9px 12px;border-radius:8px;font-weight:650;font-size:14px}.canvas{min-height:calc(100dvh - 62px);padding:16px;display:grid;place-items:center;overflow:auto}.sheet{width:min(1200px,100%);background:#fff;border-radius:12px;box-shadow:0 8px 30px #00000014;padding:12px}.sheet svg{display:block;width:100%;height:auto;max-height:calc(100dvh - 110px)}
</style></head><body><header class="bar"><h1>{{.Title}}</h1><a href="{{.DownloadURL}}">Baixar .excalidraw</a></header><main class="canvas"><div class="sheet">{{.SVG}}</div></main></body></html>`))
