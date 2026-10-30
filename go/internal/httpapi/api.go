// Package httpapi exposes the REST endpoints and actuator, matching the Spring controllers.
package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/carbynestack/amphora-go/internal/jwt"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/pgstore"
	"github.com/carbynestack/amphora-go/internal/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HealthCheck reports whether a dependency is reachable.
type HealthCheck func(ctx context.Context) error

// Info is the static /actuator/info content (values are rendered verbatim).
type Info map[string]string

// Backend is the business logic the controllers delegate to (implemented by *service.Service).
type Backend interface {
	List(ctx context.Context, p pgstore.ListParams) (model.MetadataPage, error)
	GetSecretShare(ctx context.Context, id, userID string) (*model.SecretShare, error)
	DeleteSecret(ctx context.Context, id, userID string) error
	RetrieveTags(ctx context.Context, id, userID string) ([]model.Tag, error)
	StoreTag(ctx context.Context, id string, tag model.Tag, userID string) (string, error)
	ReplaceTags(ctx context.Context, id string, tags []model.Tag, userID string) error
	RetrieveTag(ctx context.Context, id, key, userID string) (model.Tag, error)
	UpdateTag(ctx context.Context, id string, tag model.Tag, userID string) error
	DeleteTag(ctx context.Context, id, key, userID string) error
	CreateSecret(ctx context.Context, mi *model.MaskedInput, userID string) (string, error)
	InputMasks(ctx context.Context, requestID string, count int64) (model.OutputDeliveryObject, error)
	StoreSecretShare(ctx context.Context, share *model.SecretShare) (string, error)
	UseSecretShare(ctx context.Context, id, programID string) (*model.SecretShare, error)
	PutInterim(ctx context.Context, e model.ExchangeObject) error
}

// OutputDelivery computes the output delivery object of a secret share (Java OutputDeliveryService).
type OutputDelivery interface {
	ComputeFromSecretShareData(ctx context.Context, data []byte, requestID string) (model.OutputDeliveryObject, error)
}

var _ Backend = (*service.Service)(nil)

// API wires the HTTP layer.
type API struct {
	Svc Backend
	// ODO defaults to Svc's ODO when Svc is a *service.Service.
	ODO    OutputDelivery
	JWT    jwt.Reader
	Checks []HealthCheck
	Info   Info
	Log    *slog.Logger

	reg  *prometheus.Registry
	hist *prometheus.HistogramVec
}

// Handler builds the http.Handler.
func (a *API) Handler() http.Handler {
	if a.Log == nil {
		a.Log = slog.Default()
	}
	a.reg = prometheus.NewRegistry()
	a.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	a.hist = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_server_requests_seconds", Help: "HTTP server requests",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10},
	}, []string{"exception", "method", "outcome", "status", "uri"})
	a.reg.MustRegister(a.hist)
	return http.HandlerFunc(a.serve)
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.ResponseWriter.Write(b)
}

func outcome(c int) string {
	switch {
	case c >= 500:
		return "SERVER_ERROR"
	case c >= 400:
		return "CLIENT_ERROR"
	case c >= 300:
		return "REDIRECTION"
	case c >= 200:
		return "SUCCESS"
	}
	return "INFORMATIONAL"
}

func (a *API) serve(rw http.ResponseWriter, r *http.Request) {
	w := &statusWriter{ResponseWriter: rw}
	start := time.Now()
	uri := a.route(w, r)
	if w.code == 0 {
		w.code = 200
	}
	a.hist.WithLabelValues("None", r.Method, outcome(w.code), strconv.Itoa(w.code), uri).Observe(time.Since(start).Seconds())
}

// segments splits the escaped path (one trailing slash is ignored, like Spring's trailing slash match).
func segments(r *http.Request) []string {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if u, err := url.PathUnescape(s); err == nil {
			parts[i] = u
		}
	}
	return parts
}

