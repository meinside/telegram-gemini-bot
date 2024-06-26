// helpers.go
//
// helper functions

package main

import (
	"errors"
	"fmt"
	"strings"

	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"
	"github.com/tailscale/hujson"
	"google.golang.org/api/googleapi"
)

const (
	redactedString = "<REDACTED>"
)

// redact given error for logging and/or messaing
func redact(conf config, err error) (redacted string) {
	redacted = err.Error()

	if strings.Contains(redacted, *conf.GoogleAIAPIKey) {
		redacted = strings.ReplaceAll(redacted, *conf.GoogleAIAPIKey, redactedString)
	}
	if strings.Contains(redacted, *conf.TelegramBotToken) {
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
func chatMessagesFromTGMessage(bot *tg.Bot, message tg.Message, otherGroupedMessages ...tg.Message) (parent, original *chatMessage, err error) {
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
	if chatMessage, err := convertMessage(bot, message, otherGroupedMessages...); err == nil {
		original = chatMessage
	} else {
		errs = append(errs, err)
	}

	return parent, original, errors.Join(errs...)
}

// generate a help message with version info
func helpMessage(conf config) string {
	return fmt.Sprintf(msgHelp,
		*conf.GoogleGenerativeModel,
		version.Build(version.OS|version.Architecture|version.Revision),
	)
}

// convert error to string
func errorString(conf config, err error) (error string) {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		error = gerror(conf, gerr)
	} else {
		error = redact(conf, err)
	}

	return error
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
