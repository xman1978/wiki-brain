package llmconfig

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

var (
	ErrNotFound      = errors.New("llmconfig: provider not found")
	ErrInUse         = errors.New("llmconfig: provider is bound to a purpose")
	ErrInvalidInput  = errors.New("llmconfig: invalid input")
)

var PurposeList = []string{"default", "reasoning", "extraction", "classification"}

type Provider struct {
	ProviderID     string                     `json:"provider_id"`
	Name           string                     `json:"name"`
	Platform       llm.Platform               `json:"platform"`
	BaseURL        string                     `json:"base_url"`
	APIKey         string                     `json:"api_key"`
	TimeoutSeconds int                        `json:"timeout_seconds"`
	MaxRetries     int                        `json:"max_retries"`
	ResponseFormat string                     `json:"response_format"`
	Models         map[string]llm.ModelParams `json:"models,omitempty"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

// PurposeBinding ties a purpose to a provider and model parameters.
type PurposeBinding struct {
	ProviderID string `json:"provider_id"`
	llm.ModelParams
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) CountProviders() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_providers`).Scan(&n)
	return n, err
}

func (s *Store) ListProviders() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT provider_id, name, platform, base_url, api_key, timeout_seconds, max_retries, response_format, models, created_at, updated_at FROM llm_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProvider(id string) (Provider, error) {
	row := s.db.QueryRow(`SELECT provider_id, name, platform, base_url, api_key, timeout_seconds, max_retries, response_format, models, created_at, updated_at FROM llm_providers WHERE provider_id = ?`, id)
	p, err := scanProviderRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	return p, err
}

func (s *Store) InsertProvider(p Provider) error {
	if p.Models == nil {
		p.Models = map[string]llm.ModelParams{}
	}
	modelsJSON, err := json.Marshal(p.Models)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO llm_providers (provider_id, name, platform, base_url, api_key, timeout_seconds, max_retries, response_format, models, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		p.ProviderID, p.Name, string(p.Platform), p.BaseURL, p.APIKey, p.TimeoutSeconds, p.MaxRetries, p.ResponseFormat, string(modelsJSON))
	return err
}

