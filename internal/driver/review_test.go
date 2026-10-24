package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListResponseShapeAndEmptyDirectories(t *testing.T) {
	for _, tc := range []struct {
		data  string
		valid bool
	}{
		{`{}`, false}, {`null`, false}, {`{"total":0}`, false},
		{`{"content":null}`, false}, {`{"content":null,"total":1}`, false},
		{`{"content":[],"total":-1}`, false}, {`{"content":[],"total":null}`, false},
		{`{"content":[{}],"total":1}`, false},
		{`{"content":[{"name":"movie"}],"total":1}`, false},
		{`{"content":null,"total":0}`, true}, {`{"content":[],"total":0}`, true},
		{`{"content":[]}`, true},
	} {
		t.Run(tc.data, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"code":200,"data":%s}`, tc.data) }))
			defer srv.Close()
			_, err := NewOpenList(srv.URL, "").List(context.Background(), "/")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestListHonorsTotalAcrossShortPagesAndRejectsIncomplete(t *testing.T) {
	for _, mode := range []string{"short pages", "incomplete", "duplicate", "changed total"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Page int `json:"page"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				total := 2
				name := fmt.Sprint(req.Page)
				if req.Page == 2 {
					switch mode {
					case "incomplete":
						fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":2}}`)
						return
					case "duplicate":
						name = "1"
					case "changed total":
						total = 3
					}
				}
				fmt.Fprintf(w, `{"code":200,"data":{"content":[{"name":%q,"is_dir":false}],"total":%d}}`, name, total)
			}))
			defer srv.Close()
			files, err := NewOpenList(srv.URL, "").List(context.Background(), "/")
			if mode == "short pages" {
				if err != nil || len(files) != 2 || calls != 2 {
					t.Fatal(files, err, calls)
				}
			} else if err == nil {
				t.Fatal("accepted unsafe listing", files)
			}
		})
	}
}