func (a *API) route(w *statusWriter, r *http.Request) string {
	seg := segments(r)
	m := r.Method
	allow := func(methods ...string) bool {
		for _, x := range methods {
			if m == x || (m == http.MethodHead && x == http.MethodGet) {
				return true
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	switch {
	case len(seg) == 2 && seg[0] == "actuator" && seg[1] == "health":
		if allow("GET") {
			a.health(w, r)
		}
		return "/actuator/health"
	case len(seg) == 2 && seg[0] == "actuator" && seg[1] == "info":
		if allow("GET") {
			a.info(w, r)
		}
		return "/actuator/info"
	case len(seg) == 2 && seg[0] == "actuator" && seg[1] == "prometheus":
		if allow("GET") {
			// pin the exact Content-Type Spring's Prometheus endpoint emits
			promhttp.HandlerFor(a.reg, promhttp.HandlerOpts{}).ServeHTTP(&promCT{ResponseWriter: w}, r)
		}
		return "/actuator/prometheus"
	case len(seg) == 1 && seg[0] == "secret-shares":
		if allow("GET") {
			a.list(w, r)
		}
		return "/secret-shares"
	case len(seg) == 2 && seg[0] == "secret-shares":
		if !allow("GET", "DELETE") {
			return "/secret-shares/{secretId}"
		}
		if m == http.MethodDelete {
			a.deleteSecret(w, r, seg[1])
		} else {
			a.getSecret(w, r, seg[1])
		}
		return "/secret-shares/{secretId}"
	case len(seg) == 3 && seg[0] == "secret-shares" && seg[2] == "tags":
		if !allow("GET", "POST", "PUT") {
			return "/secret-shares/{secretId}/tags"
		}
		switch m {
		case http.MethodPost:
			a.createTag(w, r, seg[1])
		case http.MethodPut:
			a.updateTags(w, r, seg[1])
		default:
			a.getTags(w, r, seg[1])
		}
		return "/secret-shares/{secretId}/tags"
	case len(seg) == 4 && seg[0] == "secret-shares" && seg[2] == "tags":
		if !allow("GET", "PUT", "DELETE") {
			return "/secret-shares/{secretId}/tags/{tagKey}"
		}
		switch m {
		case http.MethodPut:
			a.putTag(w, r, seg[1], seg[3])
		case http.MethodDelete:
			a.deleteTag(w, r, seg[1], seg[3])
		default:
			a.getTag(w, r, seg[1], seg[3])
		}
		return "/secret-shares/{secretId}/tags/{tagKey}"
	case len(seg) == 1 && seg[0] == "masked-inputs":
		if allow("POST") {
			a.maskedInput(w, r)
		}
		return "/masked-inputs"
	case len(seg) == 1 && seg[0] == "input-masks":
		if allow("GET") {
			a.inputMasks(w, r)
		}
		return "/input-masks"
	case len(seg) == 2 && seg[0] == "intra-vcp" && seg[1] == "secret-shares":
		if allow("POST") {
			a.intraUpload(w, r)
		}
		return "/intra-vcp/secret-shares"
	case len(seg) == 3 && seg[0] == "intra-vcp" && seg[1] == "secret-shares":
		if allow("GET") {
			a.intraDownload(w, r, seg[2])
		}
		return "/intra-vcp/secret-shares/{secretId}"
	case len(seg) == 2 && seg[0] == "inter-vcp" && seg[1] == "open":
		if allow("POST") {
			a.open(w, r)
		}
		return "/inter-vcp/open"
	}
	a.notFound(w, r)
	return "NOT_FOUND"
}

func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	// Spring Boot BasicErrorController body
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000+00:00")
	_, _ = io.WriteString(w, `{"timestamp":`+model.Quote(ts)+`,"status":404,"error":"Not Found","path":`+model.Quote(r.URL.Path)+`}`)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.spring-boot.actuator.v3+json")
	for _, c := range a.Checks {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := c(ctx)
		cancel()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"DOWN"}`)
			return
		}
	}
	_, _ = io.WriteString(w, `{"status":"UP"}`)
}

func (a *API) info(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.spring-boot.actuator.v3+json")
	order := []string{"name", "description", "groupId", "artifactId", "version", "apiVersion"}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range order {
		v, ok := a.Info[k]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(model.Quote(k) + ":" + model.Quote(v))
	}
	b.WriteByte('}')
	_, _ = io.WriteString(w, b.String())
}

// ---- response helpers ----

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// writeError renders the RestControllerExceptionHandler responses.
func (a *API) writeError(w http.ResponseWriter, err error) {
	var me *model.Error
	if !errors.As(err, &me) {
		me = &model.Error{Kind: model.KindInternal, Msg: err.Error()}
	}
	status := http.StatusInternalServerError
	switch me.Kind {
	case model.KindNotFound:
		status = http.StatusNotFound
	case model.KindAlreadyExists:
		status = http.StatusConflict
	case model.KindBadRequest:
		status = http.StatusBadRequest
	case model.KindUnauthorized:
		status = http.StatusUnauthorized
	case model.KindBinding:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if status == http.StatusInternalServerError {
		a.Log.Error("request failed", "err", me.Msg)
	}
	body := model.Quote(me.Msg)
	if me.NullMsg {
		body = "null"
	}
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

var errBinding = model.Binding("binding")

// requestURI is ServletUriComponentsBuilder.fromCurrentRequestUri() + one path segment.
func requestURI(r *http.Request, segment string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if h, p, err := net.SplitHostPort(host); err == nil {
		if (scheme == "http" && p == "80") || (scheme == "https" && p == "443") {
			host = h
			if strings.Contains(h, ":") {
				host = "[" + h + "]"
			}
		}
	}
	path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
	return scheme + "://" + host + path + "/" + segment
}

func (a *API) created(w http.ResponseWriter, r *http.Request, segment string) {
	writeJSON(w, http.StatusCreated, model.Quote(requestURI(r, segment)))
}

// authHeader requires the Authorization header (missing -> 400 empty body).
func authHeader(r *http.Request) (string, error) {
	v := r.Header.Values("Authorization")
	if len(v) == 0 {
		return "", errBinding
	}
	return v[0], nil
}

func pathUUID(s string) (string, error) {
	id, ok := model.ParseUUID(s)
	if !ok {
		return "", errBinding
	}
	return id, nil
}

// readJSONBody enforces the JSON media type (415 -> empty body) and returns the raw body.
func readJSONBody(r *http.Request) ([]byte, int) {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || !(mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")) {
		return nil, http.StatusUnsupportedMediaType
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
	if err != nil {
		return nil, http.StatusBadRequest
	}
	return b, 0
}

func (a *API) body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, code := readJSONBody(r)
	if code != 0 {
		w.WriteHeader(code)
		return nil, false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}
	return b, true
}

// queryParams parses the raw query like Tomcat (repeated values are comma-joined by Spring).
func queryParams(r *http.Request) map[string]string {
	vals, _ := url.ParseQuery(r.URL.RawQuery)
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		out[k] = strings.Join(v, ",")
	}
	return out
}

func parseInt32(s string) (int32, bool) {
	s = strings.TrimSpace(s)
	neg := false
	t := s
	if strings.HasPrefix(t, "-") {
		neg, t = true, t[1:]
	} else if strings.HasPrefix(t, "+") {
		t = t[1:]
	}
	base := 10
	switch {
	case strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X"):
		base, t = 16, t[2:]
	case strings.HasPrefix(t, "#"):
		base, t = 16, t[1:]
	}
	if t == "" || strings.ContainsAny(t, "+-_") {
		return 0, false
	}
	n, err := strconv.ParseInt(t, base, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	if n < -2147483648 || n > 2147483647 {
		return 0, false
	}
	return int32(n), true
}

// ---- handlers ----

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	q := queryParams(r)
	pageNumber, pageSize := int32(0), int32(0)
	if v, ok := q["pageNumber"]; ok && v != "" {
		n, ok := parseInt32(v)
		if !ok {
			a.writeError(w, errBinding)
			return
		}
		pageNumber = n
	}
	if v, ok := q["pageSize"]; ok && v != "" {
		n, ok := parseInt32(v)
		if !ok {
			a.writeError(w, errBinding)
			return
		}
		pageSize = n
	}
	var filters []model.TagFilter
	if f := q["filter"]; strings.TrimSpace(f) != "" {
		var err error
		if filters, err = model.ParseTagFilters(f); err != nil {
			a.writeError(w, err)
			return
		}
	}
	p := pgstore.ListParams{Filters: filters}
	if sp := q["sort"]; strings.TrimSpace(sp) != "" {
		p.SortKey = sp
		p.SortDesc = strings.EqualFold(q["dir"], "desc")
	}
	if pageSize > 0 || pageNumber > 0 {
		p.Paged = true
		p.Page = int(max(0, pageNumber))
		p.PageSize = int(max(1, pageSize))
	}
	page, err := a.Svc.List(r.Context(), p)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page.JSON())
}

func (a *API) getSecret(w http.ResponseWriter, r *http.Request, rawID string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	rid, ok := model.ParseUUID(queryParams(r)["requestId"])
	if !ok {
		a.writeError(w, errBinding)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	share, err := a.Svc.GetSecretShare(r.Context(), id, user)
	if err != nil {
		a.writeError(w, err)
		return
	}
	odo, err := a.outputDelivery().ComputeFromSecretShareData(r.Context(), share.Data, rid)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.VerifiableSecretShareJSON(*share, odo))
}

func (a *API) outputDelivery() OutputDelivery {
	if a.ODO != nil {
		return a.ODO
	}
	return a.Svc.(*service.Service).ODO
}

func (a *API) deleteSecret(w http.ResponseWriter, r *http.Request, rawID string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if err := a.Svc.DeleteSecret(r.Context(), id, user); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) getTags(w http.ResponseWriter, r *http.Request, rawID string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	tags, err := a.Svc.RetrieveTags(r.Context(), id, user)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.TagsJSON(tags))
}

func (a *API) createTag(w http.ResponseWriter, r *http.Request, rawID string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	tag, err := model.ParseTag(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	key, err := a.Svc.StoreTag(r.Context(), id, tag, user)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.created(w, r, key)
}

func (a *API) updateTags(w http.ResponseWriter, r *http.Request, rawID string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	tags, err := model.ParseTagList(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if len(tags) == 0 {
		a.writeError(w, model.BadRequest("At least one tag must be given."))
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if err := a.Svc.ReplaceTags(r.Context(), id, tags, user); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) getTag(w http.ResponseWriter, r *http.Request, rawID, key string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	tag, err := a.Svc.RetrieveTag(r.Context(), id, key, user)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tag.JSON())
}

func (a *API) putTag(w http.ResponseWriter, r *http.Request, rawID, key string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	tag, err := model.ParseTag(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if key != tag.Key {
		a.writeError(w, model.BadRequest("The defined key and tag data do not match.\n%s <> %s", key, tag))
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if err := a.Svc.UpdateTag(r.Context(), id, tag, user); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) deleteTag(w http.ResponseWriter, r *http.Request, rawID, key string) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if err := a.Svc.DeleteTag(r.Context(), id, key, user); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) maskedInput(w http.ResponseWriter, r *http.Request) {
	auth, err := authHeader(r)
	if err != nil {
		a.writeError(w, err)
		return
	}
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	mi, err := model.ParseMaskedInput(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if !mi.HasData || len(mi.Data) == 0 {
		a.writeError(w, model.BadRequest("MaskedInput data must not be empty"))
		return
	}
	user, err := a.JWT.UserID(auth)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := a.Svc.CreateSecret(r.Context(), mi, user)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.created(w, r, id)
}

func (a *API) inputMasks(w http.ResponseWriter, r *http.Request) {
	q := queryParams(r)
	rid, ok := model.ParseUUID(q["requestId"])
	if !ok {
		a.writeError(w, errBinding)
		return
	}
	cs := strings.TrimSpace(q["count"])
	count, err := strconv.ParseInt(cs, 10, 64)
	if err != nil || cs == "" {
		// Spring's NumberUtils also accepts hex
		hc, ok := parseHexLong(cs)
		if !ok {
			a.writeError(w, errBinding)
			return
		}
		count = hc
	}
	if count <= 0 {
		a.writeError(w, model.BadRequest(service.TooLessInputMasksMsg))
		return
	}
	odo, err := a.Svc.InputMasks(r.Context(), rid, count)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, odo.JSON())
}

func parseHexLong(s string) (int64, bool) {
	t := s
	neg := false
	if strings.HasPrefix(t, "-") {
		neg, t = true, t[1:]
	}
	switch {
	case strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X"):
		t = t[2:]
	case strings.HasPrefix(t, "#"):
		t = t[1:]
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(t, 16, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func (a *API) intraUpload(w http.ResponseWriter, r *http.Request) {
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	share, err := model.ParseSecretShare(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	id, err := a.Svc.StoreSecretShare(r.Context(), share)
	if err != nil {
		a.writeError(w, err)
		return
	}
	a.created(w, r, id)
}

func (a *API) intraDownload(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := pathUUID(rawID)
	if err != nil {
		a.writeError(w, err)
		return
	}
	pid, ok := queryParams(r)["programId"]
	if !ok || pid == "" {
		a.writeError(w, errBinding)
		return
	}
	share, err := a.Svc.UseSecretShare(r.Context(), id, pid)
	if err != nil {
		a.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, share.JSON())
}

func (a *API) open(w http.ResponseWriter, r *http.Request) {
	b, ok := a.body(w, r)
	if !ok {
		return
	}
	e, err := model.ParseExchangeObject(b)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if err := a.Svc.PutInterim(r.Context(), *e); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// promCT overrides the Content-Type chosen by promhttp.
type promCT struct{ http.ResponseWriter }

const promContentType = "text/plain;version=0.0.4;charset=utf-8"

func (p *promCT) WriteHeader(c int) {
	p.Header().Set("Content-Type", promContentType)
	p.ResponseWriter.WriteHeader(c)
}
func (p *promCT) Write(b []byte) (int, error) {
	p.Header().Set("Content-Type", promContentType)
	return p.ResponseWriter.Write(b)
}