func (s *Store) UpdateProvider(p Provider) error {
	if p.Models == nil {
		p.Models = map[string]llm.ModelParams{}
	}
	modelsJSON, err := json.Marshal(p.Models)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE llm_providers SET name = ?, platform = ?, base_url = ?, api_key = ?, timeout_seconds = ?, max_retries = ?, response_format = ?, models = ?, updated_at = CURRENT_TIMESTAMP WHERE provider_id = ?`,
		p.Name, string(p.Platform), p.BaseURL, p.APIKey, p.TimeoutSeconds, p.MaxRetries, p.ResponseFormat, string(modelsJSON), p.ProviderID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProvider(id string) error {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_purpose_bindings WHERE provider_id = ?`, id).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.db.Exec(`DELETE FROM llm_providers WHERE provider_id = ?`, id)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListBindings() (map[string]PurposeBinding, error) {
	rows, err := s.db.Query(`SELECT purpose, provider_id, model_config FROM llm_purpose_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]PurposeBinding)
	for rows.Next() {
		var purpose, providerID, modelJSON string
		if err := rows.Scan(&purpose, &providerID, &modelJSON); err != nil {
			return nil, err
		}
		b := PurposeBinding{ProviderID: providerID}
		if modelJSON != "" && modelJSON != "{}" {
			_ = json.Unmarshal([]byte(modelJSON), &b.ModelParams)
		}
		if b.Model == "" {
			if enriched, err := s.legacyModelParams(providerID, purpose); err == nil && enriched.Model != "" {
				b.ModelParams = enriched
			}
		}
		out[purpose] = b
	}
	return out, rows.Err()
}

func (s *Store) legacyModelParams(providerID, purpose string) (llm.ModelParams, error) {
	p, err := s.GetProvider(providerID)
	if err != nil {
		return llm.ModelParams{}, err
	}
	if p.Models == nil {
		return llm.ModelParams{}, nil
	}
	if m, ok := p.Models[purpose]; ok && m.Model != "" {
		return m, nil
	}
	if m, ok := p.Models["default"]; ok {
		return m, nil
	}
	return llm.ModelParams{}, nil
}

func (s *Store) SetBindings(bindings map[string]PurposeBinding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM llm_purpose_bindings`); err != nil {
		return err
	}
	for purpose, b := range bindings {
		modelJSON, err := json.Marshal(b.ModelParams)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO llm_purpose_bindings (purpose, provider_id, model_config) VALUES (?, ?, ?)`,
			purpose, b.ProviderID, string(modelJSON)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanProvider(rows *sql.Rows) (Provider, error) {
	var p Provider
	var platform, modelsJSON string
	var created, updated string
	err := rows.Scan(&p.ProviderID, &p.Name, &platform, &p.BaseURL, &p.APIKey, &p.TimeoutSeconds, &p.MaxRetries, &p.ResponseFormat, &modelsJSON, &created, &updated)
	if err != nil {
		return Provider{}, err
	}
	p.Platform = llm.Platform(platform)
	if err := json.Unmarshal([]byte(modelsJSON), &p.Models); err != nil {
		return Provider{}, err
	}
	if p.Models == nil {
		p.Models = map[string]llm.ModelParams{}
	}
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return p, nil
}

func scanProviderRow(row *sql.Row) (Provider, error) {
	var p Provider
	var platform, modelsJSON string
	var created, updated string
	err := row.Scan(&p.ProviderID, &p.Name, &platform, &p.BaseURL, &p.APIKey, &p.TimeoutSeconds, &p.MaxRetries, &p.ResponseFormat, &modelsJSON, &created, &updated)
	if err != nil {
		return Provider{}, err
	}
	p.Platform = llm.Platform(platform)
	if err := json.Unmarshal([]byte(modelsJSON), &p.Models); err != nil {
		return Provider{}, err
	}
	if p.Models == nil {
		p.Models = map[string]llm.ModelParams{}
	}
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return p, nil
}

func ValidateProvider(p *Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	if !p.Platform.Valid() {
		return fmt.Errorf("%w: invalid platform", ErrInvalidInput)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("%w: base_url required", ErrInvalidInput)
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 120
	}
	if p.MaxRetries < 0 {
		p.MaxRetries = 3
	}
	if p.Models == nil {
		p.Models = map[string]llm.ModelParams{}
	}
	switch strings.TrimSpace(p.ResponseFormat) {
	case "", llm.ResponseFormatJSONObject:
		p.ResponseFormat = llm.ResponseFormatJSONObject
	case llm.ResponseFormatJSONSchema:
		p.ResponseFormat = llm.ResponseFormatJSONSchema
	default:
		return fmt.Errorf("%w: invalid response_format", ErrInvalidInput)
	}
	return nil
}

func ValidatePurposeBinding(b *PurposeBinding) error {
	if strings.TrimSpace(b.ProviderID) == "" {
		return fmt.Errorf("%w: provider_id required", ErrInvalidInput)
	}
	if strings.TrimSpace(b.Model) == "" {
		return fmt.Errorf("%w: model required", ErrInvalidInput)
	}
	return nil
}

func ProviderToRuntime(p Provider) *llm.ProviderRuntime {
	return &llm.ProviderRuntime{
		ProviderID:     p.ProviderID,
		Platform:       p.Platform,
		BaseURL:        p.BaseURL,
		APIKey:         p.APIKey,
		TimeoutSeconds: p.TimeoutSeconds,
		MaxRetries:     p.MaxRetries,
		ResponseFormat: p.ResponseFormat,
		Models:         map[string]llm.ModelParams{},
	}
}

func BindingsToRouteSnapshot(bindings map[string]PurposeBinding, providers map[string]*llm.ProviderRuntime) llm.RouteSnapshot {
	routes := make(map[string]llm.PurposeRoute, len(bindings))
	for purpose, b := range bindings {
		if b.ProviderID == "" {
			continue
		}
		routes[purpose] = llm.PurposeRoute{
			ProviderID:  b.ProviderID,
			ModelParams: b.ModelParams,
		}
	}
	return llm.RouteSnapshot{Routes: routes, Providers: providers}
}
