package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/moehoshio/web-request-attribution/internal/parser"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ip TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			query TEXT,
			protocol TEXT,
			status INTEGER,
			body_size INTEGER,
			referer TEXT,
			user_agent TEXT,
			domain TEXT,
			os TEXT,
			browser TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_requests_timestamp ON requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_requests_path ON requests(path);
		CREATE INDEX IF NOT EXISTS idx_requests_ip ON requests(ip);
		CREATE INDEX IF NOT EXISTS idx_requests_domain ON requests(domain);
		CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status);

		CREATE TABLE IF NOT EXISTS keyword_hits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			keyword TEXT NOT NULL,
			request_id INTEGER NOT NULL,
			context TEXT,
			FOREIGN KEY (request_id) REFERENCES requests(id)
		);
		CREATE INDEX IF NOT EXISTS idx_keyword_hits_keyword ON keyword_hits(keyword);
	`)
	return err
}

func (s *Store) Insert(entry *parser.LogEntry, keywords []string) error {
	osName := parser.OSInfo(entry.UserAgent)
	browser := parser.BrowserInfo(entry.UserAgent)

	res, err := s.db.Exec(`
		INSERT INTO requests (ip, timestamp, method, path, query, protocol, status, body_size, referer, user_agent, domain, os, browser)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.IP, entry.Timestamp, entry.Method, entry.Path, entry.Query,
		entry.Protocol, entry.Status, entry.BodySize, entry.Referer,
		entry.UserAgent, entry.Domain, osName, browser,
	)
	if err != nil {
		return err
	}

	if len(keywords) > 0 {
		reqID, _ := res.LastInsertId()
		fullPath := entry.Path + "?" + entry.Query
		for _, kw := range keywords {
			if strings.Contains(strings.ToLower(fullPath), strings.ToLower(kw)) {
				_, err := s.db.Exec(`INSERT INTO keyword_hits (keyword, request_id, context) VALUES (?, ?, ?)`,
					kw, reqID, fullPath)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (s *Store) InsertBatch(entries []*parser.LogEntry, keywords []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmtReq, err := tx.Prepare(`
		INSERT INTO requests (ip, timestamp, method, path, query, protocol, status, body_size, referer, user_agent, domain, os, browser)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmtReq.Close()

	stmtKw, err := tx.Prepare(`INSERT INTO keyword_hits (keyword, request_id, context) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmtKw.Close()

	for _, entry := range entries {
		osName := parser.OSInfo(entry.UserAgent)
		browser := parser.BrowserInfo(entry.UserAgent)

		res, err := stmtReq.Exec(
			entry.IP, entry.Timestamp, entry.Method, entry.Path, entry.Query,
			entry.Protocol, entry.Status, entry.BodySize, entry.Referer,
			entry.UserAgent, entry.Domain, osName, browser,
		)
		if err != nil {
			return err
		}

		if len(keywords) > 0 {
			reqID, _ := res.LastInsertId()
			fullPath := entry.Path + "?" + entry.Query
			for _, kw := range keywords {
				if strings.Contains(strings.ToLower(fullPath), strings.ToLower(kw)) {
					if _, err := stmtKw.Exec(kw, reqID, fullPath); err != nil {
						return err
					}
				}
			}
		}
	}

	return tx.Commit()
}

// QueryFilter defines filters for querying requests.
type QueryFilter struct {
	StartTime *time.Time
	EndTime   *time.Time
	IP        string
	Path      string
	Domain    string
	Method    string
	Status    int
	OS        string
	Browser   string
	Query     string
	Keyword   string
	Limit     int
	Offset    int
}

// RequestRow is a single request record.
type RequestRow struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Query     string    `json:"query"`
	Protocol  string    `json:"protocol"`
	Status    int       `json:"status"`
	BodySize  int       `json:"body_size"`
	Referer   string    `json:"referer"`
	UserAgent string    `json:"user_agent"`
	Domain    string    `json:"domain"`
	OS        string    `json:"os"`
	Browser   string    `json:"browser"`
}

type QueryResult struct {
	Total int          `json:"total"`
	Rows  []RequestRow `json:"rows"`
}

func (s *Store) Query(f QueryFilter) (*QueryResult, error) {
	where, args := buildWhere(f)

	// Count
	var total int
	countSQL := "SELECT COUNT(*) FROM requests" + where
	if err := s.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	querySQL := "SELECT id, ip, timestamp, method, path, query, protocol, status, body_size, referer, user_agent, domain, os, browser FROM requests" + where + " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RequestRow
	for rows.Next() {
		var r RequestRow
		if err := rows.Scan(&r.ID, &r.IP, &r.Timestamp, &r.Method, &r.Path, &r.Query, &r.Protocol, &r.Status, &r.BodySize, &r.Referer, &r.UserAgent, &r.Domain, &r.OS, &r.Browser); err != nil {
			return nil, err
		}
		results = append(results, r)
	}

	return &QueryResult{Total: total, Rows: results}, nil
}

// Stats returns aggregated statistics.
type StatsResult struct {
	TotalRequests int              `json:"total_requests"`
	TopPaths      []CountItem      `json:"top_paths"`
	TopIPs        []CountItem      `json:"top_ips"`
	TopDomains    []CountItem      `json:"top_domains"`
	TopOS         []CountItem      `json:"top_os"`
	TopBrowsers   []CountItem      `json:"top_browsers"`
	TopKeywords   []CountItem      `json:"top_keywords"`
	StatusCodes   []CountItem      `json:"status_codes"`
	RequestsPerDay []TimeCountItem `json:"requests_per_day"`
}

type CountItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type TimeCountItem struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func (s *Store) Stats(f QueryFilter) (*StatsResult, error) {
	where, args := buildWhere(f)
	result := &StatsResult{}

	// Total requests
	s.db.QueryRow("SELECT COUNT(*) FROM requests"+where, args...).Scan(&result.TotalRequests)

	// Top paths
	result.TopPaths = s.topN("path", where, args, 20)
	result.TopIPs = s.topN("ip", where, args, 20)
	result.TopDomains = s.topN("domain", where, args, 20)
	result.TopOS = s.topN("os", where, args, 10)
	result.TopBrowsers = s.topN("browser", where, args, 10)
	result.StatusCodes = s.topN("status", where, args, 10)

	// Top keywords
	kwWhere, kwArgs := buildKeywordWhere(f)
	result.TopKeywords = s.topNFrom("keyword_hits", "keyword", kwWhere, kwArgs, 20)

	// Requests per day
	result.RequestsPerDay = s.requestsPerDay(where, args)

	return result, nil
}

func (s *Store) topN(col, where string, args []interface{}, n int) []CountItem {
	query := fmt.Sprintf("SELECT %s, COUNT(*) as cnt FROM requests%s GROUP BY %s ORDER BY cnt DESC LIMIT ?", col, where, col)
	a := append(append([]interface{}{}, args...), n)
	rows, err := s.db.Query(query, a...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []CountItem
	for rows.Next() {
		var item CountItem
		rows.Scan(&item.Name, &item.Count)
		items = append(items, item)
	}
	return items
}

func (s *Store) topNFrom(table, col, where string, args []interface{}, n int) []CountItem {
	query := fmt.Sprintf("SELECT %s, COUNT(*) as cnt FROM %s%s GROUP BY %s ORDER BY cnt DESC LIMIT ?", col, table, where, col)
	a := append(append([]interface{}{}, args...), n)
	rows, err := s.db.Query(query, a...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []CountItem
	for rows.Next() {
		var item CountItem
		rows.Scan(&item.Name, &item.Count)
		items = append(items, item)
	}
	return items
}

func (s *Store) requestsPerDay(where string, args []interface{}) []TimeCountItem {
	query := "SELECT DATE(timestamp) as d, COUNT(*) as cnt FROM requests" + where + " GROUP BY d ORDER BY d DESC LIMIT 30"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []TimeCountItem
	for rows.Next() {
		var item TimeCountItem
		rows.Scan(&item.Date, &item.Count)
		items = append(items, item)
	}
	return items
}

func buildWhere(f QueryFilter) (string, []interface{}) {
	var conditions []string
	var args []interface{}

	if f.StartTime != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, *f.StartTime)
	}
	if f.EndTime != nil {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, *f.EndTime)
	}
	if f.IP != "" {
		conditions = append(conditions, "ip LIKE ?")
		args = append(args, "%"+f.IP+"%")
	}
	if f.Path != "" {
		conditions = append(conditions, "path LIKE ?")
		args = append(args, "%"+f.Path+"%")
	}
	if f.Domain != "" {
		conditions = append(conditions, "domain LIKE ?")
		args = append(args, "%"+f.Domain+"%")
	}
	if f.Method != "" {
		conditions = append(conditions, "method = ?")
		args = append(args, f.Method)
	}
	if f.Status > 0 {
		conditions = append(conditions, "status = ?")
		args = append(args, f.Status)
	}
	if f.OS != "" {
		conditions = append(conditions, "os = ?")
		args = append(args, f.OS)
	}
	if f.Browser != "" {
		conditions = append(conditions, "browser = ?")
		args = append(args, f.Browser)
	}
	if f.Query != "" {
		conditions = append(conditions, "query LIKE ?")
		args = append(args, "%"+f.Query+"%")
	}

	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func buildKeywordWhere(f QueryFilter) (string, []interface{}) {
	var conditions []string
	var args []interface{}

	if f.Keyword != "" {
		conditions = append(conditions, "keyword LIKE ?")
		args = append(args, "%"+f.Keyword+"%")
	}

	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB so other packages (e.g. internal/auth)
// can attach their own tables without re-opening the file.
func (s *Store) DB() *sql.DB { return s.db }
