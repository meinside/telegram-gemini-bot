// bot.go

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/meinside/infisical-go"
	"github.com/meinside/infisical-go/helper"
	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"

	"github.com/google/generative-ai-go/genai"
	"github.com/tailscale/hujson"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// constants for default values
const (
	defaultGenerationModel      = "gemini-pro"
	defaultMultimodalModel      = "gemini-pro-vision"
	defaultAIHarmBlockThreshold = 3

	defaultPromptForMedias = "Describe provided media(s)."

	defaultSystemInstruction = "You are a Telegram bot with a backend system which uses the Google Gemini API. Respond to the user's message as precisely as possible. Your response must be in plain text."
)

const (
	intervalSeconds = 1

	cmdStart = "/start"
	cmdStats = "/stats"
	cmdHelp  = "/help"

	msgStart                 = "This bot will answer your messages with Gemini API :-)"
	msgCmdNotSupported       = "Not a supported bot command: %s"
	msgTypeNotSupported      = "Not a supported message type."
	msgDatabaseNotConfigured = "Database not configured. Set `db_filepath` in your config file."
	msgDatabaseEmpty         = "Database is empty."
	msgHelp                  = `Help message here:

/stats : show stats of this bot.
/help : show this help message.

- models: %s / %s
- version: %s
`

	defaultAnswerTimeoutSeconds = 180 // 3 minutes

	readURLContentTimeoutSeconds = 60 // 1 minute

	uploadedFileStateCheckIntervalMilliseconds = 300

	redactedString = "<REDACTED>"
)

type chatMessageRole string

const (
	chatMessageRoleModel chatMessageRole = "model"
	chatMessageRoleUser  chatMessageRole = "user"
)

type chatMessage struct {
	role  chatMessageRole
	text  string
	files [][]byte
}

// config struct for loading a configuration file
type config struct {
	SystemInstruction *string `json:"system_instruction,omitempty"`

	GoogleGenerativeModel *string `json:"google_generative_model,omitempty"`
	GoogleMultimodalModel *string `json:"google_multimodal_model,omitempty"`

	// google ai safety settings threshold
	GoogleAIHarmBlockThreshold *int `json:"google_ai_harm_block_threshold,omitempty"`

	// configurations
	AllowedTelegramUsers  []string `json:"allowed_telegram_users"`
	RequestLogsDBFilepath string   `json:"db_filepath,omitempty"`
	StreamMessages        bool     `json:"stream_messages,omitempty"`
	AnswerTimeoutSeconds  int      `json:"answer_timeout_seconds,omitempty"`
	Verbose               bool     `json:"verbose,omitempty"`

	// telegram bot and google api tokens
	TelegramBotToken *string `json:"telegram_bot_token,omitempty"`
	GoogleAIAPIKey   *string `json:"google_ai_api_key,omitempty"`

	// or Infisical settings
	Infisical *infisicalSetting `json:"infisical,omitempty"`
}

