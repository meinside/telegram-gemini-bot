package main

// bot.go

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/meinside/infisical-go"
	"github.com/meinside/infisical-go/helper"
	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"

	"github.com/google/generative-ai-go/genai"
	"github.com/tailscale/hujson"
	"google.golang.org/api/option"
)

const (
	defaultGenerationModel = "gemini-pro"
	defaultMultimodalModel = "gemini-pro-vision"
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

<i>version: %s</i>
`

	defaultPromptForPhotos   = "Describe provided image(s)."
	defaultPromptForDocument = "Describe provided document."
)

type chatMessageRole string

const (
	chatMessageRoleUser      = "user"
	chatMessageRoleAssistant = "assistant"
)

type chatMessage struct {
	role  chatMessageRole
	text  string
	files [][]byte
}

// config struct for loading a configuration file
type config struct {
	GoogleGenerativeModel string `json:"google_generative_model,omitempty"`
	GoogleMultimodalModel string `json:"google_multimodal_model,omitempty"`

	// google ai safety settings threshold
	GoogleAIHarmBlockThreshold int `json:"google_ai_harm_block_threshold,omitempty"`

	// configurations
	AllowedTelegramUsers  []string `json:"allowed_telegram_users"`
	RequestLogsDBFilepath string   `json:"db_filepath,omitempty"`
	Verbose               bool     `json:"verbose,omitempty"`

	// telegram bot and google api tokens
	TelegramBotToken string `json:"telegram_bot_token,omitempty"`
	GoogleAIAPIKey   string `json:"google_ai_api_key"`

	// or Infisical settings
	Infisical *struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`

		WorkspaceID string               `json:"workspace_id"`
		Environment string               `json:"environment"`
		SecretType  infisical.SecretType `json:"secret_type"`

		TelegramBotTokenKeyPath string `json:"telegram_bot_token_key_path"`
		GoogleAIAPIKeyKeyPath   string `json:"google_ai_api_key_key_path"`
	} `json:"infisical,omitempty"`
}

