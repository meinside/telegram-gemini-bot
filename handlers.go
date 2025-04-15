// handlers.go
//
// bot handler functions

package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"
)

const (
	defaultPromptForMedias = "Describe provided media(s)."

	readURLContentTimeoutSeconds               = 60  // 1 minute
	uploadedFileStateCheckIntervalMilliseconds = 300 // 300 milliseconds
)

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

// return a /privacy command handler
func privacyCommandHandler(conf config) func(b *tg.Bot, update tg.Update, args string) {
	return func(b *tg.Bot, update tg.Update, _ string) {
		message := usableMessageFromUpdate(update)
		if message == nil {
			log.Printf("no usable message from update.")
			return
		}

		chatID := message.Chat.ID
		messageID := message.MessageID

		_, _ = sendMessage(b, conf, msgPrivacy, chatID, &messageID)
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
// (if it was sent from bot, make it an assistant's message)
func convertMessage(bot *tg.Bot, message tg.Message, otherGroupedMessages ...tg.Message) (cm *chatMessage, err error) {
	var role genai.Role
	if message.IsBot() {
		role = gt.RoleModel
	} else {
		role = gt.RoleUser
	}

	if message.HasText() {
		return &chatMessage{
			role: role,
			text: *message.Text,
		}, nil
	} else if message.HasPhoto() || message.HasVideo() || message.HasVideoNote() || message.HasAudio() || message.HasVoice() || message.HasDocument() {
		var text string
		if message.HasCaption() {
			text = *message.Caption
		} else {
			text = defaultPromptForMedias
		}

		allMessages := append([]tg.Message{message}, otherGroupedMessages...)

		allFiles := [][]byte{}
		for _, msg := range allMessages {
			if files, err := filesFromMessage(bot, msg); err == nil {
				allFiles = append(allFiles, files...)
			} else {
				return nil, err
			}
		}

		return &chatMessage{
			role:  role,
			text:  text,
			files: allFiles,
		}, nil
	} else {
		err = fmt.Errorf("failed to convert message: not a supported type")
	}

	return nil, err
}

// extract file bytes from given message
func filesFromMessage(bot *tg.Bot, message tg.Message) (files [][]byte, err error) {
	var bytes []byte
	if message.HasPhoto() {
		files = [][]byte{}

		for _, photo := range message.Photo {
			if bytes, err = readMedia(bot, "photo", photo.FileID); err == nil {
				files = append(files, bytes)
			} else {
				err = fmt.Errorf("failed to read photo content: %s", err)
				break
			}
		}

		if err == nil {
			return files, nil
		}
	} else if message.HasVideo() {
		if bytes, err = readMedia(bot, "video", message.Video.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read video content: %s", err)
		}
	} else if message.HasVideoNote() {
		if bytes, err = readMedia(bot, "video note", message.VideoNote.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read video note content: %s", err)
		}
	} else if message.HasAudio() {
		if bytes, err = readMedia(bot, "audio", message.Audio.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read audio content: %s", err)
		}
	} else if message.HasVoice() {
		if bytes, err = readMedia(bot, "voice", message.Voice.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read voice content: %s", err)
		}
	} else if message.HasDocument() {
		if bytes, err = readMedia(bot, "document", message.Document.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read document content: %s", err)
		}
	}

	return nil, err
}

// read bytes from given media
func readMedia(bot *tg.Bot, mediaType, fileID string) (result []byte, err error) {
	if res := bot.GetFile(fileID); !res.Ok {
		err = fmt.Errorf("failed to read bytes from %s: %s", mediaType, *res.Description)
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
