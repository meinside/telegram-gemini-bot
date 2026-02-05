// config.go
//
// things for configuration of the bot

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"

	// infisical
	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
)

const (
	defaultLocation   = `global`
	defaultBucketName = `telegram-gemini-bot`
)

// config struct for loading a configuration file
type config struct {
	SystemInstruction *string `json:"system_instruction,omitempty"`

	// models
	GoogleGenerativeModel                    *string `json:"google_generative_model,omitempty"`
	GoogleGenerativeModelForImageGeneration  *string `json:"google_generative_model_for_image_generation,omitempty"`
	GoogleGenerativeModelForVideoGeneration  *string `json:"google_generative_model_for_video_generation,omitempty"`
	GoogleGenerativeModelForSpeechGeneration *string `json:"google_generative_model_for_speech_generation,omitempty"`

	// google ai speech generation settings
	GoogleGenerativeModelForSpeechGenerationVoice *string `json:"google_generative_model_for_speech_generation_voice,omitempty"`

	// configurations
	AllowedTelegramUsers  []string `json:"allowed_telegram_users"`
	RequestLogsDBFilepath string   `json:"db_filepath,omitempty"`
	AnswerTimeoutSeconds  int      `json:"answer_timeout_seconds,omitempty"`
	Verbose               bool     `json:"verbose,omitempty"`

	// telegram bot
	TelegramBotToken *string `json:"telegram_bot_token,omitempty"`

	// google credentials
	GoogleAIAPIKey            *string `json:"google_ai_api_key,omitempty"`
	GoogleCredentialsFilepath *string `json:"google_credentials_filepath,omitempty"`
	Location                  *string `json:"location,omitempty"`
	BucketName                *string `json:"bucket,omitempty"`

	// or Infisical settings
	Infisical *infisicalSetting `json:"infisical,omitempty"`
}

// infisical setting struct
type infisicalSetting struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`

	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	SecretType  string `json:"secret_type"`

	TelegramBotTokenKeyPath string `json:"telegram_bot_token_key_path"`
	GoogleAIAPIKeyKeyPath   string `json:"google_ai_api_key_key_path"`
}

// load config at given path
func loadConfig(
	ctxBg context.Context,
	fpath string,
) (conf config, err error) {
	var bytes []byte
	if bytes, err = os.ReadFile(fpath); err == nil {
		if bytes, err = standardizeJSON(bytes); err == nil {
			if err = json.Unmarshal(bytes, &conf); err == nil {
				if (conf.TelegramBotToken == nil || conf.GoogleAIAPIKey == nil) &&
					conf.Infisical != nil {
					ctxInfisical, cancelInfisical := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
					defer cancelInfisical()

					// read token and api key from infisical
					client := infisical.NewInfisicalClient(
						ctxInfisical,
						infisical.Config{
							SiteUrl: "https://app.infisical.com",
						},
					)

					_, err = client.Auth().UniversalAuthLogin(
						conf.Infisical.ClientID,
						conf.Infisical.ClientSecret,
					)
					if err != nil {
						return config{}, fmt.Errorf("failed to authenticate with Infisical: %s", err)
					}

					var keyPath string
					var secret models.Secret

					// telegram bot token
					keyPath = conf.Infisical.TelegramBotTokenKeyPath
					secret, err = client.Secrets().Retrieve(infisical.RetrieveSecretOptions{
						ProjectID:   conf.Infisical.ProjectID,
						Type:        conf.Infisical.SecretType,
						Environment: conf.Infisical.Environment,
						SecretPath:  path.Dir(keyPath),
						SecretKey:   path.Base(keyPath),
					})
					if err == nil {
						val := secret.SecretValue
						conf.TelegramBotToken = &val
					} else {
						return config{}, fmt.Errorf("failed to retrieve `telegram_bot_token` from Infisical: %s", err)
					}

					// google ai api key
					keyPath = conf.Infisical.GoogleAIAPIKeyKeyPath
					secret, err = client.Secrets().Retrieve(infisical.RetrieveSecretOptions{
						ProjectID:   conf.Infisical.ProjectID,
						Type:        conf.Infisical.SecretType,
						Environment: conf.Infisical.Environment,
						SecretPath:  path.Dir(keyPath),
						SecretKey:   path.Base(keyPath),
					})
					if err == nil {
						val := secret.SecretValue
						conf.GoogleAIAPIKey = &val
					} else {
						return config{}, fmt.Errorf("failed to retrieve `google_ai_api_key` from Infisical: %s", err)
					}
				}

				// set default/fallback values
				if conf.GoogleGenerativeModel == nil {
					conf.GoogleGenerativeModel = ptr(defaultGenerativeModel)
				}
				if conf.GoogleGenerativeModelForImageGeneration == nil {
					conf.GoogleGenerativeModelForImageGeneration = ptr(defaultGenerativeModelForImageGeneration)
				}
				if conf.GoogleGenerativeModelForVideoGeneration == nil {
					conf.GoogleGenerativeModelForVideoGeneration = ptr(defaultGenerativeModelForVideoGeneration)
				}
				if conf.GoogleGenerativeModelForSpeechGeneration == nil {
					conf.GoogleGenerativeModelForSpeechGeneration = ptr(defaultGenerativeModelForSpeechGeneration)
				}
				if conf.GoogleGenerativeModelForSpeechGenerationVoice == nil {
					conf.GoogleGenerativeModelForSpeechGenerationVoice = ptr(defaultGenerativeModelForSpeechGenerationVoice)
				}
				if conf.AnswerTimeoutSeconds <= 0 {
					conf.AnswerTimeoutSeconds = longRequestTimeoutSeconds
				}
				if conf.Location == nil {
					conf.Location = ptr(defaultLocation)
				}
				if conf.BucketName == nil {
					conf.BucketName = ptr(defaultBucketName)
				}

				// check the existence of essential values
				if conf.TelegramBotToken == nil ||
					(conf.GoogleAIAPIKey == nil && conf.GoogleCredentialsFilepath == nil) {
					err = fmt.Errorf("`telegram_bot_token` and/or `google_ai_api_key`/`google_credentials_filepath` values are missing")
				}
			}
		}
	}

	return conf, err
}
