package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"estopo/internal/i18n"
	"estopo/internal/query"
	"estopo/internal/store"
)

// apiError is the JSON body of every error.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

// fail maps internal errors to HTTP answers.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, query.ErrNoData):
		writeError(w, http.StatusNotFound, "no_data", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errBadRequest):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.log.Error("request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

var errBadRequest = errors.New("bad request")

func badRequest(msg string) error { return errors.Join(errBadRequest, errors.New(msg)) }

// timeParam reads a date given in epoch milliseconds or RFC 3339; def when absent.
func timeParam(r *http.Request, name string, def int64) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t, terr := time.Parse(time.RFC3339, v)
		if terr != nil {
			return 0, badRequest("invalid date " + name + "=" + v)
		}
		ms = t.UnixMilli()
	}
	if ms < 0 || ms > maxDateMs {
		return 0, badRequest("date out of range " + name + "=" + v)
	}
	return ms, nil
}

// Bounds of the dates and time windows accepted from a request: beyond
// them, the computations on buckets overflow.
var maxDateMs = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

const maxWindow = 3660 * 24 * time.Hour // the longest retention is 10 years

func intParam(r *http.Request, name string, def, lo, hi int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, badRequest("invalid " + name + "=" + v)
	}
	return n, nil
}

func boolParam(r *http.Request, name string, def bool) bool {
	switch strings.ToLower(r.URL.Query().Get(name)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func listParam(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
	}
	return out
}

// window reads [from, to] (defaults: the last `def` before now).
func (s *Server) window(r *http.Request, fromName, toName string, def time.Duration) (int64, int64, error) {
	now := s.clock.Now().UnixMilli()
	to, err := timeParam(r, toName, now)
	if err != nil {
		return 0, 0, err
	}
	from, err := timeParam(r, fromName, max(0, to-def.Milliseconds()))
	if err != nil {
		return 0, 0, err
	}
	if from > to {
		return 0, 0, badRequest(fromName + " is after " + toName)
	}
	if to-from > maxWindow.Milliseconds() {
		return 0, 0, badRequest("the window from " + fromName + " to " + toName + " is longer than 10 years")
	}
	return from, to, nil
}

// lang is the language of labels (exports): ?lang=, else Accept-Language.
func lang(r *http.Request) i18n.Lang {
	if l := r.URL.Query().Get("lang"); l != "" {
		return i18n.Normalize(l)
	}
	return i18n.Normalize(r.Header.Get("Accept-Language"))
}

// page slices a list for pagination.
type page[T any] struct {
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Items  []T `json:"items"`
}

func paginate[T any](r *http.Request, items []T) (page[T], error) {
	limit, err := intParam(r, "limit", 200, 1, 10000)
	if err != nil {
		return page[T]{}, err
	}
	offset, err := intParam(r, "offset", 0, 0, 1<<30)
	if err != nil {
		return page[T]{}, err
	}
	p := page[T]{Total: len(items), Offset: offset}
	if offset < len(items) {
		p.Items = items[offset:min(len(items), offset+limit)]
	}
	if p.Items == nil {
		p.Items = []T{}
	}
	return p, nil
}
