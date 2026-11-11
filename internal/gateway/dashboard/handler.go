package dashboard

// The dashboard's HTTP side: GET-only routes under /ui/, no authentication (D7), read-only (D13), with
// the browser protections of ADR-0009 section 13 (S5, S6). Pages are html/template, so card text written by
// a host owner is escaped in every context (S5); JSON is encoding/json.
//
// The templates and static files belong to the frontend and are embedded:
//
//	templates/*.html   {{define "overview"}} {{define "network"}} {{define "status"}} {{define "card"}}
//	static/            app.css, app.js, htmx.min.js, cytoscape.min.js, VENDOR.md
//
// Decisions recorded here (lead's list, ADR-0009): the route names are the ADR's; "/" redirects to /ui/ (in
// the gateway, not here); a Host-header check against DNS rebinding (S6) is NOT done, because the gateway is
// legitimately reached by IP, by LAN name and by MagicDNS name; the 404 of an unknown host's card still carries
// the "card" partial with Error set.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed templates static
var assets embed.FS

// ContentSecurityPolicy forbids everything but the page's own origin: no inline script (the frontend uses
// static/app.js and htmx attributes), no inline style, no frames, no base override.
const ContentSecurityPolicy = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'"

// readTimeout bounds one snapshot read for a page, so a wedged read cannot hold a browser request forever.
const readTimeout = 5 * time.Second

// Source is the gateway's state as the dashboard reads it: the same two snapshots /v1/network and /v1/graph
// answer, in-process. It adds no new store query.
type Source interface {
	Snapshot(ctx context.Context) (Data, error)
}

type handler struct {
	src   Source
	info  Info
	pages *template.Template
	files http.Handler
}

// NewHandler parses the embedded templates and returns the handler of every /ui/ route. A template that does
// not parse is an error here, at start, not at the first request.
func NewHandler(src Source, info Info) (http.Handler, error) {
	pages, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	h := &handler{src: src, info: info, pages: pages, files: http.FileServerFS(staticFS)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/{$}", h.overview)
	mux.HandleFunc("GET /ui/network", h.network)
	mux.HandleFunc("GET /ui/network/graph.json", h.graph)
	mux.HandleFunc("GET /ui/network/status", h.status)
	mux.HandleFunc("GET /ui/hosts/{name}/card", h.card)
	mux.HandleFunc("GET /ui/static/", h.static)
	return withSecurityHeaders(mux), nil
}

// withSecurityHeaders puts the protections on every response, errors included.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", ContentSecurityPolicy)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (h *handler) data(w http.ResponseWriter, r *http.Request) (Data, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	d, err := h.src.Snapshot(ctx)
	if err != nil {
		// The error text may name a store; the browser gets a fixed sentence.
		http.Error(w, "the dashboard could not read the gateway state", http.StatusServiceUnavailable)
		return Data{}, false
	}
	return d, true
}

// render executes one named template into a buffer first, so a template error never leaves half a page.
func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	var body bytes.Buffer
	if err := h.pages.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "the dashboard could not render this page", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	if d, ok := h.data(w, r); ok {
		h.render(w, http.StatusOK, "overview", BuildOverview(d, h.info))
	}
}

func (h *handler) network(w http.ResponseWriter, r *http.Request) {
	if d, ok := h.data(w, r); ok {
		h.render(w, http.StatusOK, "network", BuildNetwork(d, h.info))
	}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	if d, ok := h.data(w, r); ok {
		h.render(w, http.StatusOK, "status", BuildStatus(d))
	}
}

func (h *handler) graph(w http.ResponseWriter, r *http.Request) {
	d, ok := h.data(w, r)
	if !ok {
		return
	}
	body, err := json.Marshal(BuildGraph(d))
	if err != nil {
		http.Error(w, "the dashboard could not encode the graph", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// maxHostName bounds the name echoed into a 404 partial; a real host name is far shorter.
const maxHostName = 128

func (h *handler) card(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	d, ok := h.data(w, r)
	if !ok {
		return
	}
	if len(name) <= maxHostName {
		if card, found := BuildCard(d, name); found {
			h.render(w, http.StatusOK, "card", card)
			return
		}
	} else {
		name = name[:maxHostName]
	}
	h.render(w, http.StatusNotFound, "card", &CardPartial{HostName: name, Error: "no paired host has this name"})
}

// static serves the embedded files, never a directory listing.
func (h *handler) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.StripPrefix("/ui/static/", h.files).ServeHTTP(w, r)
}
