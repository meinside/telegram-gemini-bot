// helpers.go
//
// helper functions

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"

	// others
	"github.com/tailscale/hujson"
)

const (
	redactedString = `<REDACTED>`

	defaultPromptForMedias = `Describe provided media(s).`
)

// create a gemini-things client with given config
func gtClient(
	ctx context.Context,
	cfg config,
	opts ...gt.ClientOption,
) (*gt.Client, error) {
	if cfg.GoogleAIAPIKey != nil {
		return gt.NewClient(
			*cfg.GoogleAIAPIKey,
			opts...,
		)
	} else if cfg.GoogleCredentialsFilepath != nil {
		bytes, err := os.ReadFile(*cfg.GoogleCredentialsFilepath)
		if err != nil {
			return nil, err
		}
		return gt.NewVertexClient(
			ctx,
			bytes,
			*cfg.Location,
			opts...,
		)
	}
	return nil, fmt.Errorf("no google ai api key or credentials file provided")
}

// redactError given error for logging and/or messaing
func redactError(
	conf config,
	err error,
) (redacted string) {
	redacted = gt.ErrToStr(err)

	if conf.GoogleAIAPIKey != nil {
		if strings.Contains(redacted, *conf.GoogleAIAPIKey) {
			redacted = strings.ReplaceAll(redacted, *conf.GoogleAIAPIKey, redactedString)
		}
	}
	if strings.Contains(redacted, *conf.TelegramBotToken) {
		redacted = strings.ReplaceAll(redacted, *conf.TelegramBotToken, redactedString)
	}

	return redacted
}

