package source

import (
	"database/sql"
	"fmt"
	"strings"
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
	UnitsStatus         string
	UnitsStage          string
	ErrorMsg            sql.NullString
	OutlineType         sql.NullString
	Summary             sql.NullString
	DomainID            sql.NullString
	WordCount           sql.NullInt64
	ShadowOf            sql.NullString
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ProcessingStartedAt sql.NullTime
	CompletedAt         sql.NullTime
	UnitsCompletedAt    sql.NullTime
	UnitsBuiltAt        sql.NullTime
}

// SourceVersion is a snapshot of a source's files as they were the moment a
// reupload superseded them (docs/impl/v1/lifecycle.md 步骤 2's archive step,
// made queryable with a stable version number instead of only a timestamp
// directory under data/sources/archived/<source_id>/).
type SourceVersion struct {
	VersionID    string
	SourceID     string
	Version      int
	FileName     string
	OriginalPath string
	HTMLPath     sql.NullString
	MarkdownPath string
	ArchivedAt   time.Time
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
	_, err := s.db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, html_path, markdown_path, status, error_msg, outline_type, summary, domain_id, word_count, shadow_of)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.SourceID, src.Title, src.Format, src.FileName, src.OriginalPath,
		src.HTMLPath, src.MarkdownPath, src.Status, src.ErrorMsg,
		src.OutlineType, src.Summary, src.DomainID, src.WordCount, src.ShadowOf)
	if err != nil {
		return fmt.Errorf("source store: create: %w", err)
	}
	return nil
}

func (s *Store) GetByID(sourceID string) (*Source, error) {
	src := &Source{}
	err := s.db.QueryRow(`SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, units_status, units_stage, error_msg, outline_type, summary, domain_id, word_count, shadow_of, version, created_at, updated_at, processing_started_at, completed_at, units_completed_at, units_built_at
		FROM sources WHERE source_id = ?`, sourceID).Scan(
		&src.SourceID, &src.Title, &src.Format, &src.FileName,
		&src.OriginalPath, &src.HTMLPath, &src.MarkdownPath, &src.Status, &src.UnitsStatus, &src.UnitsStage,
		&src.ErrorMsg, &src.OutlineType, &src.Summary, &src.DomainID,
		&src.WordCount, &src.ShadowOf, &src.Version, &src.CreatedAt, &src.UpdatedAt,
		&src.ProcessingStartedAt, &src.CompletedAt, &src.UnitsCompletedAt, &src.UnitsBuiltAt)
	if err != nil {
		return nil, fmt.Errorf("source store: get by id: %w", err)
	}
	return src, nil
}

// List returns sources visible to the outside world — shadow rows created for
// POST /sources/:id/reupload are always excluded (docs/impl/v1/lifecycle.md 步骤 2).
func (s *Store) List(status, domainID string, limit, offset int) ([]Source, error) {
	var rows *sql.Rows
	var err error
	base := `SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, units_status, units_stage, error_msg, outline_type, summary, domain_id, word_count, shadow_of, version, created_at, updated_at, processing_started_at, completed_at, units_completed_at, units_built_at FROM sources`
	where := []string{"shadow_of IS NULL"}
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if domainID != "" {
		where = append(where, "domain_id = ?")
		args = append(args, domainID)
	}
	q := base + " WHERE " + strings.Join(where, " AND ")
	q += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err = s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("source store: list: %w", err)
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.SourceID, &src.Title, &src.Format, &src.FileName,
			&src.OriginalPath, &src.HTMLPath, &src.MarkdownPath, &src.Status, &src.UnitsStatus, &src.UnitsStage,
			&src.ErrorMsg, &src.OutlineType, &src.Summary, &src.DomainID,
			&src.WordCount, &src.ShadowOf, &src.Version, &src.CreatedAt, &src.UpdatedAt,
			&src.ProcessingStartedAt, &src.CompletedAt, &src.UnitsCompletedAt, &src.UnitsBuiltAt); err != nil {
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

// UpdateUnitsStatus tracks knowledge-unit extraction progress independently
// of Status (which only reflects source processing) — written by unit.Service's
// queue handler as pending -> processing -> completed/failed, so clients
// (file management page) can tell "source parsed" apart from "knowledge
// units actually finished extracting" instead of conflating the two.
func (s *Store) UpdateUnitsStatus(sourceID, unitsStatus string) error {
	switch unitsStatus {
	case "completed", "failed":
		_, err := s.db.Exec(`UPDATE sources SET units_status = ?, units_completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
			unitsStatus, sourceID)
		if err != nil {
			return fmt.Errorf("source store: update units status: %w", err)
		}
	default:
		_, err := s.db.Exec(`UPDATE sources SET units_status = ?, units_completed_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
			unitsStatus, sourceID)
		if err != nil {
			return fmt.Errorf("source store: update units status: %w", err)
		}
	}
	return nil
}

func (s *Store) StartUnitsProcessing(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources
		SET units_status = 'processing',
			units_stage = 'building',
			units_built_at = NULL,
			units_completed_at = NULL,
			updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: start units processing: %w", err)
	}
	return nil
}