// load config at given path
func loadConfig(fpath string) (conf config, err error) {
	var bytes []byte
	if bytes, err = os.ReadFile(fpath); err == nil {
		if bytes, err = standardizeJSON(bytes); err == nil {
			if err = json.Unmarshal(bytes, &conf); err == nil {
				if (conf.TelegramBotToken == "" || conf.GoogleAIAPIKey == "") && conf.Infisical != nil {
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
						conf.TelegramBotToken = botToken
					}
					if apiKey, exists = kvs[conf.Infisical.GoogleAIAPIKeyKeyPath]; exists {
						conf.GoogleAIAPIKey = apiKey
					}
				}

				// set default/fallback values
				if conf.GoogleGenerativeModel == "" {
					conf.GoogleGenerativeModel = defaultGenerationModel
				}
				if conf.GoogleMultimodalModel == "" {
					conf.GoogleMultimodalModel = defaultMultimodalModel
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

// launch bot with given parameters
func runBot(conf config) {
	token := conf.TelegramBotToken
	apiKey := conf.GoogleAIAPIKey

	allowedUsers := map[string]bool{}
	for _, user := range conf.AllowedTelegramUsers {
		allowedUsers[user] = true
	}

	bot := tg.NewClient(token)

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Printf("failed to create API client: %s", err)

		os.Exit(1)
	}
	defer client.Close()

	_ = bot.DeleteWebhook(false) // delete webhook before polling updates
	if b := bot.GetMe(); b.Ok {
		log.Printf("launching bot: %s", userName(b.Result))

		var db *Database = nil
		if conf.RequestLogsDBFilepath != "" {
			var err error
			if db, err = OpenDatabase(conf.RequestLogsDBFilepath); err != nil {
				log.Printf("failed to open request logs db: %s", err)
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
					_ = sendMessage(b, conf, msgTypeNotSupported, message.Chat.ID, &message.MessageID, true)
				}
			} else {
				log.Printf("failed to poll updates: %s", err)
			}
		})
	} else {
		log.Printf("failed to get bot info: %s", *b.Description)
	}
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

	if msg := usableMessageFromUpdate(update); msg != nil {
		messages := chatMessagesFromTGMessage(bot, *msg)
		if len(messages) > 0 {
			answer(ctx, bot, client, conf, db, messages, chatID, userID, userNameFromUpdate(update), messageID)

			return
		} else {
			log.Printf("no converted chat messages from update: %+v", update)
		}
	} else {
		log.Printf("no usable message from update: %+v", update)
	}

	_ = sendMessage(bot, conf, "Failed to get usable chat messages from your input. See the server logs for more information.", chatID, &messageID, false)
}

// get usable message from given update
func usableMessageFromUpdate(update tg.Update) (message *tg.Message) {
	if update.HasMessage() && (update.Message.HasText() || update.Message.HasPhoto() || update.Message.HasDocument()) {
		message = update.Message
	} else if update.HasEditedMessage() && (update.EditedMessage.HasText() || update.EditedMessage.HasPhoto() || update.EditedMessage.HasDocument()) {
		message = update.EditedMessage
	}

	return message
}

// convert telegram bot message into chat messages
func chatMessagesFromTGMessage(bot *tg.Bot, message tg.Message) (chatMessages []chatMessage) {
	chatMessages = []chatMessage{}

	replyTo := repliedToMessage(message)

	// chat message 1
	if replyTo != nil {
		if chatMessage := convertMessage(bot, *replyTo); chatMessage != nil {
			chatMessages = append(chatMessages, *chatMessage)
		}
	}

	// chat message 2
	if chatMessage := convertMessage(bot, message); chatMessage != nil {
		chatMessages = append(chatMessages, *chatMessage)
	}

	return chatMessages
}

// send given text to the chat
func sendMessage(bot *tg.Bot, conf config, message string, chatID int64, messageID *int64, useMarkdown bool) (err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] sending message to chat(%d): '%s'", chatID, message)
	}

	options := tg.OptionsSendMessage{}
	if useMarkdown {
		options.SetParseMode(tg.ParseModeMarkdown)
	}
	if messageID != nil {
		options.SetReplyParameters(tg.ReplyParameters{
			MessageID: *messageID,
		})
	}
	if res := bot.SendMessage(chatID, message, options); !res.Ok {
		// FIXME: if the error was due to the malformed markdown, try again without markdown
		if strings.Contains(*res.Description, "Bad Request: can't parse entities") {
			return sendMessage(bot, conf, message, chatID, messageID, false)
		} else {
			err = fmt.Errorf("failed to send message: %s (requested message: %s)", *res.Description, message)
		}
	}

	return err
}

// send given blob data as a document to the chat
func sendFile(bot *tg.Bot, conf config, data []byte, chatID int64, messageID *int64, caption *string) (err error) {
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
	if res := bot.SendDocument(chatID, tg.NewInputFileFromBytes(data), options); !res.Ok {
		err = fmt.Errorf("failed to send document: %s", *res.Description)
	}

	return err
}

func countTokens(ctx context.Context, model *genai.GenerativeModel, parts ...genai.Part) (count int32, err error) {
	if len(parts) == 0 {
		return 0, fmt.Errorf("cannot count tokens of an empty parts array")
	}

	var counted *genai.CountTokensResponse
	if counted, err = model.CountTokens(ctx, parts...); err == nil {
		count = counted.TotalTokens
	}
	return count, err
}