// infisical setting struct
type infisicalSetting struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`

	WorkspaceID string               `json:"workspace_id"`
	Environment string               `json:"environment"`
	SecretType  infisical.SecretType `json:"secret_type"`

	TelegramBotTokenKeyPath string `json:"telegram_bot_token_key_path"`
	GoogleAIAPIKeyKeyPath   string `json:"google_ai_api_key_key_path"`
}

// load config at given path
func loadConfig(fpath string) (conf config, err error) {
	var bytes []byte
	if bytes, err = os.ReadFile(fpath); err == nil {
		if bytes, err = standardizeJSON(bytes); err == nil {
			if err = json.Unmarshal(bytes, &conf); err == nil {
				if (conf.TelegramBotToken == nil || conf.GoogleAIAPIKey == nil) && conf.Infisical != nil {
					// read token and api key from infisical
					var botToken, apiKey string

					var kvs map[string]string
					kvs, err = helper.Values(
						conf.Infisical.ClientID,
						conf.Infisical.ClientSecret,
						conf.Infisical.WorkspaceID,
						conf.Infisical.Environment,
						conf.Infisical.SecretType,
						[]string{
							conf.Infisical.TelegramBotTokenKeyPath,
							conf.Infisical.GoogleAIAPIKeyKeyPath,
						},
					)

					var exists bool
					if botToken, exists = kvs[conf.Infisical.TelegramBotTokenKeyPath]; exists {
						conf.TelegramBotToken = &botToken
					}
					if apiKey, exists = kvs[conf.Infisical.GoogleAIAPIKeyKeyPath]; exists {
						conf.GoogleAIAPIKey = &apiKey
					}
				}

				// set default/fallback values
				if conf.SystemInstruction == nil {
					conf.SystemInstruction = ptr(defaultSystemInstruction)
				}
				if conf.GoogleGenerativeModel == nil {
					conf.GoogleGenerativeModel = ptr(defaultGenerationModel)
				}
				if conf.GoogleMultimodalModel == nil {
					conf.GoogleMultimodalModel = ptr(defaultMultimodalModel)
				}
				if conf.GoogleAIHarmBlockThreshold == nil {
					conf.GoogleAIHarmBlockThreshold = ptr(defaultAIHarmBlockThreshold)
				}
				if conf.AnswerTimeoutSeconds == 0 {
					conf.AnswerTimeoutSeconds = defaultAnswerTimeoutSeconds
				}

				// check the existence of essential values
				if conf.TelegramBotToken == nil || conf.GoogleAIAPIKey == nil {
					err = fmt.Errorf("`telegram_bot_token` and/or `google_ai_api_key` values are missing")
				}
			}
		}
	}

	return conf, err
}

// standardize given JSON (JWCC) bytes
func standardizeJSON(b []byte) ([]byte, error) {
	ast, err := hujson.Parse(b)
	if err != nil {
		return b, err
	}
	ast.Standardize()

	return ast.Pack(), nil
}

// get the address (pointer) of a value
func ptr[T any](v T) *T {
	return &v
}

// launch bot with given parameters
func runBot(conf config) {
	token := conf.TelegramBotToken
	apiKey := conf.GoogleAIAPIKey

	allowedUsers := map[string]bool{}
	for _, user := range conf.AllowedTelegramUsers {
		allowedUsers[user] = true
	}

	bot := tg.NewClient(*token)

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(*apiKey))
	if err != nil {
		log.Printf("failed to create API client: %s", redact(conf, err))

		os.Exit(1)
	}
	defer client.Close()

	_ = bot.DeleteWebhook(false) // delete webhook before polling updates
	if b := bot.GetMe(); b.Ok {
		log.Printf("launching bot: %s", userName(b.Result))

		// clear things
		files := client.ListFiles(ctx)
		for {
			if file, err := files.Next(); err == nil {
				if err := client.DeleteFile(ctx, file.Name); err != nil {
					log.Printf("failed to delete cloud file: %s", err)
				}
			} else {
				if err == iterator.Done {
					break
				}
				log.Printf("failed to iterate cloud files: %s", err)
			}
		}

		// database
		var db *Database = nil
		if conf.RequestLogsDBFilepath != "" {
			var err error
			if db, err = OpenDatabase(conf.RequestLogsDBFilepath); err != nil {
				log.Printf("failed to open request logs db: %s", redact(conf, err))
			}
		}

		// set message handler
		bot.SetMessageHandler(func(b *tg.Bot, update tg.Update, message tg.Message, edited bool) {
			if !isAllowed(update, allowedUsers) {
				log.Printf("message not allowed: %s", userNameFromUpdate(update))
				return
			}

			handleMessage(ctx, b, client, conf, db, update, message)
		})

		// set command handlers
		bot.AddCommandHandler(cmdStart, startCommandHandler(conf, allowedUsers))
		bot.AddCommandHandler(cmdStats, statsCommandHandler(conf, db, allowedUsers))
		bot.AddCommandHandler(cmdHelp, helpCommandHandler(conf, allowedUsers))
		bot.SetNoMatchingCommandHandler(noSuchCommandHandler(conf, allowedUsers))

		// poll updates
		bot.StartPollingUpdates(0, intervalSeconds, func(b *tg.Bot, update tg.Update, err error) {
			if err == nil {
				if !isAllowed(update, allowedUsers) {
					log.Printf("not allowed: %s", userNameFromUpdate(update))
					return
				}

				// type not supported
				message := usableMessageFromUpdate(update)
				if message != nil {
					_, _ = sendMessage(b, conf, msgTypeNotSupported, message.Chat.ID, &message.MessageID)
				}
			} else {
				log.Printf("failed to poll updates: %s", redact(conf, err))
			}
		})
	} else {
		log.Printf("failed to get bot info: %s", *b.Description)
	}
}

// redact given error for logging and/or messaing
func redact(conf config, err error) (redacted string) {
	redacted = err.Error()

	if strings.Index(redacted, *conf.GoogleAIAPIKey) != -1 {
		redacted = strings.ReplaceAll(redacted, *conf.GoogleAIAPIKey, redactedString)
	}
	if strings.Index(redacted, *conf.TelegramBotToken) != -1 {
		redacted = strings.ReplaceAll(redacted, *conf.TelegramBotToken, redactedString)
	}

	return redacted
}

// redact given googleapi error for logging and/or messaing
func gerror(conf config, gerr *googleapi.Error) string {
	return redact(conf, fmt.Errorf("googleapi error: %s", gerr.Body))
}

// checks if given update is allowed or not
func isAllowed(update tg.Update, allowedUsers map[string]bool) bool {
	var username string
	if update.HasMessage() && update.Message.From.Username != nil {
		username = *update.Message.From.Username
	} else if update.HasEditedMessage() && update.EditedMessage.From.Username != nil {
		username = *update.EditedMessage.From.Username
	}

	if _, exists := allowedUsers[username]; exists {
		return true
	}

	return false
}

// handle allowed message update from telegram bot api
func handleMessage(ctx context.Context, bot *tg.Bot, client *genai.Client, conf config, db *Database, update tg.Update, message tg.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID
	messageID := message.MessageID

	var errMessage string
	if msg := usableMessageFromUpdate(update); msg != nil {
		if parent, original, err := chatMessagesFromTGMessage(bot, *msg); err == nil {
			if original != nil {
				ctx, cancel := context.WithTimeout(ctx, time.Duration(conf.AnswerTimeoutSeconds)*time.Second)
				defer cancel()

				answer(ctx, bot, client, conf, db, parent, original, chatID, userID, userNameFromUpdate(update), messageID)

				if err := ctx.Err(); err == nil {
					return
				}

				errMessage = fmt.Sprintf("Failed to answer in %d seconds: %s", conf.AnswerTimeoutSeconds, redact(conf, err))

				log.Printf(errMessage)
			} else {
				log.Printf("no converted chat messages from update: %+v", update)

				errMessage = fmt.Sprintf("There was no usable chat messages from telegram message.")
			}
		} else {
			log.Printf("failed to get chat messages from telegram message: %s", err)

			errMessage = fmt.Sprintf("Failed to get chat messages from telegram message: %s", redact(conf, err))
		}
	} else {
		log.Printf("no usable message from update: %+v", update)

		errMessage = fmt.Sprintf("There was no usable message from update.")
	}

	_, _ = sendMessage(bot, conf, errMessage, chatID, &messageID)
}

// get usable message from given update
func usableMessageFromUpdate(update tg.Update) (message *tg.Message) {
	if update.HasMessage() &&
		(update.Message.HasText() ||
			update.Message.HasPhoto() ||
			update.Message.HasVideo() ||
			update.Message.HasVideoNote() ||
			update.Message.HasAudio() ||
			update.Message.HasVoice() ||
			update.Message.HasDocument()) {
		message = update.Message
	} else if update.HasEditedMessage() &&
		(update.EditedMessage.HasText() ||
			update.EditedMessage.HasPhoto() ||
			update.EditedMessage.HasVideo() ||
			update.EditedMessage.HasVideoNote() ||
			update.EditedMessage.HasAudio() ||
			update.EditedMessage.HasVoice() ||
			update.EditedMessage.HasDocument()) {
		message = update.EditedMessage
	}

	return message
}

// convert telegram bot message into chat messages
func chatMessagesFromTGMessage(bot *tg.Bot, message tg.Message) (parent, original *chatMessage, err error) {
	replyTo := repliedToMessage(message)
	errs := []error{}

	// chat message 1 (parent message)
	if replyTo != nil {
		if chatMessage, err := convertMessage(bot, *replyTo); err == nil {
			parent = chatMessage
		} else {
			errs = append(errs, err)
		}
	}

	// chat message 2 (original message)
	if chatMessage, err := convertMessage(bot, message); err == nil {
		original = chatMessage
	} else {
		errs = append(errs, err)
	}

	return parent, original, errors.Join(errs...)
}

// send given text to the chat
func sendMessage(bot *tg.Bot, conf config, message string, chatID int64, messageID *int64) (sentMessageID int64, err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] sending message to chat(%d): '%s'", chatID, message)
	}

	options := tg.OptionsSendMessage{}
	if messageID != nil {
		options.SetReplyParameters(tg.ReplyParameters{
			MessageID: *messageID,
		})
	}
	if res := bot.SendMessage(chatID, message, options); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send message: %s (requested message: %s)", *res.Description, message)
	}

	return sentMessageID, err
}

// update a message in the chat
func updateMessage(bot *tg.Bot, conf config, message string, chatID int64, messageID int64) (err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] updating message in chat(%d): '%s'", chatID, message)
	}

	options := tg.OptionsEditMessageText{}.
		SetIDs(chatID, messageID)
	if res := bot.EditMessageText(message, options); !res.Ok {
		err = fmt.Errorf("failed to send message: %s (requested message: %s)", *res.Description, message)
	}

	return err
}

// send given blob data as a document to the chat
func sendFile(bot *tg.Bot, conf config, data []byte, chatID int64, messageID *int64, caption *string) (sentMessageID int64, err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] sending document to chat(%d): %d bytes of data", chatID, len(data))
	}

	options := tg.OptionsSendDocument{}
	if messageID != nil {
		options.SetReplyParameters(tg.ReplyParameters{
			MessageID: *messageID,
		})
	}
	if caption != nil {
		options.SetCaption(*caption)
	}
	if res := bot.SendDocument(chatID, tg.NewInputFileFromBytes(data), options); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send document: %s", *res.Description)
	}

	return sentMessageID, err
}

// generate an answer to given message and send it to the chat
func answer(ctx context.Context, bot *tg.Bot, client *genai.Client, conf config, db *Database, parent, original *chatMessage, chatID, userID int64, username string, messageID int64) {
	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(chatID, messageID, tg.NewMessageReactionWithEmoji("👌"))

	multimodal := (original != nil && len(original.files) > 0) || (parent != nil && len(parent.files) > 0)

	// model
	var model *genai.GenerativeModel
	if multimodal {
		model = client.GenerativeModel(*conf.GoogleMultimodalModel)
	} else {
		model = client.GenerativeModel(*conf.GoogleGenerativeModel)
	}

	// set system instruction
	if !multimodal { // NOTE: FIXME: some multimodal models (eg. `gemini-pro-vision`) do not support system instructions yet
		model.SystemInstruction = &genai.Content{
			Role: string(chatMessageRoleModel),
			Parts: []genai.Part{
				genai.Text(*conf.SystemInstruction),
			},
		}
	}

	// prompt
	prompt := []genai.Part{}
	fileNames := []string{}
	if original != nil {
		// text
		prompt = append(prompt, genai.Text(original.text))

		// files
		for _, file := range original.files {
			mimeType := http.DetectContentType(file)

			if strings.HasPrefix(mimeType, "image/") {
				prompt = append(prompt, genai.Blob{
					MIMEType: mimeType,
					Data:     file,
				})
			} else {
				if file, err := client.UploadFile(ctx, "", bytes.NewReader(file), &genai.UploadFileOptions{
					MIMEType: mimeType,
				}); err == nil {
					prompt = append(prompt, genai.FileData{
						MIMEType: file.MIMEType,
						URI:      file.URI,
					})

					fileNames = append(fileNames, file.Name) // FIXME: will wait for it to become active
				} else {
					log.Printf("failed to upload file(%s) for prompt: %s", mimeType, redact(conf, err))
				}
			}
		}
	}

	// wait for all prompt files to become active
	waitForFiles(ctx, conf, client, fileNames)

	// set history
	session := model.StartChat()
	fileNames = []string{}
	if parent != nil {
		session.History = []*genai.Content{}

		// text
		parts := []genai.Part{
			genai.Text(parent.text),
		}

		// files
		for _, file := range parent.files {
			mimeType := http.DetectContentType(file)

			if strings.HasPrefix(mimeType, "image/") {
				parts = append(parts, genai.Blob{
					MIMEType: mimeType,
					Data:     file,
				})
			} else {
				if file, err := client.UploadFile(ctx, "", bytes.NewReader(file), &genai.UploadFileOptions{
					MIMEType: mimeType,
				}); err == nil {
					parts = append(parts, genai.FileData{
						MIMEType: file.MIMEType,
						URI:      file.URI,
					})

					fileNames = append(fileNames, file.Name) // FIXME: will wait for it to become active
				} else {
					log.Printf("failed to upload file(%s) for history: %s", mimeType, redact(conf, err))
				}
			}
		}

		session.History = append(session.History, &genai.Content{
			Role:  string(chatMessageRoleModel),
			Parts: parts,
		})
	}

	// wait for all prompt files to become active
	waitForFiles(ctx, conf, client, fileNames)

	// set safety filters
	model.SafetySettings = safetySettings(genai.HarmBlockThreshold(*conf.GoogleAIHarmBlockThreshold))

	// number of tokens for logging
	var numTokensInput int32 = 0
	var numTokensOutput int32 = 0

	if conf.StreamMessages {
		iter := session.SendMessageStream(ctx, prompt...)

		if conf.Verbose {
			log.Printf("[verbose] streaming [%+v + %+v] ===> %+v", parent, original, iter)
		}

		var firstMessageID *int64 = nil
		mergedText := ""

		for {
			if it, err := iter.Next(); err == nil {
				var candidate *genai.Candidate
				var content *genai.Content
				var parts []genai.Part

				if len(it.Candidates) > 0 {
					// update number of tokens
					numTokensInput = it.UsageMetadata.PromptTokenCount
					numTokensOutput = it.UsageMetadata.TotalTokenCount - it.UsageMetadata.PromptTokenCount

					candidate = it.Candidates[0]
					content = candidate.Content

					if len(content.Parts) > 0 {
						parts = content.Parts
					}
				}

				if conf.Verbose {
					log.Printf("[verbose] streaming answer to chat(%d): %+v", chatID, parts)
				}

				for _, part := range parts {
					if text, ok := part.(genai.Text); ok { // (text)
						generatedText := string(text)
						mergedText += generatedText

						if firstMessageID == nil { // send the first message
							if sentMessageID, err := sendMessage(bot, conf, generatedText, chatID, &messageID); err == nil {
								firstMessageID = &sentMessageID
							} else {
								log.Printf("failed to send stream messages [%+v + %+v] with '%+v': %s", parent, original, parts, redact(conf, err))
							}
						} else { // update the first message
							// update the first message (append text)
							if err := updateMessage(bot, conf, mergedText, chatID, *firstMessageID); err != nil {
								log.Printf("failed to update stream messages [%+v + %+v] with '%+v': %s", parent, original, parts, redact(conf, err))
							}
						}
					} else {
						log.Printf("unsupported type of part for streaming: %+v", part)
					}
				}
			} else {
				if err != iterator.Done {
					var error string
					var gerr *googleapi.Error
					if errors.As(err, &gerr) {
						error = gerror(conf, gerr)
					} else {
						error = redact(conf, err)
					}
					log.Printf("failed to iterate stream: %s", error)
				}
				break
			}
		}

		// log if it was successful or not
		successful := (func() bool {
			if firstMessageID != nil {
				// leave a reaction on the first message for notifying the termination of the stream
				_ = bot.SetMessageReaction(chatID, *firstMessageID, tg.NewMessageReactionWithEmoji("👌"))

				return true
			}
			return false
		})()
		savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), mergedText, uint(numTokensOutput), successful)
	} else {
		if generated, err := session.SendMessage(ctx, prompt...); err == nil {
			if conf.Verbose {
				log.Printf("[verbose] [%+v + %+v] ===> %+v", parent, original, generated)
			}

			var candidate *genai.Candidate
			var content *genai.Content
			var parts []genai.Part

			if len(generated.Candidates) > 0 {
				// update number of tokens
				numTokensInput = generated.UsageMetadata.PromptTokenCount
				numTokensOutput = generated.UsageMetadata.TotalTokenCount - generated.UsageMetadata.PromptTokenCount

				candidate = generated.Candidates[0]
				content = candidate.Content

				if len(content.Parts) > 0 {
					parts = content.Parts
				} else {
					parts = []genai.Part{genai.Text("There was no part in the generated candidate's content.")}
				}
			} else {
				parts = []genai.Part{genai.Text("There was no response from Gemini API.")}
			}

			if conf.Verbose {
				log.Printf("[verbose] sending answer to chat(%d): %+v", chatID, parts)
			}

			for _, part := range parts {
				if text, ok := part.(genai.Text); ok { // (text)
					generatedText := string(text)

					// if answer is too long for telegram api, send it as a text document
					if len(generatedText) > 4096 {
						caption := strings.ToValidUTF8(generatedText[:128], "") + "..."
						if sentMessageID, err := sendFile(bot, conf, []byte(generatedText), chatID, &messageID, &caption); err == nil {
							// leave a reaction on the sent message
							_ = bot.SetMessageReaction(chatID, sentMessageID, tg.NewMessageReactionWithEmoji("👌"))

							// save to database (successful)
							savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
						} else {
							log.Printf("failed to answer messages [%+v + %+v] with '%s' as file: %s", parent, original, parts, redact(conf, err))

							_, _ = sendMessage(bot, conf, "Failed to send you the answer as a text file. See the server logs for more information.", chatID, &messageID)

							// save to database (error)
							savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
						}
					} else {
						if sentMessageID, err := sendMessage(bot, conf, generatedText, chatID, &messageID); err == nil {
							// leave a reaction on the sent message
							_ = bot.SetMessageReaction(chatID, sentMessageID, tg.NewMessageReactionWithEmoji("👌"))

							// save to database (successful)
							savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
						} else {
							log.Printf("failed to answer messages [%+v + %+v] with '%+v': %s", parent, original, parts, redact(conf, err))

							_, _ = sendMessage(bot, conf, "Failed to send you the answer as a text. See the server logs for more information.", chatID, &messageID)

							// save to database (error)
							savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
						}
					}
				} else if blob, ok := part.(genai.Blob); ok { // (blob)
					caption := fmt.Sprintf("%d byte(s) of %s", len(blob.Data), blob.MIMEType)
					if sentMessageID, err := sendFile(bot, conf, blob.Data, chatID, &messageID, &caption); err == nil {
						// leave a reaction on the sent message
						_ = bot.SetMessageReaction(chatID, sentMessageID, tg.NewMessageReactionWithEmoji("👌"))

						generatedText := fmt.Sprintf("%d bytes of %s", len(blob.Data), blob.MIMEType)

						// save to database (successful)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
					} else {
						log.Printf("failed to answer messages [%+v + %+v] with '%s' as file: %s", parent, original, parts, redact(conf, err))

						_, _ = sendMessage(bot, conf, "Failed to send you the answer as a text file. See the server logs for more information.", chatID, &messageID)

						// save to database (error)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
					}
				} else {
					log.Printf("unsupported type of part: %+v", part)
				}
			}
		} else {
			var error string
			var gerr *googleapi.Error
			if errors.As(err, &gerr) {
				error = gerror(conf, gerr)
			} else {
				error = redact(conf, err)
			}

			log.Printf("failed to create chat completion: %s", error)

			_, _ = sendMessage(bot, conf, fmt.Sprintf("Failed to generate an answer from Gemini: %s", error), chatID, &messageID)

			// save to database (error)
			savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), 0, error, 0, false)
		}
	}
}

// wait for all files to be active
func waitForFiles(ctx context.Context, conf config, client *genai.Client, fileNames []string) {
	if conf.Verbose {
		log.Printf("[verbose] will wait for %d file(s) to become active", len(fileNames))
	}

	var wg sync.WaitGroup
	for _, fileName := range fileNames {
		wg.Add(1)

		go func(name string) {
			for {
				if file, err := client.GetFile(ctx, name); err == nil {
					if file.State == genai.FileStateActive {
						wg.Done()
						break
					} else {
						time.Sleep(uploadedFileStateCheckIntervalMilliseconds * time.Millisecond)
					}
				}
			}
		}(fileName)
	}
	wg.Wait()

	if conf.Verbose {
		log.Printf("[verbose] %d file(s) now active", len(fileNames))
	}
}

// generate safety settings for all supported harm categories
func safetySettings(threshold genai.HarmBlockThreshold) (settings []*genai.SafetySetting) {
	for _, category := range []genai.HarmCategory{
		/*
			// categories for PaLM 2 (Legacy) models
			genai.HarmCategoryUnspecified,
			genai.HarmCategoryDerogatory,
			genai.HarmCategoryToxicity,
			genai.HarmCategoryViolence,
			genai.HarmCategorySexual,
			genai.HarmCategoryMedical,
			genai.HarmCategoryDangerous,
		*/

		// all categories supported by Gemini models
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	} {
		settings = append(settings, &genai.SafetySetting{
			Category:  category,
			Threshold: threshold,
		})
	}

	return settings
}

// generate user's name
func userName(user *tg.User) string {
	if user.Username != nil {
		return fmt.Sprintf("@%s (%s)", *user.Username, user.FirstName)
	} else {
		return user.FirstName
	}
}

// generate user's name from update
func userNameFromUpdate(update tg.Update) string {
	if from := update.GetFrom(); from != nil {
		return userName(from)
	} else {
		return "unknown"
	}
}

// get original message which was replied by given `message`
func repliedToMessage(message tg.Message) *tg.Message {
	if message.HasReplyToMessage() {
		return message.ReplyToMessage
	}

	return nil
}

// convert given telegram bot message to an genai chat message,
//
// return nil if there was any error.
//
// (if it was sent from bot, make it an assistant's message)
func convertMessage(bot *tg.Bot, message tg.Message) (cm *chatMessage, err error) {
	var role chatMessageRole
	if message.IsBot() {
		role = chatMessageRoleModel
	} else {
		role = chatMessageRoleUser
	}

	if message.HasPhoto() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		photos := [][]byte{}
		var bytes []byte
		for _, photo := range message.Photo {
			if bytes, err = readMedia(bot, "photo", photo.FileID); err == nil {
				photos = append(photos, bytes)
			} else {
				err = fmt.Errorf("failed to read photo content for %s message: %s", role, err)
				break
			}
		}

		if err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: photos,
			}, nil
		}
	} else if message.HasText() {
		return &chatMessage{
			role: role,
			text: *message.Text,
		}, nil
	} else if message.HasVideo() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		var bytes []byte
		if bytes, err = readMedia(bot, "video", message.Video.FileID); err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: [][]byte{bytes},
			}, nil
		} else {
			err = fmt.Errorf("failed to read video content for %s message: %s", role, err)
		}
	} else if message.HasVideoNote() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		var bytes []byte
		if bytes, err = readMedia(bot, "video note", message.VideoNote.FileID); err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: [][]byte{bytes},
			}, nil
		} else {
			err = fmt.Errorf("failed to read video note content for %s message: %s", role, err)
		}
	} else if message.HasAudio() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		var bytes []byte
		if bytes, err = readMedia(bot, "audio", message.Audio.FileID); err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: [][]byte{bytes},
			}, nil
		} else {
			err = fmt.Errorf("failed to read audio content for %s message: %s", role, err)
		}
	} else if message.HasVoice() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		var bytes []byte
		if bytes, err = readMedia(bot, "voice", message.Voice.FileID); err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: [][]byte{bytes},
			}, nil
		} else {
			err = fmt.Errorf("failed to read voice content for %s message: %s", role, err)
		}
	} else if message.HasDocument() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		var bytes []byte
		if bytes, err = readMedia(bot, "document", message.Document.FileID); err == nil {
			return &chatMessage{
				role:  role,
				text:  text,
				files: [][]byte{bytes},
			}, nil
		} else {
			err = fmt.Errorf("failed to read document content for %s message: %s", role, err)
		}
	}

	return nil, err
}

// read bytes from given media
func readMedia(bot *tg.Bot, mediaType, fileID string) (result []byte, err error) {
	if res := bot.GetFile(fileID); !res.Ok {
		err = fmt.Errorf("Failed to read bytes from %s: %s", mediaType, *res.Description)
	} else {
		fileURL := bot.GetFileURL(*res.Result)
		result, err = readFileContentAtURL(fileURL)
	}

	return result, err
}

// read file content at given url
func readFileContentAtURL(url string) (content []byte, err error) {
	httpClient := http.Client{
		Timeout: time.Second * readURLContentTimeoutSeconds,
	}

	var resp *http.Response
	resp, err = httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	content, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// convert chat messages to a prompt for logging
func messagesToPrompt(parent, original *chatMessage) string {
	messages := []chatMessage{}
	if parent != nil {
		messages = append(messages, *parent)
	}
	if original != nil {
		messages = append(messages, *original)
	}

	lines := []string{}

	for _, message := range messages {
		if len(message.files) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] %s (%d file(s))", message.role, message.text, len(message.files)))
		} else {
			lines = append(lines, fmt.Sprintf("[%s] %s", message.role, message.text))
		}
	}

	return strings.Join(lines, "\n--------\n")
}

// retrieve stats from database
func retrieveStats(db *Database) string {
	if db == nil {
		return msgDatabaseNotConfigured
	} else {
		lines := []string{}

		var prompt Prompt
		if tx := db.db.First(&prompt); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Since %s", prompt.CreatedAt.Format("2006-01-02 15:04:05")))
			lines = append(lines, "")
		}

		printer := message.NewPrinter(language.English) // for adding commas to numbers

		var count int64
		if tx := db.db.Table("prompts").Select("count(distinct chat_id) as count").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Chats: %s", printer.Sprintf("%d", count)))
		}

		var sumAndCount struct {
			Sum   int64
			Count int64
		}
		if tx := db.db.Table("prompts").Select("sum(tokens) as sum, count(id) as count").Where("tokens > 0").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Prompts: %s (Total tokens: %s)", printer.Sprintf("%d", sumAndCount.Count), printer.Sprintf("%d", sumAndCount.Sum)))
		}
		if tx := db.db.Table("generateds").Select("sum(tokens) as sum, count(id) as count").Where("successful = 1").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Completions: %s (Total tokens: %s)", printer.Sprintf("%d", sumAndCount.Count), printer.Sprintf("%d", sumAndCount.Sum)))
		}
		if tx := db.db.Table("generateds").Select("count(id) as count").Where("successful = 0").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("Errors: %s", printer.Sprintf("%d", count)))
		}

		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}

		return msgDatabaseEmpty
	}
}

// save prompt and its result to logs database
func savePromptAndResult(db *Database, chatID, userID int64, username string, prompt string, promptTokens uint, result string, resultTokens uint, resultSuccessful bool) {
	if db != nil {
		if err := db.SavePrompt(Prompt{
			ChatID:   chatID,
			UserID:   userID,
			Username: username,
			Text:     prompt,
			Tokens:   promptTokens,
			Result: Generated{
				Successful: resultSuccessful,
				Text:       result,
				Tokens:     resultTokens,
			},
		}); err != nil {
			log.Printf("failed to save prompt & result to database: %s", err)
		}
	}
}

// generate a help message with version info
func helpMessage(conf config) string {
	return fmt.Sprintf(msgHelp, *conf.GoogleGenerativeModel, *conf.GoogleMultimodalModel, version.Build(version.OS|version.Architecture|version.Revision))
}

// return a /start command handler
func startCommandHandler(conf config, allowedUsers map[string]bool) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, _ string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("start command not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID

		_, _ = sendMessage(b, conf, msgStart, chatID, nil)
	}
}

// return a /stats command handler
func statsCommandHandler(conf config, db *Database, allowedUsers map[string]bool) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, args string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("stats command not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		_, _ = sendMessage(b, conf, retrieveStats(db), chatID, &messageID)
	}
}

// return a /help command handler
func helpCommandHandler(conf config, allowedUsers map[string]bool) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, _ string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("help command not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		_, _ = sendMessage(b, conf, helpMessage(conf), chatID, &messageID)
	}
}

// return a 'no such command' handler
func noSuchCommandHandler(conf config, allowedUsers map[string]bool) func(b *tg.Bot, update tg.Update, cmd, args string) {
	return func(b *tg.Bot, update tg.Update, cmd, args string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("command not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		_, _ = sendMessage(b, conf, fmt.Sprintf(msgCmdNotSupported, cmd), chatID, &messageID)
	}
}
