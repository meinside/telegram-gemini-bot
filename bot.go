// bot.go

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"time"

	// google ai
	"google.golang.org/genai"

	// infisical
	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"

	// others
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// constants for default values
const (
	defaultGenerativeModel                               = "gemini-2.0-flash"
	defaultAIHarmBlockThreshold genai.HarmBlockThreshold = genai.HarmBlockThresholdBlockOnlyHigh

	defaultSystemInstructionFormat = `You are a Telegram bot which uses Google Gemini API(model: %[1]s).

Current datetime is %[2]s.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Unless otherwise specified, respond in the same language as used in the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
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

type chatMessage struct {
	role  genai.Role
	text  string
	files [][]byte
}

// config struct for loading a configuration file
type config struct {
	SystemInstruction *string `json:"system_instruction,omitempty"`

	GoogleGenerativeModel *string `json:"google_generative_model,omitempty"`

	// google ai safety settings threshold
	GoogleAIHarmBlockThreshold *genai.HarmBlockThreshold `json:"google_ai_harm_block_threshold,omitempty"`

	// configurations
	AllowedTelegramUsers    []string `json:"allowed_telegram_users"`
	RequestLogsDBFilepath   string   `json:"db_filepath,omitempty"`
	AnswerTimeoutSeconds    int      `json:"answer_timeout_seconds,omitempty"`
	ReplaceHTTPURLsInPrompt bool     `json:"replace_http_urls_in_prompt,omitempty"`
	FetchURLTimeoutSeconds  int      `json:"fetch_url_timeout_seconds,omitempty"`
	ForceUseGoogleSearch    bool     `json:"force_use_google_search,omitempty"`
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
					client := infisical.NewInfisicalClient(context.TODO(), infisical.Config{
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

	allowedUsers := map[string]bool{}
	for _, user := range conf.AllowedTelegramUsers {
		allowedUsers[user] = true
	}

	// telegram bot client
	bot := tg.NewClient(*token)

	// gemini-things client
	gtc, err := gt.NewClient(*conf.GoogleAIAPIKey, *conf.GoogleGenerativeModel)
	if err != nil {
		log.Printf("error initializing gemini-things client: %s", redactError(conf, err))

		os.Exit(1)
	}
	defer gtc.Close()
	gtc.SetTimeout(conf.AnswerTimeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		if conf.SystemInstruction == nil {
			return defaultSystemInstruction(conf)
		} else {
			return *conf.SystemInstruction
		}
	})

	ctx := context.Background()

	_ = bot.DeleteWebhook(false) // delete webhook before polling updates
	if b := bot.GetMe(); b.Ok {
		log.Printf("launching bot: %s", userName(b.Result))

		// database
		var db *Database = nil
		if conf.RequestLogsDBFilepath != "" {
			var err error
			if db, err = openDatabase(conf.RequestLogsDBFilepath); err != nil {
				log.Printf("failed to open request logs db: %s", redactError(conf, err))
			}
		}

		// set message handler
		bot.SetMessageHandler(func(b *tg.Bot, update tg.Update, message tg.Message, edited bool) {
			if !isAllowed(update, allowedUsers) {
				log.Printf("message not allowed: %s", userNameFromUpdate(update))
				return
			}

			handleMessages(ctx, b, conf, db, gtc, []tg.Update{update}, nil)
		})
		bot.SetMediaGroupHandler(func(b *tg.Bot, updates []tg.Update, mediaGroupID string) {
			for _, update := range updates {
				if !isAllowed(update, allowedUsers) {
					log.Printf("message (media group id: %s) not allowed: %s", mediaGroupID, userNameFromUpdate(update))
					return
				}
			}

			handleMessages(ctx, b, conf, db, gtc, updates, &mediaGroupID)
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
				log.Printf("failed to poll updates: %s", redactError(conf, err))
			}
		})
	} else {
		log.Printf("failed to get bot info: %s", *b.Description)
	}
}

// handle allowed message updates from telegram bot api
func handleMessages(ctx context.Context, bot *tg.Bot, conf config, db *Database, gtc *gt.Client, updates []tg.Update, mediaGroupID *string) {
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

				answer(ctx, bot, conf, db, gtc, parent, original, chatID, userID, userNameFromUpdate(update), messageID)

				if err = ctx.Err(); err == nil {
					return
				}

				log.Printf("failed to answer in %d seconds: %s", conf.AnswerTimeoutSeconds, redactError(conf, err))

				errMessage = fmt.Sprintf("Failed to answer in %d seconds: %s", conf.AnswerTimeoutSeconds, redactError(conf, err))
			} else {
				log.Printf("no converted chat messages from update: %+v", update)

				errMessage = "There was no usable chat messages from telegram message."
			}
		} else {
			log.Printf("failed to get chat messages from telegram message: %s", err)

			errMessage = fmt.Sprintf("Failed to get chat messages from telegram message: %s", redactError(conf, err))
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
func answer(ctx context.Context, bot *tg.Bot, conf config, db *Database, gtc *gt.Client, parent, original *chatMessage, chatID, userID int64, username string, messageID int64) {
	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(chatID, messageID, tg.NewMessageReactionWithEmoji("👌"))

	opts := &gt.GenerationOptions{
		HarmBlockThreshold: conf.GoogleAIHarmBlockThreshold,
	}
	if conf.ForceUseGoogleSearch {
		opts.Tools = []*genai.Tool{
			{
				GoogleSearch: &genai.GoogleSearch{},
			},
		}
	}

	// prompt
	var promptText string
	promptFiles := map[string]io.Reader{}

	if original != nil {
		// text
		promptText = original.text
		promptFilesFromURL := [][]byte{}
		if conf.ReplaceHTTPURLsInPrompt {
			promptText, promptFilesFromURL = convertPromptWithURLs(conf, promptText)
		}

		// files
		for i, file := range promptFilesFromURL {
			promptFiles[fmt.Sprintf("url %d", i+1)] = bytes.NewReader(file)
		}
		for i, file := range original.files {
			promptFiles[fmt.Sprintf("file %d", i+1)] = bytes.NewReader(file)
		}

		if conf.Verbose {
			log.Printf("[verbose] will process prompt text '%s' with %d files", promptText, len(promptFiles))
		}
	}

	// histories (parent message)
	if parent != nil {
		// text of parent message
		parentText := parent.text
		parts := []*genai.Part{
			genai.NewPartFromText(parentText),
		}

		// files of parent message
		parentFiles := map[string]io.Reader{}
		for i, file := range parent.files {
			parentFiles[fmt.Sprintf("file %d", i+1)] = bytes.NewReader(file)
		}

		// upload files and wait
		parentFilesToUpload := []gt.Prompt{}
		for filename, file := range parentFiles {
			parentFilesToUpload = append(parentFilesToUpload, gt.PromptFromFile(filename, file))
		}
		if uploaded, err := gtc.UploadFilesAndWait(ctx, parentFilesToUpload); err == nil {
			for _, upload := range uploaded {
				parts = append(parts, ptr(upload.ToPart()))
			}
		}

		// set history for generation options
		opts.History = []genai.Content{
			{
				Role:  string(gt.RoleModel),
				Parts: parts,
			},
		}
	}

	// number of tokens for logging
	var numTokensInput int32 = 0
	var numTokensOutput int32 = 0

	prompts := []gt.Prompt{gt.PromptFromText(promptText)}
	for filename, file := range promptFiles {
		prompts = append(prompts, gt.PromptFromFile(filename, file))
	}

	// generate
	var firstMessageID *int64 = nil
	mergedText := ""
	if err := gtc.GenerateStreamed(
		ctx,
		prompts,
		func(data gt.StreamCallbackData) {
			if conf.Verbose {
				log.Printf("[verbose] streaming answer to chat(%d): %+v", chatID, data)
			}

			// check finish reason
			if data.FinishReason != nil && *data.FinishReason != genai.FinishReasonStop {
				generatedText := fmt.Sprintf("<<<%s>>>", *data.FinishReason)
				mergedText += generatedText

				if firstMessageID == nil { // send the first message
					if sentMessageID, err := sendMessage(bot, conf, generatedText, chatID, &messageID); err == nil {
						firstMessageID = &sentMessageID
					} else {
						log.Printf("failed to send stream messages [%+v + %+v] with '%+v': %s", parent, original, data, redactError(conf, err))
					}
				} else { // update the first message
					// update the first message (append text)
					if err := updateMessage(bot, conf, mergedText, chatID, *firstMessageID); err != nil {
						log.Printf("failed to update stream messages [%+v + %+v] with '%+v': %s", parent, original, data, redactError(conf, err))
					}
				}
			}

			// check stream content
			if data.TextDelta != nil {
				generatedText := *data.TextDelta
				mergedText += generatedText

				if firstMessageID == nil { // send the first message
					if sentMessageID, err := sendMessage(bot, conf, generatedText, chatID, &messageID); err == nil {
						firstMessageID = &sentMessageID
					} else {
						log.Printf("failed to send stream messages [%+v + %+v] with '%+v': %s", parent, original, data, redactError(conf, err))
					}
				} else { // update the first message
					// update the first message (append text)
					if err := updateMessage(bot, conf, mergedText, chatID, *firstMessageID); err != nil {
						log.Printf("failed to update stream messages [%+v + %+v] with '%+v': %s", parent, original, data, redactError(conf, err))
					}
				}
			} else if data.Error != nil {
				error := redactError(conf, data.Error)

				log.Printf("error from stream: %s", error)

				_, _ = sendMessage(bot, conf, fmt.Sprintf("Failed to iterate stream: %s", error), chatID, nil)
			}

			// check tokens
			if data.NumTokens != nil {
				if numTokensInput < data.NumTokens.Input {
					numTokensInput = data.NumTokens.Input
				}
				if numTokensOutput < data.NumTokens.Output {
					numTokensOutput = data.NumTokens.Output
				}
			}
		},
		opts,
	); err == nil {
		if conf.Verbose {
			log.Printf("[verbose] streaming [%+v + %+v] ...", parent, original)
		}
	} else {
		error := redactError(conf, err)

		log.Printf("failed to generate stream: %s", error)

		_, _ = sendMessage(bot, conf, fmt.Sprintf("Generation failed: %s", error), chatID, nil)
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
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	return fmt.Sprintf(defaultSystemInstructionFormat,
		*conf.GoogleGenerativeModel,
		time.Now().Format("2006-01-02 15:04:05 MST (Mon)"),
	)
}
