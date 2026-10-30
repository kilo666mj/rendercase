package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/rendercase/internal/store"
)

func TestNormalizeCommentBody(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"trims", "  hello \n", "hello", true},
		{"empty", " \t\n", "", false},
		{"nul", "a\x00b", "", false},
		{"invalid utf8", "a\xffb", "", false},
		{"max runes multibyte", strings.Repeat("é", maxCommentRunes), strings.Repeat("é", maxCommentRunes), true},
		{"too long", strings.Repeat("a", maxCommentRunes+1), "", false},
	} {
		got, err := normalizeCommentBody(tc.in)
		var input commentInputError
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("%s: got %q, %v", tc.name, got, err)
		}
		if !tc.ok && !errors.As(err, &input) {
			t.Errorf("%s: err = %v, want commentInputError", tc.name, err)
		}
	}
}

func TestValidateAnchor(t *testing.T) {
	valid := []string{
		`{"selector":"main > p:nth-of-type(2)"}`,
		`{"selector":"#intro","quote":{"exact":"Hello","prefix":"Say","suffix":"world"},"point":{"x":0,"y":1}}`,
	}
	for _, raw := range valid {
		got, err := validateAnchor(json.RawMessage(raw))
		if err != nil || got == nil {
			t.Errorf("%s: %s, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "null"} {
		if got, err := validateAnchor(json.RawMessage(raw)); err != nil || got != nil {
			t.Errorf("%q: %s, %v", raw, got, err)
		}
	}
	invalid := []string{
		`{"selector":""}`,
		`{"selector":"` + strings.Repeat("a", 513) + `"}`,
		`{"selector":"p\u0007"}`,
		`{"selector":"p","extra":true}`,
		`{"selector":"p","quote":{"exact":""}}`,
		`{"selector":"p","quote":{"exact":"` + strings.Repeat("a", 501) + `"}}`,
		`{"selector":"p","quote":{"exact":"a","prefix":"` + strings.Repeat("a", 33) + `"}}`,
		`{"selector":"p","quote":{"exact":"a","nested":1}}`,
		`{"selector":"p","point":{"x":1.5,"y":0}}`,
		`{"selector":"p","point":{"x":-0.1,"y":0}}`,
		`{"selector":"p"} {"selector":"q"}`,
		`[1]`,
	}
	for _, raw := range invalid {
		var input commentInputError
		if _, err := validateAnchor(json.RawMessage(raw)); !errors.As(err, &input) {
			t.Errorf("%s: err = %v, want commentInputError", raw, err)
		}
	}
}

func TestThreadViewPermissions(t *testing.T) {
	now := time.Now()
	thread := store.CommentThread{
		ID: "ct_1", Version: 2, Anchor: json.RawMessage(`{"selector":"p","quote":{"exact":"Hi"}}`),
		CreatedBy: store.CommentAuthor{ID: "u_author", DisplayName: "Author"}, CreatedAt: now,
		Comments: []store.Comment{
			{ID: "c_1", Author: store.CommentAuthor{ID: "u_author"}, Body: "root", CreatedAt: now},
			{ID: "c_2", Author: store.CommentAuthor{ID: "u_other"}, Body: "", CreatedAt: now, DeletedAt: &now},
		},
	}
	author := newThreadView(thread, store.Artifact{Role: "viewer"}, store.User{ID: "u_author"})
	if !author.CanResolve || !author.Comments[0].CanEdit || !author.Comments[0].CanDelete || author.Comments[1].CanDelete || !author.Comments[1].Deleted {
		t.Fatalf("author view = %+v", author)
	}
	if author.Anchor == nil || author.Anchor.Selector != "p" || author.Anchor.Quote.Exact != "Hi" {
		t.Fatalf("anchor = %+v", author.Anchor)
	}
	viewer := newThreadView(thread, store.Artifact{Role: "viewer"}, store.User{ID: "u_viewer"})
	if viewer.CanResolve || viewer.Comments[0].CanEdit || viewer.Comments[0].CanDelete {
		t.Fatalf("viewer view = %+v", viewer)
	}
	editor := newThreadView(thread, store.Artifact{Role: "editor"}, store.User{ID: "u_editor"})
	if !editor.CanResolve || editor.Comments[0].CanEdit || !editor.Comments[0].CanDelete {
		t.Fatalf("editor view = %+v", editor)
	}
	general := newThreadView(store.CommentThread{ID: "ct_2"}, store.Artifact{Role: "owner"}, store.User{ID: "u"})
	if general.Anchor != nil || general.Comments == nil {
		t.Fatalf("general view = %+v", general)
	}
	raw, err := json.Marshal(general)
	if err != nil {
		t.Fatal(err)
	}
	for _, omitted := range []string{`"anchor"`, `"resolved_at"`, `"resolved_by"`} {
		if strings.Contains(string(raw), omitted) {
			t.Errorf("null field %s serialized: %s", omitted, raw)
		}
	}
}
