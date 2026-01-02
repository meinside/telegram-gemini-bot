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
	defaultGenerativeModel                    = `gemini-3-flash-preview`
	defaultGenerativeModelForImageGeneration  = `gemini-3-pro-image-preview`
	defaultGenerativeModelForVideoGeneration  = `veo-3.1-fast-generate-preview`
	defaultGenerativeModelForSpeechGeneration = `gemini-2.5-flash-preview-tts`

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

	requestTimeoutSeconds          = 30
	longRequestTimeoutSeconds      = 180 // 3 minutes
	ignorableRequestTimeoutSeconds = 3

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
	cmdGenerateVideo             = "/video"
	descGenerateVideo            = `generate video(s) with the given prompt.`
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

%[6]s : %[7]s
%[8]s : %[9]s
%[10]s : %[11]s
%[12]s : %[13]s
%[14]s : %[15]s
%[16]s : %[17]s
%[18]s : %[19]s

- configured models:
  * text: %[1]s
  * image: %[2]s
  * video: %[3]s
  * speech: %[4]s
- version: %[5]s
`
	msgPrivacy = `Privacy Policy:

https://github.com/meinside/telegram-gemini-bot/raw/master/PRIVACY.md`
	msgPromptNotGiven = `Prompt was not given.`

	// for replacing URLs in prompt to body texts
	urlRegexp = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
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
	ctx := context.Background()

	token := conf.TelegramBotToken

	allowedUsers := map[string]bool{}
	for _, user := range conf.AllowedTelegramUsers {
		allowedUsers[user] = true
	}

	// telegram bot client
	bot := tg.NewClient(*token)

	// gemini-things client for text generation
	gtc, err := gtClient(
		ctx,
		conf,
		gt.WithModel(*conf.GoogleGenerativeModel),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for text generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtc.SetSystemInstructionFunc(func() string {
		if conf.SystemInstruction == nil {
			return defaultSystemInstruction()
		} else {
			return *conf.SystemInstruction
		}
	})
	defer func() { _ = gtc.Close() }()

	// gemini-things client for image generation
	gtcImg, err := gtClient(
		ctx,
		conf,
		gt.WithModel(*conf.GoogleGenerativeModelForImageGeneration),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for image generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtcImg.SetSystemInstructionFunc(nil)
	defer func() { _ = gtcImg.Close() }()

	// gemini-things client for video generation
	gtcVideo, err := gtClient(
		ctx,
		conf,
		gt.WithModel(*conf.GoogleGenerativeModelForVideoGeneration),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for video generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtcVideo.SetSystemInstructionFunc(nil)
	defer func() { _ = gtcVideo.Close() }()

	// gemini-things client for speech generation
	gtcSpeech, err := gtClient(
		ctx,
		conf,
		gt.WithModel(*conf.GoogleGenerativeModelForSpeechGeneration),
	)
	if err != nil {
		log.Printf("error initializing gemini-things client for speech generation: %s", redactError(conf, err))

		os.Exit(1)
	}
	gtcSpeech.SetSystemInstructionFunc(nil)
	defer func() { _ = gtcSpeech.Close() }()

	// context for background tasks
	ctxBg := context.Background()

	// delete webhook before polling updates
	ctxDelete, cancelDelete := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
	defer cancelDelete()
	_ = bot.DeleteWebhook(ctxDelete, false)

	// get bot info
	ctxInfo, cancelInfo := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
	defer cancelInfo()
	if b := bot.GetMe(ctxInfo); b.Ok {
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
				ctxBg,
				b,
				conf,
				db,
				gtc,
				[]tg.Update{update},
				nil,
				false,
			)
		})
		// set media group handler
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
				ctxBg,
				b,
				conf,
				db,
				gtc,
				updates,
				&mediaGroupID,
				false,
			)
		})
		// set inline query handler
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

			// answer inline query
			ctxAnswer, cancelAnswer := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
			defer cancelAnswer()
			if result := bot.AnswerInlineQuery(
				ctxAnswer,
				inlineQuery.ID,
				results,
				options,
			); !result.Ok {
				log.Printf("failed to answer inline query: %s", *result.Description)
			}
		})

		// set general command handlers
		bot.AddCommandHandler(cmdStart, startCommandHandler(ctxBg, conf, allowedUsers))
		bot.AddCommandHandler(cmdStats, statsCommandHandler(ctxBg, conf, db, allowedUsers))
		bot.AddCommandHandler(cmdHelp, helpCommandHandler(ctxBg, conf, allowedUsers))
		bot.AddCommandHandler(cmdPrivacy, privacyCommandHandler(ctxBg, conf))

		// set generation commands' handlers
		bot.AddCommandHandler(cmdGenerateImage, genImageCommandHandler(ctxBg, conf, db, gtcImg, allowedUsers))
		bot.AddCommandHandler(cmdGenerateVideo, genVideoCommandHandler(ctxBg, conf, db, gtcVideo, allowedUsers))
		bot.AddCommandHandler(cmdGenerateSpeech, genSpeechCommandHandler(ctxBg, conf, db, gtcSpeech, allowedUsers))
		bot.AddCommandHandler(cmdGenerateWithGoogleSearch, genWithGoogleSearchCommandHandler(ctxBg, conf, db, gtc, allowedUsers))
		bot.SetNoMatchingCommandHandler(noSuchCommandHandler(ctxBg, conf, allowedUsers))

		// set bot commands
		ctxCommands, cancelCommands := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
		defer cancelCommands()
		if res := bot.SetMyCommands(
			ctxCommands,
			[]tg.BotCommand{
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
			},
			tg.OptionsSetMyCommands{},
		); !res.Ok {
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
							ctxBg,
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
func defaultSystemInstruction() string {
	return fmt.Sprintf(defaultSystemInstructionFormat,
		time.Now().Format("2006-01-02 15:04:05 MST (Mon)"),
	)
}
