package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/kilo666mj/rendercase/internal/store"
)

func parseThreadQuery(v url.Values) (threadQuery, error) {
	open := false
	q := threadQuery{Resolved: &open}
	switch raw := v.Get("version"); raw {
	case "":
	case "all":
		q.AllVersions = true
	default:
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return threadQuery{}, commentInputError{"version must be a positive integer or all"}
		}
		q.Version = n
	}
	switch v.Get("resolved") {
	case "", "false":
	case "true":
		resolved := true
		q.Resolved = &resolved
	case "all":
		q.Resolved = nil
	default:
		return threadQuery{}, commentInputError{"resolved must be true, false, or all"}
	}
	return q, nil
}

func (s *Server) writeCommentError(w http.ResponseWriter, r *http.Request, err error) {
	var input commentInputError
	switch {
	case errors.As(err, &input):
		writeError(w, http.StatusBadRequest, input.Error())
	case errors.Is(err, errCommentArtifactNotFound), errors.Is(err, errCommentVersionNotFound),
		errors.Is(err, errCommentThreadNotFound), errors.Is(err, errCommentNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errCommentForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, store.ErrCommentLimit):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) listCommentThreadsHTTP(w http.ResponseWriter, r *http.Request) {
	q, err := parseThreadQuery(r.URL.Query())
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	threads, err := s.listCommentThreads(r.Context(), currentUser(r), r.PathValue("artifact"), q)
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (s *Server) createCommentThreadHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int             `json:"version"`
		Anchor  json.RawMessage `json:"anchor"`
		Body    string          `json:"body"`
	}
	if !decodeJSONLimit(w, r, &in, maxCommentRequestBytes) {
		return
	}
	t, err := s.createCommentThread(r.Context(), currentUser(r), r.PathValue("artifact"), in.Version, in.Anchor, in.Body, "")
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"thread": t})
}

func (s *Server) replyToCommentThreadHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if !decodeJSONLimit(w, r, &in, maxCommentRequestBytes) {
		return
	}
	c, err := s.replyToCommentThread(r.Context(), currentUser(r), r.PathValue("thread"), in.Body, "")
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"comment": c})
}

func (s *Server) editCommentHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if !decodeJSONLimit(w, r, &in, maxCommentRequestBytes) {
		return
	}
	c, err := s.editComment(r.Context(), currentUser(r), r.PathValue("comment"), in.Body, "")
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comment": c})
}

func (s *Server) deleteCommentHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteComment(r.Context(), currentUser(r), r.PathValue("comment"), ""); err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setCommentThreadResolvedHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolved *bool `json:"resolved"`
	}
	if !decodeJSONLimit(w, r, &in, maxCommentRequestBytes) {
		return
	}
	if in.Resolved == nil {
		writeError(w, http.StatusBadRequest, "resolved is required")
		return
	}
	t, err := s.setCommentThreadResolved(r.Context(), currentUser(r), r.PathValue("thread"), *in.Resolved, "")
	if err != nil {
		s.writeCommentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": t})
}
