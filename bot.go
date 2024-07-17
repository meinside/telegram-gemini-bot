// bot.go

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	// google ai
	"github.com/google/generative-ai-go/genai"

	// infisical
	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"

	// my libraries
	tg "github.com/meinside/telegram-bot-go"

	// others
	"github.com/gabriel-vasile/mimetype"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// constants for default values
const (
	defaultGenerativeModel      = "gemini-1.5-pro-latest"
	defaultAIHarmBlockThreshold = 3

	defaultSystemInstructionFormat = `You are a Telegram bot which is built with Golang and Google Gemini API(model: %[1]s).

Current datetime is %[2]s.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
- Be as concise and meaningful as possible.
- Your response must be in plain text, so do not try to emphasize words with markdown characters.
`
)

const (
	intervalSeconds = 1

	cmdStart   = "/start"
	cmdStats   = "/stats"
	cmdPrivacy = "/privacy"
	cmdHelp    = "/help"

	descStats   = "show stats of this bot."
	descPrivacy = "show privacy policy of this bot."
	descHelp    = "show help message."

	msgStart                 = "This bot will answer your messages with Gemini API :-)"
	msgCmdNotSupported       = "Not a supported bot command: %s"
	msgTypeNotSupported      = "Not a supported message type."
	msgDatabaseNotConfigured = "Database not configured. Set `db_filepath` in your config file."
	msgDatabaseEmpty         = "Database is empty."
	msgHelp                  = `Help message here:

%[3]s : %[4]s
%[5]s : %[6]s
%[7]s : %[8]s

- model: %[1]s
- version: %[2]s
`
	msgPrivacy = `Privacy Policy:

https://github.com/meinside/telegram-gemini-bot/raw/master/PRIVACY.md`

	defaultAnswerTimeoutSeconds   = 180 // 3 minutes
	defaultFetchURLTimeoutSeconds = 10  // 10 seconds

	// for replacing URLs in prompt to body texts
	urlRegexp       = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
	urlToTextFormat = `<link url="%[1]s" content-type="%[2]s">
%[3]s
</link>`
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

	// google ai safety settings threshold
	GoogleAIHarmBlockThreshold *int `json:"google_ai_harm_block_threshold,omitempty"`

	// configurations
	AllowedTelegramUsers    []string `json:"allowed_telegram_users"`
	RequestLogsDBFilepath   string   `json:"db_filepath,omitempty"`
	StreamMessages          bool     `json:"stream_messages,omitempty"`
	AnswerTimeoutSeconds    int      `json:"answer_timeout_seconds,omitempty"`
	ReplaceHTTPURLsInPrompt bool     `json:"replace_http_urls_in_prompt,omitempty"`
	FetchURLTimeoutSeconds  int      `json:"fetch_url_timeout_seconds,omitempty"`
	Verbose                 bool     `json:"verbose,omitempty"`

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

	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	SecretType  string `json:"secret_type"`

	TelegramBotTokenKeyPath string `json:"telegram_bot_token_key_path"`
	GoogleAIAPIKeyKeyPath   string `json:"google_ai_api_key_key_path"`
}