func (s *Store) MarkUnitsSemanticsStarted(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources
		SET units_stage = 'semantics',
			units_built_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: mark units semantics started: %w", err)
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

// Count mirrors List's visibility rule: shadow rows are always excluded.
func (s *Store) Count(status, domainID string) (int, error) {
	var count int
	q := `SELECT COUNT(*) FROM sources`
	where := []string{"shadow_of IS NULL"}
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if domainID != "" {
		where = append(where, "domain_id = ?")
		args = append(args, domainID)
	}
	q += " WHERE " + strings.Join(where, " AND ")
	err := s.db.QueryRow(q, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("source store: count: %w", err)
	}
	return count, nil
}

func (s *Store) ExistsByFileName(fileName string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sources WHERE file_name = ?`, fileName).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("source store: exists by file name: %w", err)
	}
	return count > 0, nil
}

// ExistsByFileNameExcept is like ExistsByFileName but ignores excludeSourceID —
// used when creating a Shadow Source for reupload, which is allowed to reuse
// its own target's file name (docs/impl/v1/lifecycle.md 步骤 2).
func (s *Store) ExistsByFileNameExcept(fileName, excludeSourceID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sources WHERE file_name = ? AND source_id != ?`, fileName, excludeSourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("source store: exists by file name except: %w", err)
	}
	return count > 0, nil
}

// GetShadowByTarget returns the shadow Source row for targetSourceID (any
// status), or nil if none exists. A target can have at most one live shadow
// at a time (enforced by discarding stale ones before creating a new one).
func (s *Store) GetShadowByTarget(targetSourceID string) (*Source, error) {
	src := &Source{}
	err := s.db.QueryRow(`SELECT source_id, title, format, file_name, original_path, html_path, markdown_path, status, units_status, units_stage, error_msg, outline_type, summary, domain_id, word_count, shadow_of, created_at, updated_at, processing_started_at, completed_at
		FROM sources WHERE shadow_of = ?`, targetSourceID).Scan(
		&src.SourceID, &src.Title, &src.Format, &src.FileName,
		&src.OriginalPath, &src.HTMLPath, &src.MarkdownPath, &src.Status, &src.UnitsStatus, &src.UnitsStage,
		&src.ErrorMsg, &src.OutlineType, &src.Summary, &src.DomainID,
		&src.WordCount, &src.ShadowOf, &src.CreatedAt, &src.UpdatedAt,
		&src.ProcessingStartedAt, &src.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("source store: get shadow by target: %w", err)
	}
	return src, nil
}

// MarkDeleted soft-deletes a Source (docs/impl/v1/lifecycle.md 步骤 2):
// rows and files are kept, only status flips to 'deleted'.
func (s *Store) MarkDeleted(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources SET status = 'deleted', updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: mark deleted: %w", err)
	}
	return nil
}