// generate an answer to given message and send it to the chat
func answer(ctx context.Context, bot *tg.Bot, client *genai.Client, conf config, db *Database, messages []chatMessage, chatID, userID int64, username string, messageID int64) {
	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(chatID, messageID, tg.NewMessageReactionWithEmoji("👌"))

	multimodal := false
	texts := []genai.Part{}
	for _, message := range messages {
		texts = append(texts, genai.Text(message.text))
		for _, file := range message.files {
			texts = append(texts, genai.Blob{
				MIMEType: http.DetectContentType(file),
				Data:     file,
			})
			multimodal = true
		}
	}

	var model *genai.GenerativeModel
	if multimodal {
		model = client.GenerativeModel(conf.GoogleMultimodalModel)
	} else {
		model = client.GenerativeModel(conf.GoogleGenerativeModel)
	}

	// set safety filters
	model.SafetySettings = safetySettings(genai.HarmBlockThreshold(conf.GoogleAIHarmBlockThreshold))

	if generated, err := model.GenerateContent(ctx, texts...); err == nil {
		if conf.Verbose {
			log.Printf("[verbose] %+v ===> %+v", messages, generated)
		}

		var content *genai.Content
		var parts []genai.Part

		if len(generated.Candidates) > 0 {
			content = generated.Candidates[0].Content

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
					if err := sendFile(bot, conf, []byte(generatedText), chatID, &messageID, &caption); err == nil {
						numTokensInput, _ := countTokens(ctx, model, texts...)
						numTokensOutput, _ := countTokens(ctx, model, parts...)

						// save to database (successful)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
					} else {
						log.Printf("failed to answer messages '%+v' with '%s' as file: %s", messages, parts, err)

						_ = sendMessage(bot, conf, "Failed to send you the answer as a text file. See the server logs for more information.", chatID, &messageID, false)

						numTokensInput, _ := countTokens(ctx, model, texts...)
						numTokensOutput, _ := countTokens(ctx, model, parts...)

						// save to database (error)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
					}
				} else {
					if err := sendMessage(bot, conf, generatedText, chatID, &messageID, true); err == nil {
						numTokensInput, _ := countTokens(ctx, model, texts...)
						numTokensOutput, _ := countTokens(ctx, model, parts...)

						// save to database (successful)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
					} else {
						log.Printf("failed to answer messages '%+v' with '%+v': %s", messages, parts, err)

						_ = sendMessage(bot, conf, "Failed to send you the answer as a text. See the server logs for more information.", chatID, &messageID, false)

						numTokensInput, _ := countTokens(ctx, model, texts...)
						numTokensOutput, _ := countTokens(ctx, model, parts...)

						// save to database (error)
						savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
					}
				}
			} else if blob, ok := part.(genai.Blob); ok { // (blob)
				caption := fmt.Sprintf("%d byte(s) of %s", len(blob.Data), blob.MIMEType)
				if err := sendFile(bot, conf, blob.Data, chatID, &messageID, &caption); err == nil {
					numTokensInput, _ := countTokens(ctx, model, texts...)
					numTokensOutput, _ := countTokens(ctx, model, parts...)

					generatedText := fmt.Sprintf("%d bytes of %s", len(blob.Data), blob.MIMEType)

					// save to database (successful)
					savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), generatedText, uint(numTokensOutput), true)
				} else {
					log.Printf("failed to answer messages '%+v' with '%s' as file: %s", messages, parts, err)

					_ = sendMessage(bot, conf, "Failed to send you the answer as a text file. See the server logs for more information.", chatID, &messageID, false)

					numTokensInput, _ := countTokens(ctx, model, texts...)
					numTokensOutput, _ := countTokens(ctx, model, parts...)

					// save to database (error)
					savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), uint(numTokensInput), err.Error(), uint(numTokensOutput), false)
				}
			} else {
				log.Printf("unsupported type of part: %+v", part)
			}
		}
	} else {
		log.Printf("failed to create chat completion: %s", err)

		_ = sendMessage(bot, conf, fmt.Sprintf("Failed to generate an answer from Gemini: %s", err), chatID, &messageID, false)

		// save to database (error)
		savePromptAndResult(db, chatID, userID, username, messagesToPrompt(messages), 0, err.Error(), 0, false)
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
	if message.ReplyToMessage != nil {
		return message.ReplyToMessage
	}

	return nil
}

