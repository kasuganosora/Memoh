-- 0080_add_image_model_type (down)
-- Revert image models back to chat type and restore original constraint.
UPDATE models SET type = 'chat' WHERE type = 'image';
ALTER TABLE models DROP CONSTRAINT models_type_check;
ALTER TABLE models ADD CONSTRAINT models_type_check CHECK (type = ANY (ARRAY['chat', 'embedding', 'speech', 'transcription']));