// RestoreSource reverses MarkDeleted: status flips back to completed. Only
// matches rows currently 'deleted' — callers (Service.Restore) check status
// via GetByID first, so this is a defensive guard rather than the primary check.
func (s *Store) RestoreSource(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources SET status = 'completed', updated_at = CURRENT_TIMESTAMP WHERE source_id = ? AND status = 'deleted'`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: restore source: %w", err)
	}
	return nil
}

// SwapShadowIntoTarget re-parents a shadow's knowledge_units / knowledge_points /
// source_outlines onto targetID and drops the now-empty shadow row, in one
// transaction (docs/impl/v1/lifecycle.md 步骤 2, 换血事务 a + d). Metadata fields
// computed by the shadow's own source_process run (summary/domain/outline_type/
// word_count/format/file_name — everything except title) are copied onto the
// target row so they describe the new content, not the superseded one.
// originalPath/htmlPath are the post-swap file paths on disk, computed by the
// caller (Service.archiveAndSwapFiles) since Store does not touch the filesystem.
func (s *Store) SwapShadowIntoTarget(shadowID, targetID, originalPath string, htmlPath sql.NullString) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("source store: swap: begin tx: %w", err)
	}
	defer tx.Rollback()

	shadow := &Source{}
	err = tx.QueryRow(`SELECT file_name, format, summary, domain_id, outline_type, word_count, units_stage, units_built_at, units_completed_at
		FROM sources WHERE source_id = ?`, shadowID).Scan(
		&shadow.FileName, &shadow.Format, &shadow.Summary, &shadow.DomainID,
		&shadow.OutlineType, &shadow.WordCount, &shadow.UnitsStage, &shadow.UnitsBuiltAt, &shadow.UnitsCompletedAt)
	if err != nil {
		return fmt.Errorf("source store: swap: get shadow: %w", err)
	}

	// Unlike units/points (kept under the old source_id and marked superseded
	// by the caller before this transaction — see CompleteShadowSwap), outline
	// nodes have no lifecycle field and are filtered purely by deletion
	// (docs/impl/v1/lifecycle.md 步骤 3), so the target's own pre-reupload
	// outline rows must be dropped before the shadow's take over the same
	// source_id — otherwise both sets end up coexisting under one source_id.
	// The old (now-superseded) units still reference those rows via the
	// outline_id FK, so it must be cleared first, and this has to happen
	// before shadow's own units are reparented in below (still cleanly
	// distinguishable as "everything currently under targetID" only at this
	// point) or it would also null out the shadow's still-valid outline_id.
	if _, err := tx.Exec(`UPDATE knowledge_units SET outline_id = NULL WHERE source_id = ? AND outline_id IS NOT NULL`, targetID); err != nil {
		return fmt.Errorf("source store: swap: clear superseded units' outline_id: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM source_outlines WHERE source_id = ?`, targetID); err != nil {
		return fmt.Errorf("source store: swap: delete target outlines: %w", err)
	}

	if _, err := tx.Exec(`UPDATE knowledge_units SET source_id = ? WHERE source_id = ?`, targetID, shadowID); err != nil {
		return fmt.Errorf("source store: swap: reparent units: %w", err)
	}
	if _, err := tx.Exec(`UPDATE knowledge_points SET source_id = ? WHERE source_id = ?`, targetID, shadowID); err != nil {
		return fmt.Errorf("source store: swap: reparent points: %w", err)
	}
	if _, err := tx.Exec(`UPDATE source_outlines SET source_id = ? WHERE source_id = ?`, targetID, shadowID); err != nil {
		return fmt.Errorf("source store: swap: reparent outlines: %w", err)
	}
	// units_status is forced to completed here: the swap's own precondition
	// (docs/impl/v1/lifecycle.md 步骤 2) is that the shadow's unit_extract —
	// including KPN/concept matching — already finished successfully, and
	// the units/points just reparented above ARE that finished result, so
	// the target must reflect "done" immediately, not "pending" until some
	// unrelated future unit_extract run happens to touch it.
	if _, err := tx.Exec(`UPDATE sources SET file_name = ?, format = ?, summary = ?, domain_id = ?, outline_type = ?, word_count = ?, original_path = ?, html_path = ?, version = version + 1, units_status = 'completed', units_stage = ?, units_built_at = ?, units_completed_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ?`,
		shadow.FileName, shadow.Format, shadow.Summary, shadow.DomainID, shadow.OutlineType, shadow.WordCount, originalPath, htmlPath, shadow.UnitsStage, shadow.UnitsBuiltAt, shadow.UnitsCompletedAt, targetID); err != nil {
		return fmt.Errorf("source store: swap: update target metadata: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sources WHERE source_id = ?`, shadowID); err != nil {
		return fmt.Errorf("source store: swap: delete shadow row: %w", err)
	}

	return tx.Commit()
}

func (s *Store) InsertSourceVersion(v *SourceVersion) error {
	if v.VersionID == "" {
		v.VersionID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO source_versions (version_id, source_id, version, file_name, original_path, html_path, markdown_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.VersionID, v.SourceID, v.Version, v.FileName, v.OriginalPath, v.HTMLPath, v.MarkdownPath)
	if err != nil {
		return fmt.Errorf("source store: insert source version: %w", err)
	}
	return nil
}

func (s *Store) GetSourceVersions(sourceID string) ([]SourceVersion, error) {
	rows, err := s.db.Query(`SELECT version_id, source_id, version, file_name, original_path, html_path, markdown_path, archived_at
		FROM source_versions WHERE source_id = ? ORDER BY version DESC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("source store: get source versions: %w", err)
	}
	defer rows.Close()

	var versions []SourceVersion
	for rows.Next() {
		var v SourceVersion
		if err := rows.Scan(&v.VersionID, &v.SourceID, &v.Version, &v.FileName,
			&v.OriginalPath, &v.HTMLPath, &v.MarkdownPath, &v.ArchivedAt); err != nil {
			return nil, fmt.Errorf("source store: scan source version: %w", err)
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *Store) GetSourceVersion(sourceID string, version int) (*SourceVersion, error) {
	v := &SourceVersion{}
	err := s.db.QueryRow(`SELECT version_id, source_id, version, file_name, original_path, html_path, markdown_path, archived_at
		FROM source_versions WHERE source_id = ? AND version = ?`, sourceID, version).Scan(
		&v.VersionID, &v.SourceID, &v.Version, &v.FileName,
		&v.OriginalPath, &v.HTMLPath, &v.MarkdownPath, &v.ArchivedAt)
	if err != nil {
		return nil, fmt.Errorf("source store: get source version: %w", err)
	}
	return v, nil
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
