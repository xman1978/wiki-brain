package source

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Source struct {
	SourceID            string
	Title               string
	Format              string
	FileName            string
	OriginalPath        string
	HTMLPath            sql.NullString
	MarkdownPath        string
	Status              string
	ErrorMsg            sql.NullString
	OutlineType         sql.NullString
	Summary             sql.NullString
	DomainID            sql.NullString
	WordCount           sql.NullInt64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ProcessingStartedAt sql.NullTime
	CompletedAt         sql.NullTime
}

type Outline struct {
	OutlineID string
	SourceID  string
	ParentID  sql.NullString
	Level     int
	Title     string
	Summary   sql.NullString
	LineStart int
	LineEnd   int
	NodeType  string
	Position  int
	CreatedAt time.Time
}

type OutlineTree struct {
	OutlineID string        `json:"outline_id"`
	Title     string        `json:"title"`
	Level     int           `json:"level"`
	NodeType  string        `json:"node_type"`
	Summary   *string       `json:"summary"`
	LineStart int           `json:"line_start"`
	LineEnd   int           `json:"line_end"`
	Children  []OutlineTree `json:"children"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(src *Source) error {
	if src.SourceID == "" {
		src.SourceID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, html_path, markdown_path, status, error_msg, outline_type, summary, domain_id, word_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.SourceID, src.Title, src.Format, src.FileName, src.OriginalPath,
		src.HTMLPath, src.MarkdownPath, src.Status, src.ErrorMsg,
		src.OutlineType, src.Summary, src.DomainID, src.WordCount)
	if err != nil {
		return fmt.Errorf("source store: create: %w", err)
	}
	return nil
}

func (s *Store) GetByID(sourceID string) (*Source, error) {
	src := &Source{}
	err := s.db.QueryRow(`SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, error_msg, outline_type, summary, domain_id, word_count, created_at, updated_at, processing_started_at, completed_at
		FROM sources WHERE source_id = ?`, sourceID).Scan(
		&src.SourceID, &src.Title, &src.Format, &src.FileName,
		&src.OriginalPath, &src.HTMLPath, &src.MarkdownPath, &src.Status,
		&src.ErrorMsg, &src.OutlineType, &src.Summary, &src.DomainID,
		&src.WordCount, &src.CreatedAt, &src.UpdatedAt,
		&src.ProcessingStartedAt, &src.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("source store: get by id: %w", err)
	}
	return src, nil
}

func (s *Store) List(status string, limit, offset int) ([]Source, error) {
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.db.Query(`SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, error_msg, outline_type, summary, domain_id, word_count, created_at, updated_at, processing_started_at, completed_at
			FROM sources WHERE status = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`, status, limit, offset)
	} else {
		rows, err = s.db.Query(`SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, error_msg, outline_type, summary, domain_id, word_count, created_at, updated_at, processing_started_at, completed_at
			FROM sources ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("source store: list: %w", err)
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.SourceID, &src.Title, &src.Format, &src.FileName,
			&src.OriginalPath, &src.HTMLPath, &src.MarkdownPath, &src.Status,
			&src.ErrorMsg, &src.OutlineType, &src.Summary, &src.DomainID,
			&src.WordCount, &src.CreatedAt, &src.UpdatedAt,
			&src.ProcessingStartedAt, &src.CompletedAt); err != nil {
			return nil, fmt.Errorf("source store: scan: %w", err)
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

func (s *Store) UpdateStatus(sourceID, status string, errorMsg *string) error {
	var errVal sql.NullString
	if errorMsg != nil {
		errVal = sql.NullString{String: *errorMsg, Valid: true}
	}
	now := time.Now().UTC()
	switch status {
	case "processing":
		_, err := s.db.Exec(`UPDATE sources SET status = ?, error_msg = ?, processing_started_at = ?, updated_at = ? WHERE source_id = ?`,
			status, errVal, now, now, sourceID)
		if err != nil {
			return fmt.Errorf("source store: update status: %w", err)
		}
	case "completed", "failed":
		_, err := s.db.Exec(`UPDATE sources SET status = ?, error_msg = ?, completed_at = ?, updated_at = ? WHERE source_id = ?`,
			status, errVal, now, now, sourceID)
		if err != nil {
			return fmt.Errorf("source store: update status: %w", err)
		}
	default:
		_, err := s.db.Exec(`UPDATE sources SET status = ?, error_msg = ?, updated_at = ? WHERE source_id = ?`,
			status, errVal, now, sourceID)
		if err != nil {
			return fmt.Errorf("source store: update status: %w", err)
		}
	}
	return nil
}

func (s *Store) UpdateOutlineType(sourceID, outlineType string) error {
	_, err := s.db.Exec(`UPDATE sources SET outline_type = ?, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
		outlineType, sourceID)
	if err != nil {
		return fmt.Errorf("source store: update outline type: %w", err)
	}
	return nil
}

func (s *Store) UpdateSummary(sourceID, summary string) error {
	_, err := s.db.Exec(`UPDATE sources SET summary = ?, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
		summary, sourceID)
	if err != nil {
		return fmt.Errorf("source store: update summary: %w", err)
	}
	return nil
}

func (s *Store) UpdateDomainID(sourceID string, domainID *string) error {
	var val sql.NullString
	if domainID != nil {
		val = sql.NullString{String: *domainID, Valid: true}
	}
	_, err := s.db.Exec(`UPDATE sources SET domain_id = ?, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
		val, sourceID)
	if err != nil {
		return fmt.Errorf("source store: update domain id: %w", err)
	}
	return nil
}

func (s *Store) UpdateWordCount(sourceID string, wordCount int) error {
	_, err := s.db.Exec(`UPDATE sources SET word_count = ?, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
		wordCount, sourceID)
	if err != nil {
		return fmt.Errorf("source store: update word count: %w", err)
	}
	return nil
}

func (s *Store) InsertOutline(o *Outline) error {
	if o.OutlineID == "" {
		o.OutlineID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, summary, line_start, line_end, node_type, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OutlineID, o.SourceID, o.ParentID, o.Level, o.Title, o.Summary,
		o.LineStart, o.LineEnd, o.NodeType, o.Position)
	if err != nil {
		return fmt.Errorf("source store: insert outline: %w", err)
	}
	return nil
}

func (s *Store) InsertOutlines(outlines []Outline) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("source store: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, summary, line_start, line_end, node_type, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("source store: prepare: %w", err)
	}
	defer stmt.Close()

	for i := range outlines {
		o := &outlines[i]
		if o.OutlineID == "" {
			o.OutlineID = uuid.New().String()
		}
		if _, err := stmt.Exec(o.OutlineID, o.SourceID, o.ParentID, o.Level, o.Title, o.Summary, o.LineStart, o.LineEnd, o.NodeType, o.Position); err != nil {
			return fmt.Errorf("source store: insert outline %s: %w", o.OutlineID, err)
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteSource(sourceID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("source store: begin tx: %w", err)
	}
	defer tx.Rollback()

	// 按 FK 依赖顺序删除：先子表再父表
	// KP relations → KP → KU → outlines → source
	if _, err := tx.Exec(`DELETE FROM knowledge_point_relations WHERE source_point_id IN (SELECT point_id FROM knowledge_points WHERE source_id = ?) OR target_point_id IN (SELECT point_id FROM knowledge_points WHERE source_id = ?)`, sourceID, sourceID); err != nil {
		return fmt.Errorf("source store: delete kp relations: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_points WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("source store: delete knowledge_points: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_units WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("source store: delete knowledge_units: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM source_outlines WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("source store: delete outlines: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sources WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("source store: delete source: %w", err)
	}

	return tx.Commit()
}

func (s *Store) GetOutlineIDs(sourceID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT outline_id FROM source_outlines WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetUnitIDs(sourceID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT unit_id FROM knowledge_units WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetPointIDs(sourceID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT point_id FROM knowledge_points WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) DeleteOutlines(sourceID string) error {
	_, err := s.db.Exec(`DELETE FROM source_outlines WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: delete outlines: %w", err)
	}
	return nil
}

func (s *Store) GetOutlines(sourceID string) ([]Outline, error) {
	rows, err := s.db.Query(`SELECT outline_id, source_id, parent_id, level, title, summary, line_start, line_end, node_type, position, created_at
		FROM source_outlines WHERE source_id = ? ORDER BY line_start ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("source store: get outlines: %w", err)
	}
	defer rows.Close()

	var outlines []Outline
	for rows.Next() {
		var o Outline
		if err := rows.Scan(&o.OutlineID, &o.SourceID, &o.ParentID, &o.Level, &o.Title, &o.Summary, &o.LineStart, &o.LineEnd, &o.NodeType, &o.Position, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("source store: scan outline: %w", err)
		}
		outlines = append(outlines, o)
	}
	return outlines, rows.Err()
}

func (s *Store) GetOutlineTree(sourceID string) ([]OutlineTree, error) {
	outlines, err := s.GetOutlines(sourceID)
	if err != nil {
		return nil, err
	}
	return buildOutlineTree(outlines), nil
}

func buildOutlineTree(outlines []Outline) []OutlineTree {
	nodeMap := make(map[string]*OutlineTree)
	var roots []OutlineTree

	for _, o := range outlines {
		node := OutlineTree{
			OutlineID: o.OutlineID,
			Title:     o.Title,
			Level:     o.Level,
			NodeType:  o.NodeType,
			LineStart: o.LineStart,
			LineEnd:   o.LineEnd,
			Children:  []OutlineTree{},
		}
		if o.Summary.Valid {
			s := o.Summary.String
			node.Summary = &s
		}
		nodeMap[o.OutlineID] = &node

		if !o.ParentID.Valid {
			roots = append(roots, node)
		}
	}

	for _, o := range outlines {
		if o.ParentID.Valid {
			if parent, ok := nodeMap[o.ParentID.String]; ok {
				child := nodeMap[o.OutlineID]
				parent.Children = append(parent.Children, *child)
			}
		}
	}

	// Rebuild roots to include updated children
	var result []OutlineTree
	for _, o := range outlines {
		if !o.ParentID.Valid {
			result = append(result, *nodeMap[o.OutlineID])
		}
	}
	if result == nil {
		result = []OutlineTree{}
	}
	return result
}

func (s *Store) DomainExists(domainID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM domains WHERE domain_id = ?`, domainID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("source store: domain exists: %w", err)
	}
	return count > 0, nil
}

func (s *Store) ListDomains() ([]Domain, error) {
	rows, err := s.db.Query(`SELECT domain_id, name, description FROM domains ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("source store: list domains: %w", err)
	}
	defer rows.Close()

	var domains []Domain
	for rows.Next() {
		var d Domain
		var desc sql.NullString
		if err := rows.Scan(&d.DomainID, &d.Name, &desc); err != nil {
			return nil, fmt.Errorf("source store: scan domain: %w", err)
		}
		if desc.Valid {
			d.Description = desc.String
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

type Domain struct {
	DomainID    string
	Name        string
	Description string
}
