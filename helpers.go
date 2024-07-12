// helpers.go
//
// helper functions

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	// google ai
	"google.golang.org/api/googleapi"

	// my libraries
	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"

	// others
	"github.com/PuerkitoBio/goquery"
	"github.com/tailscale/hujson"
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
	} else if update.HasInlineQuery() && update.InlineQuery.From.Username != nil {
		username = *update.InlineQuery.From.Username
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
		cmdStats, descStats,
		cmdPrivacy, descPrivacy,
		cmdHelp, descHelp,
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

// strip trailing charset text from given mime type
func stripCharsetFromMimeType(mimeType string) string {
	splitted := strings.Split(mimeType, ";")
	return splitted[0]
}

// replace all http urls in given text to body texts
func replaceHTTPURLsInPromptToBodyTexts(conf config, prompt string) string {
	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if converted, err := urlToText(conf, url); err == nil {
			prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", converted), 1)
		}
	}

	return prompt
}

// fetch the content from given url and convert it to text for prompting.
func urlToText(conf config, url string) (body string, err error) {
	client := &http.Client{
		Timeout: time.Duration(conf.FetchURLTimeoutSeconds) * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch contents from url: %s", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	if resp.StatusCode == 200 {
		if strings.HasPrefix(contentType, "text/html") {
			var doc *goquery.Document
			if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
				_ = doc.Find("script").Remove() // FIXME: remove unwanted javascripts

				body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text()))
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this HTML document.")
				err = fmt.Errorf("failed to read html document from %s: %s", url, err)
			}
		} else if strings.HasPrefix(contentType, "text/") {
			var bytes []byte
			if bytes, err = io.ReadAll(resp.Body); err == nil {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document.")
				err = fmt.Errorf("failed to read %s document from %s: %s", contentType, url, err)
			}
		} else {
			body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type: %s not supported.", contentType))
			err = fmt.Errorf("content type %s not supported for url: %s", contentType, url)
		}
	} else {
		body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode))
		err = fmt.Errorf("http error %d from url: %s", resp.StatusCode, url)
	}

	return body, err
}

// remove consecutive empty lines for compacting prompt lines
func removeConsecutiveEmptyLines(input string) string {
	// trim each line
	trimmed := []string{}
	for _, line := range strings.Split(input, "\n") {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	input = strings.Join(trimmed, "\n")

	// remove redundant empty lines
	regex := regexp.MustCompile("\n{2,}")
	return regex.ReplaceAllString(input, "\n")
}
