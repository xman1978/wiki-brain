ALTER TABLE llm_purpose_bindings ADD COLUMN model_config TEXT NOT NULL DEFAULT '{}';

UPDATE llm_purpose_bindings
SET model_config = COALESCE(
    (SELECT json_extract(models, '$.default') FROM llm_providers p WHERE p.provider_id = llm_purpose_bindings.provider_id),
    '{}'
)
WHERE purpose = 'default';

UPDATE llm_purpose_bindings
SET model_config = COALESCE(
    (SELECT json_extract(models, '$.reasoning') FROM llm_providers p WHERE p.provider_id = llm_purpose_bindings.provider_id),
    '{}'
)
WHERE purpose = 'reasoning';

UPDATE llm_purpose_bindings
SET model_config = COALESCE(
    (SELECT json_extract(models, '$.extraction') FROM llm_providers p WHERE p.provider_id = llm_purpose_bindings.provider_id),
    '{}'
)
WHERE purpose = 'extraction';

UPDATE llm_purpose_bindings
SET model_config = COALESCE(
    (SELECT json_extract(models, '$.classification') FROM llm_providers p WHERE p.provider_id = llm_purpose_bindings.provider_id),
    '{}'
)
WHERE purpose = 'classification';