// load config at given path
func loadConfig(fpath string) (conf config, err error) {
	var bytes []byte
	if bytes, err = os.ReadFile(fpath); err == nil {
		if bytes, err = standardizeJSON(bytes); err == nil {
			if err = json.Unmarshal(bytes, &conf); err == nil {
				if (conf.TelegramBotToken == nil || conf.GoogleAIAPIKey == nil) &&
					conf.Infisical != nil {
					// read token and api key from infisical
					client := infisical.NewInfisicalClient(infisical.Config{
						SiteUrl: "https://app.infisical.com",
					})

					_, err = client.Auth().UniversalAuthLogin(conf.Infisical.ClientID, conf.Infisical.ClientSecret)
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
				if conf.GoogleAIHarmBlockThreshold == nil {
					conf.GoogleAIHarmBlockThreshold = ptr(defaultAIHarmBlockThreshold)
				}
				if conf.AnswerTimeoutSeconds <= 0 {
					conf.AnswerTimeoutSeconds = defaultAnswerTimeoutSeconds
				}
				if conf.FetchURLTimeoutSeconds <= 0 {
					conf.FetchURLTimeoutSeconds = defaultFetchURLTimeoutSeconds
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
		if files := client.ListFiles(ctx); files != nil {
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
		} else {
			log.Printf("failed to list files for deletion")
		}

		// database
		var db *Database = nil
		if conf.RequestLogsDBFilepath != "" {
			var err error
			if db, err = openDatabase(conf.RequestLogsDBFilepath); err != nil {
				log.Printf("failed to open request logs db: %s", redact(conf, err))
			}
		}

		// set message handler
		bot.SetMessageHandler(func(b *tg.Bot, update tg.Update, message tg.Message, edited bool) {
			if !isAllowed(update, allowedUsers) {
				log.Printf("message not allowed: %s", userNameFromUpdate(update))
				return
			}

			handleMessages(ctx, b, client, conf, db, []tg.Update{update}, nil)
		})
		bot.SetMediaGroupHandler(func(b *tg.Bot, updates []tg.Update, mediaGroupID string) {
			for _, update := range updates {
				if !isAllowed(update, allowedUsers) {
					log.Printf("message (media group id: %s) not allowed: %s", mediaGroupID, userNameFromUpdate(update))
					return
				}
			}

			handleMessages(ctx, b, client, conf, db, updates, &mediaGroupID)
		})
		bot.SetInlineQueryHandler(func(b *tg.Bot, update tg.Update, inlineQuery tg.InlineQuery) {
			options := tg.OptionsAnswerInlineQuery{}.
				SetIsPersonal(true).
				SetNextOffset("") // no more results

			results := []any{}
			prompts := retrieveSuccessfulPrompts(db, inlineQuery.From.ID)
			if len(prompts) > 0 {
				printer := message.NewPrinter(language.English) // for adding commas to numbers

				for _, prompt := range prompts {
					article, _ := tg.NewInlineQueryResultArticle(
						prompt.Text,
						prompt.Result.Text,
						fmt.Sprintf(
							"Tokens: input %s, output: %s",
							printer.Sprintf("%d", prompt.Tokens),
							printer.Sprintf("%d", prompt.Result.Tokens),
						),
					)

					results = append(results, article)
				}
			} else {
				article, _ := tg.NewInlineQueryResultArticle(
					"No result",
					"There was no successful prompt for this user.",
					"There was no successful prompt for this user.",
				)

				results = append(results, article)
			}

			if result := bot.AnswerInlineQuery(inlineQuery.ID, results, options); !result.Ok {
				log.Printf("failed to answer inline query: %s", *result.Description)
			}
		})

		// set command handlers
		bot.AddCommandHandler(cmdStart, startCommandHandler(conf, allowedUsers))
		bot.AddCommandHandler(cmdStats, statsCommandHandler(conf, db, allowedUsers))
		bot.AddCommandHandler(cmdHelp, helpCommandHandler(conf, allowedUsers))
		bot.AddCommandHandler(cmdPrivacy, privacyCommandHandler(conf))
		bot.SetNoMatchingCommandHandler(noSuchCommandHandler(conf, allowedUsers))

		// set bot commands
		if res := bot.SetMyCommands([]tg.BotCommand{
			{
				Command:     cmdStats,
				Description: descStats,
			},
			{
				Command:     cmdPrivacy,
				Description: descPrivacy,
			},
			{
				Command:     cmdHelp,
				Description: descHelp,
			},
		}, tg.OptionsSetMyCommands{}); !res.Ok {
			log.Printf("failed to set bot commands: %s", *res.Description)
		}

		// poll updates
		bot.StartPollingUpdates(0, intervalSeconds, func(b *tg.Bot, update tg.Update, err error) {
			if err == nil {
				if !isAllowed(update, allowedUsers) {
					log.Printf("user not allowed: %s", userNameFromUpdate(update))
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

// handle allowed message updates from telegram bot api
func handleMessages(ctx context.Context, bot *tg.Bot, client *genai.Client, conf config, db *Database, updates []tg.Update, mediaGroupID *string) {
	if len(updates) <= 0 {
		if mediaGroupID == nil {
			log.Printf("failed to handle messages: no updates given")
		} else {
			log.Printf("failed to handle messages (media group id: '%s'): no updates given", *mediaGroupID)
		}
		return
	}

	update := updates[0]

	// first message + other grouped messages
	var message *tg.Message
	if update.HasMessage() {
		message = update.Message
	} else if update.HasEditedMessage() {
		message = update.EditedMessage
	}
	var otherGroupedMessages []tg.Message
	for _, update := range updates[1:] {
		if update.HasMessage() {
			otherGroupedMessages = append(otherGroupedMessages, *update.Message)
		} else if update.HasEditedMessage() {
			otherGroupedMessages = append(otherGroupedMessages, *update.EditedMessage)
		}
	}

	chatID := message.Chat.ID
	userID := message.From.ID
	messageID := message.MessageID

	var errMessage string
	if msg := usableMessageFromUpdate(update); msg != nil {
		if parent, original, err := chatMessagesFromTGMessage(bot, *msg, otherGroupedMessages...); err == nil {
			if original != nil {
				ctx, cancel := context.WithTimeout(ctx, time.Duration(conf.AnswerTimeoutSeconds)*time.Second)
				defer cancel()

				answer(ctx, bot, client, conf, db, parent, original, chatID, userID, userNameFromUpdate(update), messageID)

				if err = ctx.Err(); err == nil {
					return
				}

				log.Printf("failed to answer in %d seconds: %s", conf.AnswerTimeoutSeconds, redact(conf, err))

				errMessage = fmt.Sprintf("Failed to answer in %d seconds: %s", conf.AnswerTimeoutSeconds, redact(conf, err))
			} else {
				log.Printf("no converted chat messages from update: %+v", update)

				errMessage = "There was no usable chat messages from telegram message."
			}
		} else {
			log.Printf("failed to get chat messages from telegram message: %s", err)

			errMessage = fmt.Sprintf("Failed to get chat messages from telegram message: %s", redact(conf, err))
		}
	} else {
		log.Printf("no usable message from update: %+v", update)

		errMessage = "There was no usable message from update."
	}

	_, _ = sendMessage(bot, conf, errMessage, chatID, &messageID)
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

	// model
	model := client.GenerativeModel(*conf.GoogleGenerativeModel)

	// set system instruction
	var systemInstruction string
	if conf.SystemInstruction == nil {
		systemInstruction = defaultSystemInstruction(conf)
	} else {
		systemInstruction = *conf.SystemInstruction
	}
	model.SystemInstruction = &genai.Content{
		Role: string(chatMessageRoleModel),
		Parts: []genai.Part{
			genai.Text(systemInstruction),
		},
	}

	// set safety filters
	model.SafetySettings = safetySettings(genai.HarmBlockThreshold(*conf.GoogleAIHarmBlockThreshold))

	fileNames := []string{}

	// prompt
	prompt := []genai.Part{}
	if original != nil {
		// text
		text := original.text
		if conf.ReplaceHTTPURLsInPrompt {
			text = replaceHTTPURLsInPromptToBodyTexts(conf, text)
		}
		if conf.Verbose {
			log.Printf("[verbose] prompt text: '%s'", text)
		}
		prompt = append(prompt, genai.Text(text))

		// files
		var mimeType string
		for _, file := range original.files {
			mimeType = stripCharsetFromMimeType(mimetype.Detect(file).String())

			if file, err := client.UploadFile(ctx, "", bytes.NewReader(file), &genai.UploadFileOptions{
				MIMEType: mimeType,
			}); err == nil {
				prompt = append(prompt, genai.FileData{
					MIMEType: file.MIMEType,
					URI:      file.URI,
				})

				fileNames = append(fileNames, file.Name) // FIXME: will wait synchronously for it to become active
			} else {
				log.Printf("failed to upload file(%s) for prompt: %s", mimeType, redact(conf, err))
			}
		}
	}

	// set history
	session := model.StartChat()
	if parent != nil {
		session.History = []*genai.Content{}

		// text
		parts := []genai.Part{
			genai.Text(parent.text),
		}

		// files
		var mimeType string
		for _, file := range parent.files {
			mimeType = stripCharsetFromMimeType(mimetype.Detect(file).String())

			if file, err := client.UploadFile(ctx, "", bytes.NewReader(file), &genai.UploadFileOptions{
				MIMEType: mimeType,
			}); err == nil {
				parts = append(parts, genai.FileData{
					MIMEType: file.MIMEType,
					URI:      file.URI,
				})

				fileNames = append(fileNames, file.Name) // FIXME: will wait synchronously for it to become active
			} else {
				log.Printf("failed to upload file(%s) for history: %s", mimeType, redact(conf, err))
			}
		}

		session.History = append(session.History, &genai.Content{
			Role:  string(chatMessageRoleModel),
			Parts: parts,
		})
	}

	// FIXME: wait for all files to become active
	waitForFiles(ctx, conf, client, fileNames)

	// number of tokens for logging
	var numTokensInput int32 = 0
	var numTokensOutput int32 = 0

	if conf.StreamMessages { // stream message
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
					error := errorString(conf, err)

					log.Printf("failed to iterate stream: %s", error)

					_, _ = sendMessage(bot, conf, fmt.Sprintf("Failed to iterate stream: %s", error), chatID, nil)
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
	} else { // send message synchronously
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
			error := errorString(conf, err)

			log.Printf("failed to generate an answer from Gemini: %s", error)

			_, _ = sendMessage(bot, conf, fmt.Sprintf("Failed to generate an answer from Gemini: %s", error), chatID, &messageID)

			// save to database (error)
			savePromptAndResult(db, chatID, userID, username, messagesToPrompt(parent, original), 0, error, 0, false)
		}
	}
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	return fmt.Sprintf(defaultSystemInstructionFormat,
		*conf.GoogleGenerativeModel,
		time.Now().Format("2006-01-02 15:04:05 (Mon)"),
	)
}
