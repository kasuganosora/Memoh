-- 0080_add_image_model_type
-- Add 'image' to the models type constraint and migrate existing image models.
ALTER TABLE models DROP CONSTRAINT models_type_check;
ALTER TABLE models ADD CONSTRAINT models_type_check CHECK (type = ANY (ARRAY['chat', 'embedding', 'speech', 'transcription', 'image']));
-- Migrate existing image models (openai-images provider) from chat to image type.
UPDATE models SET type = 'image' FROM providers WHERE models.provider_id = providers.id AND providers.client_type = 'openai-images';
