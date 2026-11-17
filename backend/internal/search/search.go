package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"chattp/internal/platform"
	"github.com/jackc/pgx/v5"
)

const index = "/chattp-messages-v1"

type Index struct {
	*platform.Services
	http                 *http.Client
	base, user, password string
}

func New(s *platform.Services) (*Index, error) {
	value := strings.TrimRight(os.Getenv("OPENSEARCH_URL"), "/")
	if value == "" {
		return nil, nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid OPENSEARCH_URL")
	}
	return &Index{s, &http.Client{Timeout: 5 * time.Second}, value, os.Getenv("OPENSEARCH_USER"), os.Getenv("OPENSEARCH_PASSWORD")}, nil
}

func (s *Index) request(ctx context.Context, method, path, contentType string, payload []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if s.user != "" {
		request.SetBasicAuth(s.user, s.password)
	}
	response, err := s.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return response.StatusCode, body, err
}

func (s *Index) ensure(ctx context.Context) error {
	status, _, err := s.request(ctx, http.MethodHead, index, "", nil)
	if err != nil {
		return err
	}
	if status == 200 {
		return nil
	}
	if status != 404 {
		return fmt.Errorf("OpenSearch index check returned %d", status)
	}
	settings := []byte(`{
 "settings":{"number_of_replicas":0,"analysis":{"analyzer":{"chattp_folded":{"tokenizer":"icu_tokenizer","filter":["icu_folding"]}}}},
 "mappings":{"dynamic":"strict","properties":{"body":{"type":"text","analyzer":"chattp_folded"},"channel_id":{"type":"keyword"}}}
 }`)
	status, body, err := s.request(ctx, http.MethodPut, index, "application/json", settings)
	if err != nil {
		return err
	}
	if status == 200 || (status == 400 && bytes.Contains(body, []byte("resource_already_exists_exception"))) {
		return nil
	}
	return fmt.Errorf("OpenSearch index creation returned %d", status)
}

func (s *Index) Sync(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cursor int64
	if err = tx.QueryRow(ctx, "SELECT cursor FROM search_state WHERE name='messages' FOR UPDATE").Scan(&cursor); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT e.id,coalesce(m.id,e.payload->>'id'),coalesce(m.channel_id,''),coalesce(m.body,''),m.deleted_at IS NULL AND m.id IS NOT NULL
 FROM (SELECT id,kind,payload FROM events WHERE id>$1 ORDER BY id LIMIT 400) e
 LEFT JOIN broadcasts b ON e.kind='broadcast.new' AND b.id::text=e.payload->>'id'
 LEFT JOIN messages m ON m.id=coalesce(b.message_id,e.payload->>'id')
 WHERE e.kind IN ('message.new','message.updated','message.deleted','broadcast.new')`, cursor)
	if err != nil {
		return err
	}
	var bulk bytes.Buffer
	enc := json.NewEncoder(&bulk)
	for rows.Next() {
		var eventID int64
		var id, channel, body string
		var visible bool
		if err = rows.Scan(&eventID, &id, &channel, &body, &visible); err != nil {
			rows.Close()
			return err
		}
		op := "delete"
		if visible {
			op = "index"
		}
		if err = enc.Encode(map[string]any{op: map[string]string{"_id": id}}); err != nil {
			rows.Close()
			return err
		}
		if visible {
			if err = enc.Encode(map[string]string{"body": body, "channel_id": channel}); err != nil {
				rows.Close()
				return err
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var next int64
	if err = tx.QueryRow(ctx, "SELECT coalesce(max(id),$1) FROM (SELECT id FROM events WHERE id>$1 ORDER BY id LIMIT 400) e", cursor).Scan(&next); err != nil {
		return err
	}
	if next == cursor {
		return nil
	}
	if bulk.Len() > 0 {
		if err = s.ensure(ctx); err != nil {
			return err
		}
		status, body, err := s.request(ctx, http.MethodPost, index+"/_bulk", "application/x-ndjson", bulk.Bytes())
		if err != nil {
			return err
		}
		if status != 200 {
			return fmt.Errorf("OpenSearch bulk returned %d", status)
		}
		var result struct {
			Items []map[string]struct {
				Status int `json:"status"`
			} `json:"items"`
		}
		if err = json.Unmarshal(body, &result); err != nil {
			return err
		}
		for _, item := range result.Items {
			for action, outcome := range item {
				if outcome.Status >= 300 && !(action == "delete" && outcome.Status == 404) {
					return fmt.Errorf("OpenSearch bulk %s returned %d", action, outcome.Status)
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE search_state SET cursor=$1 WHERE name='messages'", next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Index) Match(ctx context.Context, user, term string) ([]string, error) {
	var channels []string
	err := s.DB.QueryRow(ctx, "SELECT coalesce(array_agg(channel_id),'{}') FROM channel_members WHERE user_id=$1", user).Scan(&channels)
	if err != nil || len(channels) == 0 {
		return []string{}, err
	}
	request := map[string]any{
		"size":    50,
		"_source": false,
		"query": map[string]any{"bool": map[string]any{
			"must":   map[string]any{"match_bool_prefix": map[string]string{"body": term}},
			"filter": map[string]any{"terms": map[string]any{"channel_id": channels}},
		}},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	status, body, err := s.request(ctx, http.MethodPost, index+"/_search", "application/json", payload)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, errors.New("OpenSearch index not ready")
	}
	if status != 200 {
		return nil, fmt.Errorf("OpenSearch query returned %d", status)
	}
	var result struct {
		Hits struct {
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		ids = append(ids, hit.ID)
	}
	return ids, nil
}