// checks if given update is allowed or not
func isAllowed(
	update tg.Update,
	allowedUsers map[string]bool,
) bool {
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
func chatMessagesFromTGMessage(
	ctxBg context.Context,
	bot *tg.Bot,
	message tg.Message,
	otherGroupedMessages ...tg.Message,
) (parent, original *chatMessage, err error) {
	replyTo := repliedToMessage(message)
	errs := []error{}

	// chat message 1 (parent message)
	if replyTo != nil {
		if chatMessage, err := convertMessage(
			ctxBg,
			bot,
			*replyTo,
		); err == nil {
			parent = chatMessage
		} else {
			errs = append(errs, err)
		}
	}

	// chat message 2 (original message)
	if chatMessage, err := convertMessage(
		ctxBg,
		bot,
		message,
		otherGroupedMessages...,
	); err == nil {
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
		*conf.GoogleGenerativeModelForImageGeneration,
		*conf.GoogleGenerativeModelForVideoGeneration,
		*conf.GoogleGenerativeModelForSpeechGeneration,
		version.Build(version.OS|version.Architecture|version.Revision),
		cmdGenerateImage+" <prompt>", descGenerateImage,
		cmdGenerateVideo+" <prompt>", descGenerateVideo,
		cmdGenerateSpeech+" <prompt>", descGenerateSpeech,
		cmdGenerateWithGoogleSearch+" <prompt>", descGenerateWithGoogleSearch,
		cmdStats, descStats,
		cmdPrivacy, descPrivacy,
		cmdHelp, descHelp,
	)
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

// convert given prompt with http urls into usable prompts
func convertPromptWithURLs(
	prompt string,
) (converted []gt.Prompt) {
	converted = []gt.Prompt{}
	remaining := prompt

	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if before, after, found := strings.Cut(remaining, url); found {
			if isURLFromYoutube(url) { // => replace each url with corresponding URI prompt
				if len(before) > 0 {
					converted = append(
						converted,
						gt.PromptFromText(before),
					)
				}
				converted = append(
					converted,
					gt.PromptFromURI(url, `video/mp4`),
				)
			} else { // => keep the original urls as-is
				converted = append(
					converted,
					gt.PromptFromText(before+url),
				)
			}
			remaining = after
		}
	}

	if len(remaining) > 0 {
		converted = append(
			converted,
			gt.PromptFromText(remaining),
		)
	}

	return converted
}

// check if given `url` is from YouTube
func isURLFromYoutube(url string) bool {
	return slices.ContainsFunc([]string{
		"www.youtube.com",
		"youtu.be",
	}, func(e string) bool {
		return strings.Contains(url, e)
	})
}

// convert pcm data to wav
func pcmToWav(
	pcmBytes []byte,
	sampleRate, bitDepth, numChannels int,
) (converted []byte, err error) {
	var buf bytes.Buffer

	// wav header
	dataLen := uint32(len(pcmBytes))
	header := struct {
		ChunkID       [4]byte // "RIFF"
		ChunkSize     uint32
		Format        [4]byte // "WAVE"
		Subchunk1ID   [4]byte // "fmt "
		Subchunk1Size uint32
		AudioFormat   uint16
		NumChannels   uint16
		SampleRate    uint32
		ByteRate      uint32
		BlockAlign    uint16
		BitsPerSample uint16
		Subchunk2ID   [4]byte // "data"
		Subchunk2Size uint32
	}{
		ChunkID:       [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize:     36 + dataLen,
		Format:        [4]byte{'W', 'A', 'V', 'E'},
		Subchunk1ID:   [4]byte{'f', 'm', 't', ' '},
		Subchunk1Size: 16,
		AudioFormat:   1, // PCM
		NumChannels:   uint16(numChannels),
		SampleRate:    uint32(sampleRate),
		ByteRate:      uint32(sampleRate * numChannels * bitDepth / 8),
		BlockAlign:    uint16(numChannels * bitDepth / 8),
		BitsPerSample: uint16(bitDepth),
		Subchunk2ID:   [4]byte{'d', 'a', 't', 'a'},
		Subchunk2Size: dataLen,
	}

	// write wav header
	if err := binary.Write(&buf, binary.LittleEndian, header); err != nil {
		return nil, fmt.Errorf("failed to write wav header: %w", err)
	}

	// write pcm data
	if _, err := buf.Write(pcmBytes); err != nil {
		return nil, fmt.Errorf("failed to write pcm data: %w", err)
	}

	return buf.Bytes(), nil
}

// convert wav to ogg with `ffmpeg`
func wavToOGG(wavBytes []byte) ([]byte, error) {
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-i", "pipe:0", // input from stdin
		"-c:a", "libopus",
		"-b:a", "128k",
		"-f", "ogg",
		"pipe:1", // output to stdout
	)

	cmd.Stdin = bytes.NewReader(wavBytes)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg error: %w (%s)", err, stderr.String())
	}

	return out.Bytes(), nil
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
func convertMessage(
	ctxBg context.Context,
	bot *tg.Bot,
	message tg.Message,
	otherGroupedMessages ...tg.Message,
) (cm *chatMessage, err error) {
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
			if files, err := filesFromMessage(ctxBg, bot, msg); err == nil {
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
func filesFromMessage(
	ctxBg context.Context,
	bot *tg.Bot,
	message tg.Message,
) (files [][]byte, err error) {
	var bytes []byte
	if message.HasPhoto() {
		files = [][]byte{}

		for _, photo := range message.Photo {
			if bytes, err = readMedia(ctxBg, bot, "photo", photo.FileID); err == nil {
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
		if bytes, err = readMedia(ctxBg, bot, "video", message.Video.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read video content: %s", err)
		}
	} else if message.HasVideoNote() {
		if bytes, err = readMedia(ctxBg, bot, "video note", message.VideoNote.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read video note content: %s", err)
		}
	} else if message.HasAudio() {
		if bytes, err = readMedia(ctxBg, bot, "audio", message.Audio.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read audio content: %s", err)
		}
	} else if message.HasVoice() {
		if bytes, err = readMedia(ctxBg, bot, "voice", message.Voice.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read voice content: %s", err)
		}
	} else if message.HasDocument() {
		if bytes, err = readMedia(ctxBg, bot, "document", message.Document.FileID); err == nil {
			return [][]byte{bytes}, nil
		} else {
			err = fmt.Errorf("failed to read document content: %s", err)
		}
	}

	return nil, err
}

// read bytes from given media
func readMedia(
	ctxBg context.Context,
	bot *tg.Bot,
	mediaType, fileID string,
) (result []byte, err error) {
	ctxFile, cancelFile := context.WithTimeout(ctxBg, requestTimeoutSeconds*time.Second)
	defer cancelFile()

	if res := bot.GetFile(ctxFile, fileID); !res.Ok {
		err = fmt.Errorf("failed to read bytes from %s: %s", mediaType, *res.Description)
	} else {
		fileURL := bot.GetFileURL(*res.Result)
		result, err = readFileContentAtURL(ctxBg, fileURL)
	}

	return result, err
}

// read file content at given url
func readFileContentAtURL(
	ctxBg context.Context,
	url string,
) (content []byte, err error) {
	ctxRead, cancelRead := context.WithTimeout(ctxBg, longRequestTimeoutSeconds*time.Second)
	defer cancelRead()

	// generate request
	var req *http.Request
	req, err = http.NewRequestWithContext(ctxRead, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// send request
	var resp *http.Response
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// read response
	content, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// convert chat messages to a prompt for logging
func messagesToPrompt(
	parent, original *chatMessage,
) string {
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