// convert given telegram bot message to an genai chat message,
//
// return nil if there was any error.
//
// (if it was sent from bot, make it an assistant's message)
func convertMessage(bot *tg.Bot, message tg.Message) *chatMessage {
	if message.ViaBot != nil &&
		message.ViaBot.IsBot {
		if message.HasPhoto() {
			var text string
			if message.HasCaption() {
				text = *message.Caption
			} else {
				text = defaultPromptForPhotos
			}

			photos := [][]byte{}
			for _, photo := range message.Photo {
				if bytes, err := photoBytes(bot, &photo); err == nil {
					photos = append(photos, bytes)
				} else {
					log.Printf("failed to read photo content for assistant message: %s", err)
				}
			}

			return &chatMessage{
				role:  chatMessageRoleAssistant,
				text:  text,
				files: photos,
			}
		} else if message.HasText() {
			return &chatMessage{
				role: chatMessageRoleAssistant,
				text: *message.Text,
			}
		} else if message.HasDocument() {
			var text string
			if message.HasCaption() {
				text = *message.Caption
			} else {
				text = defaultPromptForDocument
			}

			if bytes, err := documentBytes(bot, message.Document); err == nil {
				return &chatMessage{
					role:  chatMessageRoleAssistant,
					text:  text,
					files: [][]byte{bytes},
				}
			} else {
				log.Printf("failed to read document content for assistant message: %s", err)
			}
		}
	}

	if message.HasPhoto() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForPhotos
		}

		photos := [][]byte{}
		for _, photo := range message.Photo {
			if bytes, err := photoBytes(bot, &photo); err == nil {
				photos = append(photos, bytes)
			} else {
				log.Printf("failed to read photo content for user message: %s", err)
			}
		}

		return &chatMessage{
			role:  chatMessageRoleUser,
			text:  text,
			files: photos,
		}
	} else if message.HasText() {
		return &chatMessage{
			role: chatMessageRoleUser,
			text: *message.Text,
		}
	} else if message.HasDocument() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForDocument
		}

		if bytes, err := documentBytes(bot, message.Document); err == nil {
			return &chatMessage{
				role:  chatMessageRoleUser,
				text:  text,
				files: [][]byte{bytes},
			}
		} else {
			log.Printf("failed to read document content for user message: %s", err)
		}
	}

	return nil
}

// read bytes from given photo
func photoBytes(bot *tg.Bot, photo *tg.PhotoSize) (result []byte, err error) {
	if res := bot.GetFile(photo.FileID); !res.Ok {
		err = fmt.Errorf("Failed to get photo: %s", *res.Description)
	} else {
		fileURL := bot.GetFileURL(*res.Result)
		result, err = readFileContentAtURL(fileURL)
	}

	return result, err
}

// read bytes from given document
func documentBytes(bot *tg.Bot, document *tg.Document) (result []byte, err error) {
	if res := bot.GetFile(document.FileID); !res.Ok {
		err = fmt.Errorf("Failed to get document: %s", *res.Description)
	} else {
		fileURL := bot.GetFileURL(*res.Result)
		result, err = readFileContentAtURL(fileURL)
	}

	return result, err
}

// read file content at given url, will timeout in 60 seconds
func readFileContentAtURL(url string) (content []byte, err error) {
	httpClient := http.Client{
		Timeout: time.Second * 60,
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
func messagesToPrompt(messages []chatMessage) string {
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
			lines = append(lines, fmt.Sprintf("Since _%s_", prompt.CreatedAt.Format("2006-01-02 15:04:05")))
			lines = append(lines, "")
		}

		var count int64
		if tx := db.db.Table("prompts").Select("count(distinct chat_id) as count").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("* Chats: *%d*", count))
		}

		var sumAndCount struct {
			Sum   int64
			Count int64
		}
		if tx := db.db.Table("prompts").Select("sum(tokens) as sum, count(id) as count").Where("tokens > 0").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("* Prompts: *%d* (Total tokens: *%d*)", sumAndCount.Count, sumAndCount.Sum))
		}
		if tx := db.db.Table("generateds").Select("sum(tokens) as sum, count(id) as count").Where("successful = 1").Scan(&sumAndCount); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("* Completions: *%d* (Total tokens: *%d*)", sumAndCount.Count, sumAndCount.Sum))
		}
		if tx := db.db.Table("generateds").Select("count(id) as count").Where("successful = 0").Scan(&count); tx.Error == nil {
			lines = append(lines, fmt.Sprintf("* Errors: *%d*", count))
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
func helpMessage() string {
	return fmt.Sprintf(msgHelp, version.Build(version.OS|version.Architecture|version.Revision))
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

		_ = sendMessage(b, conf, msgStart, chatID, nil, false)
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

		_ = sendMessage(b, conf, retrieveStats(db), chatID, &messageID, true)
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

		_ = sendMessage(b, conf, helpMessage(), chatID, &messageID, true)
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

		_ = sendMessage(b, conf, fmt.Sprintf(msgCmdNotSupported, cmd), chatID, &messageID, true)
	}
}
