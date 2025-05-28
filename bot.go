// bot.go

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"

	// others
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// constants for default values
const (
	defaultGenerativeModel                    = `gemini-2.0-flash`
	defaultGenerativeModelForImageGeneration  = `gemini-2.0-flash-preview-image-generation`
	defaultGenerativeModelForSpeechGeneration = `gemini-2.5-flash-preview-tts`

	defaultAIHarmBlockThreshold genai.HarmBlockThreshold = genai.HarmBlockThresholdBlockOnlyHigh

	// https://ai.google.dev/gemini-api/docs/speech-generation#voices
	defaultGenerativeModelForSpeechGenerationVoice = `Kore`

	defaultSystemInstructionFormat = `You are a Telegram bot which uses Google Gemini API.

Current datetime is %[1]s.

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

	cmdStart = "/start"

	// general commands
	cmdStats    = "/stats"
	descStats   = `show stats of this bot.`
	cmdPrivacy  = "/privacy"
	descPrivacy = `show privacy policy of this bot.`
	cmdHelp     = "/help"
	descHelp    = `show help message.`

	// commands for various types of generations
	cmdGenerateImage             = "/image"
	descGenerateImage            = `generate image(s) with the given prompt.`
	cmdGenerateSpeech            = "/speech"
	descGenerateSpeech           = `generate a speech with the given prompt.`
	cmdGenerateWithGoogleSearch  = "/google"
	descGenerateWithGoogleSearch = `generate text with the given prompt and google search.`

	msgStart                 = `This bot will answer your messages with Gemini API :-)`
	msgCmdNotSupported       = `Not a supported bot command: %s`
	msgTypeNotSupported      = `Not a supported message type.`
	msgDatabaseNotConfigured = `Database not configured. Set 'db_filepath' in your config file.`
	msgDatabaseEmpty         = `Database is empty.`
	msgHelp                  = `Help message here:

%[5]s : %[6]s
%[7]s : %[8]s
%[9]s : %[10]s
%[11]s : %[12]s
%[13]s : %[14]s
%[15]s : %[16]s

- configured models:
  * text: %[1]s
  * image: %[2]s
  * speech: %[3]s
- version: %[4]s
`
	msgPrivacy = `Privacy Policy:

https://github.com/meinside/telegram-gemini-bot/raw/master/PRIVACY.md`
	msgPromptNotGiven = `Prompt was not given.`

	defaultAnswerTimeoutSeconds   = 180 // 3 minutes
	defaultFetchURLTimeoutSeconds = 10  // 10 seconds

	// for replacing URLs in prompt to body texts
	urlRegexp       = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
	urlToTextFormat = `<link url="%[1]s" content-type="%[2]s">
%[3]s
</link>`
)

const (
	wavBitDepth    = 16
	wavNumChannels = 1
)

type chatMessage struct {
	role  genai.Role
	text  string
	files [][]byte
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

	// gemini-things client for text generation
	gtc, err := gt.NewClient(
		*conf.GoogleAIAPIKey,
		gt.WithModel(*conf.GoogleGenerativeModel),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for text generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtc.SetTimeoutSeconds(conf.AnswerTimeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		if conf.SystemInstruction == nil {
			return defaultSystemInstruction(conf)
		} else {
			return *conf.SystemInstruction
		}
	})
	defer gtc.Close()

	// gemini-things client for image generation
	gtcImg, err := gt.NewClient(
		*conf.GoogleAIAPIKey,
		gt.WithModel(*conf.GoogleGenerativeModelForImageGeneration),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for image generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtcImg.SetTimeoutSeconds(conf.AnswerTimeoutSeconds)
	gtcImg.SetSystemInstructionFunc(nil)
	defer gtcImg.Close()

	// gemini-things client for speech generation
	gtcSpeech, err := gt.NewClient(
		*conf.GoogleAIAPIKey,
		gt.WithModel(*conf.GoogleGenerativeModelForSpeechGeneration),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for speech generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtcSpeech.SetTimeoutSeconds(conf.AnswerTimeoutSeconds)
	gtcSpeech.SetSystemInstructionFunc(nil)
	defer gtcSpeech.Close()

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
		bot.SetMessageHandler(func(
			b *tg.Bot,
			update tg.Update,
			message tg.Message,
			edited bool,
		) {
			if !isAllowed(update, allowedUsers) {
				log.Printf("message not allowed: %s", userNameFromUpdate(update))
				return
			}

			handleMessages(
				ctx,
				b,
				conf,
				db,
				gtc,
				[]tg.Update{update},
				nil,
				false,
			)
		})
		bot.SetMediaGroupHandler(func(
			b *tg.Bot,
			updates []tg.Update,
			mediaGroupID string,
		) {
			for _, update := range updates {
				if !isAllowed(update, allowedUsers) {
					log.Printf("message (media group id: %s) not allowed: %s", mediaGroupID, userNameFromUpdate(update))
					return
				}
			}

			handleMessages(
				ctx,
				b,
				conf,
				db,
				gtc,
				updates,
				&mediaGroupID,
				false,
			)
		})
		bot.SetInlineQueryHandler(func(
			b *tg.Bot,
			update tg.Update,
			inlineQuery tg.InlineQuery,
		) {
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

			if result := bot.AnswerInlineQuery(
				inlineQuery.ID,
				results,
				options,
			); !result.Ok {
				log.Printf("failed to answer inline query: %s", *result.Description)
			}
		})

		// set general command handlers
		bot.AddCommandHandler(cmdStart, startCommandHandler(conf, allowedUsers))
		bot.AddCommandHandler(cmdStats, statsCommandHandler(conf, db, allowedUsers))
		bot.AddCommandHandler(cmdHelp, helpCommandHandler(conf, allowedUsers))
		bot.AddCommandHandler(cmdPrivacy, privacyCommandHandler(conf))

		// set generation commands' handlers
		bot.AddCommandHandler(cmdGenerateImage, genImageCommandHandler(ctx, conf, db, gtcImg, allowedUsers))
		bot.AddCommandHandler(cmdGenerateSpeech, genSpeechCommandHandler(ctx, conf, db, gtcSpeech, allowedUsers))
		bot.AddCommandHandler(cmdGenerateWithGoogleSearch, genWithGoogleSearchCommandHandler(ctx, conf, db, gtc, allowedUsers))
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
		bot.StartPollingUpdates(
			0,
			intervalSeconds,
			func(b *tg.Bot, update tg.Update, err error) {
				if err == nil {
					if !isAllowed(update, allowedUsers) {
						log.Printf("user not allowed: %s", userNameFromUpdate(update))
						return
					}

					// type not supported
					message := usableMessageFromUpdate(update)
					if message != nil {
						_, _ = sendMessage(
							b,
							conf,
							msgTypeNotSupported,
							message.Chat.ID,
							&message.MessageID,
						)
					}
				} else {
					log.Printf("failed to poll updates: %s", redactError(conf, err))
				}
			},
		)
	} else {
		log.Printf("failed to get bot info: %s", *b.Description)
	}
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	return fmt.Sprintf(defaultSystemInstructionFormat,
		time.Now().Format("2006-01-02 15:04:05 MST (Mon)"),
	)
}
