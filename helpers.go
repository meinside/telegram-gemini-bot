// helpers.go
//
// helper functions

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"
	"github.com/meinside/version-go"

	// others
	"github.com/PuerkitoBio/goquery"
	"github.com/tailscale/hujson"
)

const (
	httpUserAgent = `TGB/url2text`

	redactedString                      = `<REDACTED>`
	urlReplacedWithFileAttachmentFormat = `<file fetched-from="%[1]s" content-type="%[2]s">This element was replaced with the file fetched from '%[1]s', and is attached to the prompt as a file.</file>`
)

// redactError given error for logging and/or messaing
func redactError(conf config, err error) (redacted string) {
	redacted = gt.ErrToStr(err)

	if strings.Contains(redacted, *conf.GoogleAIAPIKey) {
		redacted = strings.ReplaceAll(redacted, *conf.GoogleAIAPIKey, redactedString)
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
	bot *tg.Bot,
	message tg.Message,
	otherGroupedMessages ...tg.Message,
) (parent, original *chatMessage, err error) {
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
		*conf.GoogleGenerativeModelForImageGeneration,
		*conf.GoogleGenerativeModelForSpeechGeneration,
		version.Build(version.OS|version.Architecture|version.Revision),
		cmdGenerateImage+" <prompt>", descGenerateImage,
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

// replace all http urls in given prompt to body texts and/or files
func convertPromptWithURLs(
	conf config,
	prompt string,
) (converted string, files [][]byte) {
	files = [][]byte{}

	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if content, contentType, err := fetchURLContent(conf, url); err == nil {
			if supportedHTTPContentType(contentType) {
				// replace url with fetched content
				prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", string(content)), 1)
			} else if supportedFileMimeType(contentType) {
				// replace url with the info,
				prompt = strings.Replace(prompt, url, fmt.Sprintf(urlReplacedWithFileAttachmentFormat, url, contentType), 1)

				// and append the fetched file to files
				files = append(files, content)
			}
		}
	}

	return prompt, files
}

// fetch the content from given url and convert it to text for prompting.
func fetchURLContent(
	conf config,
	url string,
) (content []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(conf.FetchURLTimeoutSeconds) * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return content, contentType, fmt.Errorf("failed to create http request: %s", err)
	}
	req.Header.Set("User-Agent", httpUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return content, contentType, fmt.Errorf("failed to fetch contents from url: %s", err)
	}
	defer resp.Body.Close()

	contentType = resp.Header.Get("Content-Type")

	if resp.StatusCode == 200 {
		if supportedHTTPContentType(contentType) {
			if strings.HasPrefix(contentType, "text/html") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things from HTML
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css styles

					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text()))
				} else {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this HTML document.")
					err = fmt.Errorf("failed to read html document from '%s': %s", url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))
				} else {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this document.")
					err = fmt.Errorf("failed to read %s document from '%s': %s", contentType, url, err)
				}
			}
		} else if supportedFileMimeType(contentType) {
			if content, err = io.ReadAll(resp.Body); err != nil {
				content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this file.")
				err = fmt.Errorf("failed to read %s file from '%s': %s", contentType, url, err)
			}
		} else {
			content = fmt.Appendf(nil, urlToTextFormat, url, contentType, fmt.Sprintf("Content type: %s not supported.", contentType))
			err = fmt.Errorf("content type %s not supported for url: %s", contentType, url)
		}
	} else {
		content = fmt.Appendf(nil, urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode))
		err = fmt.Errorf("http error %d from '%s'", resp.StatusCode, url)
	}

	return content, contentType, err
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

// check if given file's mime type is supported
//
// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#supported_file_formats
func supportedFileMimeType(mimeType string) bool {
	return func(mimeType string) bool {
		switch {
		// images
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#image_formats
		case slices.Contains([]string{
			"image/png",
			"image/jpeg",
			"image/webp",
			"image/heic",
			"image/heif",
		}, mimeType):
			return true
		// audios
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#audio_formats
		case slices.Contains([]string{
			"audio/wav",
			"audio/mp3",
			"audio/aiff",
			"audio/aac",
			"audio/ogg",
			"audio/flac",
		}, mimeType):
			return true
		// videos
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#video_formats
		case slices.Contains([]string{
			"video/mp4",
			"video/mpeg",
			"video/mov",
			"video/avi",
			"video/x-flv",
			"video/mpg",
			"video/webm",
			"video/wmv",
			"video/3gpp",
		}, mimeType):
			return true
		// plain text formats
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#plain_text_formats
		case slices.Contains([]string{
			"text/plain",
			"text/html",
			"text/css",
			"text/javascript",
			"application/x-javascript",
			"text/x-typescript",
			"application/x-typescript",
			"text/csv",
			"text/markdown",
			"text/x-python",
			"application/x-python-code",
			"application/json",
			"text/xml",
			"application/rtf",
			"text/rtf",

			// FIXME: not stated in the document yet
			"application/pdf",
		}, mimeType):
			return true
		default:
			return false
		}
	}(mimeType)
}

// check if given HTTP content type is supported
func supportedHTTPContentType(contentType string) bool {
	return func(contentType string) bool {
		switch {
		case strings.HasPrefix(contentType, "text/"):
			return true
		case strings.HasPrefix(contentType, "application/json"):
			return true
		default:
			return false
		}
	}(contentType)
}

// convert pcm data to wav
func pcmToWav(pcmBytes []byte, sampleRate, bitDepth, numChannels int) (converted []byte, err error) {
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
