// handlers.go
//
// bot handler functions

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"
)

// return a /start command handler
func startCommandHandler(
	ctxBg context.Context,
	conf config,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
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

		ctxSend, cancelSend := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
		defer cancelSend()
		_, _ = sendMessage(ctxSend, b, conf, msgStart, chatID, nil)
	}
}

// return a /stats command handler
func statsCommandHandler(
	ctxBg context.Context,
	conf config,
	db *Database,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
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

		_, _ = sendMessage(ctxBg, b, conf, retrieveStats(db), chatID, &messageID)
	}
}

// return a /help command handler
func helpCommandHandler(
	ctxBg context.Context,
	conf config,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
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

		_, _ = sendMessage(ctxBg, b, conf, helpMessage(conf), chatID, &messageID)
	}
}

// return a /privacy command handler
func privacyCommandHandler(
	ctxBg context.Context,
	conf config,
) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, _ string) {
		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		_, _ = sendMessage(ctxBg, b, conf, msgPrivacy, chatID, &messageID)
	}
}

// return a /image command handler
func genImageCommandHandler(
	ctxBg context.Context,
	conf config,
	db *Database,
	gtc *gt.Client,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, args string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("message not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		userID := message.From.ID
		messageID := message.MessageID
		username := userNameFromUpdate(update)

		// handle empty `args`
		if len(args) <= 0 {
			if _, err := sendMessage(
				ctxBg,
				b,
				conf,
				msgPromptNotGiven,
				chatID,
				&messageID,
			); err != nil {
				log.Printf("failed to send error message: %s", redactError(conf, err))
			}
			return
		}

		if parent, original, err := chatMessagesFromTGMessage(ctxBg, b, *message); err == nil {
			if err := answerWithImage(
				ctxBg,
				b,
				conf,
				db,
				gtc,
				parent,
				original,
				chatID,
				userID,
				username,
				messageID,
			); err != nil {
				log.Printf("failed to answer with image: %s", redactError(conf, err))
			}
		} else {
			log.Printf("failed to get chat message from telegram message: %s", redactError(conf, err))
		}
	}
}

// return a /speech command handler
func genSpeechCommandHandler(
	ctxBg context.Context,
	conf config,
	db *Database,
	gtc *gt.Client,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, args string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("message not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		userID := message.From.ID
		messageID := message.MessageID
		username := userNameFromUpdate(update)

		// handle empty `args`
		if len(args) <= 0 {
			if _, err := sendMessage(
				ctxBg,
				b,
				conf,
				msgPromptNotGiven,
				chatID,
				&messageID,
			); err != nil {
				log.Printf("failed to send error message: %s", redactError(conf, err))
			}
			return
		}

		if parent, original, err := chatMessagesFromTGMessage(ctxBg, b, *message); err == nil {
			if err := answerWithVoice(
				ctxBg,
				b,
				conf,
				db,
				gtc,
				parent,
				original,
				chatID,
				userID,
				username,
				messageID,
			); err != nil {
				log.Printf("failed to answer with voice: %s", redactError(conf, err))
			}
		} else {
			log.Printf("failed to answer with speech: %s", redactError(conf, err))
		}
	}
}

// return a /google command handler
func genWithGoogleSearchCommandHandler(
	ctxBg context.Context,
	conf config,
	db *Database,
	gtc *gt.Client,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, args string) {
		if !isAllowed(update, allowedUsers) {
			log.Printf("message not allowed: %s", userNameFromUpdate(update))
			return
		}

		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		// handle empty `args`
		if len(args) <= 0 {
			if _, err := sendMessage(
				ctxBg,
				b,
				conf,
				msgPromptNotGiven,
				chatID,
				&messageID,
			); err != nil {
				log.Printf("failed to send error message: %s", redactError(conf, err))
			}
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
			true,
		)
	}
}

// return a 'no such command' handler
func noSuchCommandHandler(
	ctxBg context.Context,
	conf config,
	allowedUsers map[string]bool,
) func(b *tg.Bot, update tg.Update, cmd, args string) {
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

		_, _ = sendMessage(ctxBg, b, conf, fmt.Sprintf(msgCmdNotSupported, cmd), chatID, &messageID)
	}
}
