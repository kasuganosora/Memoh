package audio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db/sqlc"
	"github.com/memohai/memoh/internal/models"
)

func SyncRegistry(ctx context.Context, logger *slog.Logger, queries *sqlc.Queries, registry *Registry) error {
	for _, def := range registry.List() {
		provider, err := queries.GetProviderByClientType(ctx, string(def.ClientType))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Provider not yet in DB – create it from registry definition so
				// that speech models can be synced.  This covers providers whose
				// YAML template may not have been loaded before the audio bootstrap.
				var icon pgtype.Text
				if def.Icon != "" {
					icon = pgtype.Text{String: def.Icon, Valid: true}
				}
				providerCfg, cfgErr := json.Marshal(map[string]any{})
				if cfgErr != nil {
					return fmt.Errorf("marshal empty config for %s: %w", def.ClientType, cfgErr)
				}
				provider, err = queries.UpsertRegistryProvider(ctx, sqlc.UpsertRegistryProviderParams{
					Name:       def.DisplayName,
					ClientType: string(def.ClientType),
					Icon:       icon,
					Config:     providerCfg,
				})
				if err != nil {
					return fmt.Errorf("auto-create provider %s: %w", def.ClientType, err)
				}
				if logger != nil {
					logger.Info("audio registry auto-created provider",
						slog.String("provider", string(def.ClientType)),
						slog.String("display_name", def.DisplayName))
				}
			} else {
				if logger != nil {
					logger.Warn("audio registry failed to load provider template",
						slog.String("provider", string(def.ClientType)),
						slog.String("display_name", def.DisplayName),
						slog.Any("error", err))
				}
				return fmt.Errorf("get provider by client type %s: %w", def.ClientType, err)
			}
		}

		synced := 0
		if !isTranscriptionClientType(def.ClientType) {
			for _, model := range def.Models {
				if shouldHideTemplateModel(def, models.ModelTypeSpeech, model.ID) {
					if err := queries.DeleteModelByProviderAndType(ctx, sqlc.DeleteModelByProviderAndTypeParams{
						ProviderID: provider.ID,
						ModelID:    model.ID,
						Type:       string(models.ModelTypeSpeech),
					}); err != nil {
						return fmt.Errorf("delete hidden speech template model %s: %w", model.ID, err)
					}
					continue
				}
				modelConfigJSON, err := json.Marshal(map[string]any{})
				if err != nil {
					return fmt.Errorf("marshal speech model config: %w", err)
				}
				name := pgtype.Text{String: model.Name, Valid: model.Name != ""}
				if _, err := queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
					ModelID:    model.ID,
					Name:       name,
					ProviderID: provider.ID,
					Type:       string(models.ModelTypeSpeech),
					Config:     modelConfigJSON,
				}); err != nil {
					return fmt.Errorf("upsert speech model %s: %w", model.ID, err)
				}
				synced++
			}
		}
		for _, model := range def.TranscriptionModels {
			if shouldHideTemplateModel(def, models.ModelTypeTranscription, model.ID) {
				if err := queries.DeleteModelByProviderAndType(ctx, sqlc.DeleteModelByProviderAndTypeParams{
					ProviderID: provider.ID,
					ModelID:    model.ID,
					Type:       string(models.ModelTypeTranscription),
				}); err != nil {
					return fmt.Errorf("delete hidden transcription template model %s: %w", model.ID, err)
				}
				continue
			}
			modelConfigJSON, err := json.Marshal(map[string]any{})
			if err != nil {
				return fmt.Errorf("marshal transcription model config: %w", err)
			}
			name := pgtype.Text{String: model.Name, Valid: model.Name != ""}
			if _, err := queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
				ModelID:    model.ID,
				Name:       name,
				ProviderID: provider.ID,
				Type:       string(models.ModelTypeTranscription),
				Config:     modelConfigJSON,
			}); err != nil {
				return fmt.Errorf("upsert transcription model %s: %w", model.ID, err)
			}
		}

		if logger != nil {
			logger.Info("speech registry synced", slog.String("provider", string(def.ClientType)), slog.Int("models", synced))
		}
	}
	return nil
}
