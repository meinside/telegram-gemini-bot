// messages.go
//
// functions for messages

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
	tg "github.com/meinside/telegram-bot-go"

	// others
	"github.com/gabriel-vasile/mimetype"
)

// handle allowed message updates from telegram bot api
func handleMessages(
	ctx context.Context,
	bot *tg.Bot,
	conf config,
	db *Database,
	gtc *gt.Client,
	updates []tg.Update,
	mediaGroupID *string,
	withGoogleSearch bool,
) {
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
		if parent, original, err := chatMessagesFromTGMessage(
			bot,
			*msg,
			otherGroupedMessages...,
		); err == nil {
			if original != nil {
				if e := answer(
					ctx,
					bot,
					conf,
					db,
					gtc,
					parent,
					original,
					chatID,
					userID,
					userNameFromUpdate(update),
					messageID,
					withGoogleSearch,
				); e == nil {
					return
				} else {
					log.Printf("failed to answer message: %s", redactError(conf, e))

					errMessage = fmt.Sprintf("Failed to answer message: %s", redactError(conf, err))
				}
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

	if _, err := sendMessage(
		bot,
		conf,
		errMessage,
		chatID,
		&messageID,
	); err != nil {
		log.Printf("failed to send error message while handling messages: %s", redactError(conf, err))
	}
}

// send given text to the chat
func sendMessage(
	bot *tg.Bot,
	conf config,
	message string,
	chatID int64,
	messageID *int64,
) (sentMessageID int64, err error) {
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

	if res := bot.SendMessage(
		chatID,
		message,
		options,
	); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send message: %s (requested message: %s)", *res.Description, message)
	}

	return sentMessageID, err
}

// update a message in the chat
func updateMessage(
	bot *tg.Bot,
	conf config,
	message string,
	chatID int64,
	messageID int64,
) (err error) {
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

// send given blob data as a photo to the chat
func sendPhoto(
	bot *tg.Bot,
	conf config,
	data []byte,
	chatID int64,
	messageID *int64,
) (sentMessageID int64, err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] sending photo to chat(%d): %d bytes of data", chatID, len(data))
	}

	options := tg.OptionsSendPhoto{}
	if messageID != nil {
		options.SetReplyParameters(tg.ReplyParameters{
			MessageID: *messageID,
		})
	}
	if res := bot.SendPhoto(
		chatID,
		tg.NewInputFileFromBytes(data),
		options,
	); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send photo: %s", *res.Description)
	}

	return sentMessageID, err
}

// send given blob data as a voice to the chat
func sendVoice(
	bot *tg.Bot,
	conf config,
	data []byte,
	chatID int64,
	messageID *int64,
) (sentMessageID int64, err error) {
	_ = bot.SendChatAction(chatID, tg.ChatActionTyping, nil)

	if conf.Verbose {
		log.Printf("[verbose] sending voice to chat(%d): %d bytes of data", chatID, len(data))
	}

	options := tg.OptionsSendVoice{}
	if messageID != nil {
		options.SetReplyParameters(tg.ReplyParameters{
			MessageID: *messageID,
		})
	}
	if res := bot.SendVoice(
		chatID,
		tg.NewInputFileFromBytes(data),
		options,
	); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send voice: %s", *res.Description)
	}

	return sentMessageID, err
}

// send given blob data as a document to the chat
func sendFile(
	bot *tg.Bot,
	conf config,
	data []byte,
	chatID int64,
	messageID *int64,
	caption *string,
) (sentMessageID int64, err error) {
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
	if res := bot.SendDocument(
		chatID,
		tg.NewInputFileFromBytes(data),
		options,
	); res.Ok {
		sentMessageID = res.Result.MessageID
	} else {
		err = fmt.Errorf("failed to send document: %s", *res.Description)
	}

	return sentMessageID, err
}

// generate an answer to given message and send it to the chat
func answer(
	ctx context.Context,
	bot *tg.Bot,
	conf config,
	db *Database,
	gtc *gt.Client,
	parent, original *chatMessage,
	chatID, userID int64,
	username string,
	messageID int64,
	withGoogleSearch bool,
) error {
	errs := []error{}

	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(
		chatID,
		messageID,
		tg.NewMessageReactionWithEmoji("👌"),
	)

	opts := &gt.GenerationOptions{
		HarmBlockThreshold: conf.GoogleAIHarmBlockThreshold,
	}
	if withGoogleSearch {
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
		} else {
			errs = append(errs, fmt.Errorf("file upload failed: %w", err))
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
					if sentMessageID, err := sendMessage(
						bot,
						conf,
						generatedText,
						chatID,
						&messageID,
					); err == nil {
						firstMessageID = &sentMessageID
					} else {
						errs = append(errs, fmt.Errorf("failed to send message: %w", err))
					}
				} else { // update the first message
					// update the first message (append text)
					if err := updateMessage(
						bot,
						conf,
						mergedText,
						chatID,
						*firstMessageID,
					); err != nil {
						errs = append(errs, fmt.Errorf("failed to update message: %w", err))
					}
				}
			}

			// check stream content
			if data.TextDelta != nil {
				generatedText := *data.TextDelta
				mergedText += generatedText

				if firstMessageID == nil { // send the first message
					if sentMessageID, err := sendMessage(
						bot,
						conf,
						generatedText,
						chatID,
						&messageID,
					); err == nil {
						firstMessageID = &sentMessageID
					} else {
						errs = append(errs, fmt.Errorf("failed to send message: %w", err))
					}
				} else { // update the first message
					// update the first message (append text)
					if err := updateMessage(
						bot,
						conf,
						mergedText,
						chatID,
						*firstMessageID,
					); err != nil {
						errs = append(errs, fmt.Errorf("failed to update message: %w", err))
					}
				}
			} else if data.Error != nil {
				errs = append(errs, fmt.Errorf("error from stream: %w", data.Error))

				if _, err := sendMessage(
					bot,
					conf,
					fmt.Sprintf("Stream error: %s", redactError(conf, data.Error)),
					chatID,
					nil,
				); err != nil {
					errs = append(errs, fmt.Errorf("failed to send error message: %w", err))
				}
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
		errs = append(errs, fmt.Errorf("failed to generate stream: %w", err))

		// send error message
		if _, e := sendMessage(
			bot,
			conf,
			fmt.Sprintf("Generation failed: %s", redactError(conf, err)),
			chatID,
			nil,
		); e != nil {
			errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
		}
	}

	// log if it was successful or not
	successful := (func() bool {
		if firstMessageID != nil {
			// leave a reaction on the first message for notifying the termination of the stream
			if result := bot.SetMessageReaction(
				chatID,
				*firstMessageID,
				tg.NewMessageReactionWithEmoji("👌"),
			); !result.Ok {
				errs = append(errs, fmt.Errorf("failed to set message reaction: %s", *result.Description))
			}
			return true
		}
		return false
	})()
	savePromptAndResult(
		db,
		chatID,
		userID,
		username,
		messagesToPrompt(parent, original),
		uint(numTokensInput),
		mergedText,
		uint(numTokensOutput),
		successful,
	)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// generate an image with given message and send it to the chat
func answerWithImage(
	ctx context.Context,
	bot *tg.Bot,
	conf config,
	db *Database,
	gtc *gt.Client,
	parent, original *chatMessage,
	chatID, userID int64,
	username string,
	messageID int64,
) error {
	errs := []error{}

	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(
		chatID,
		messageID,
		tg.NewMessageReactionWithEmoji("👌"),
	)

	opts := &gt.GenerationOptions{
		HarmBlockThreshold: conf.GoogleAIHarmBlockThreshold,
		ResponseModalities: []gt.ResponseModality{
			gt.ResponseModalityText,
			gt.ResponseModalityImage,
		},
	}

	// prompt
	var promptText string
	promptFiles := map[string]io.Reader{}

	if original != nil {
		// text
		promptFilesFromURL := [][]byte{}
		promptText = original.text
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
		} else {
			errs = append(errs, fmt.Errorf("file upload failed: %w", err))
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

	if conf.Verbose {
		log.Printf("[verbose] generating image [%+v] ...", original)
	}

	// generate
	resultAsText := ""
	successful := false
	if generated, err := gtc.Generate(
		ctx,
		prompts,
		opts,
	); err == nil {
		if generated.UsageMetadata != nil {
			numTokensInput = generated.UsageMetadata.PromptTokenCount
			numTokensOutput = generated.UsageMetadata.CandidatesTokenCount
		}

	outer:
		for _, cand := range generated.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.InlineData != nil {
						data := part.InlineData.Data

						mimeType := mimetype.Detect(data).String()
						if strings.HasPrefix(mimeType, "image/") {
							if _, e := sendPhoto(
								bot,
								conf,
								data,
								chatID,
								&messageID,
							); e == nil {
								resultAsText = fmt.Sprintf("%s;%d bytes", mimeType, len(data))
								successful = true
								break outer
							} else {
								errs = append(errs, fmt.Errorf("failed to send image: %w", e))
							}
						} else {
							errs = append(errs, fmt.Errorf("non-image part was received (%s)", mimeType))
						}
					}
				}
			} else if cand.FinishReason != genai.FinishReasonStop {
				if _, e := sendMessage(
					bot,
					conf,
					fmt.Sprintf("Image generation failed with finish reason: %s", cand.FinishReason),
					chatID,
					&messageID,
				); e != nil {
					errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
				}
			}
		}
		if !successful {
			if _, e := sendMessage(
				bot,
				conf,
				"Successfully generated image(s), but send failed.",
				chatID,
				&messageID,
			); e != nil {
				errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
			}
		}
	} else {
		errs = append(errs, fmt.Errorf("failed to generate image: %w", err))

		if _, e := sendMessage(
			bot,
			conf,
			fmt.Sprintf("Image generation failed: %s", redactError(conf, err)),
			chatID,
			&messageID,
		); e != nil {
			errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
		}
	}

	savePromptAndResult(
		db,
		chatID,
		userID,
		username,
		messagesToPrompt(parent, original),
		uint(numTokensInput),
		resultAsText,
		uint(numTokensOutput),
		successful,
	)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// generate a speech with given message and send it to the chat
func answerWithVoice(
	ctx context.Context,
	bot *tg.Bot,
	conf config,
	db *Database,
	gtc *gt.Client,
	parent, original *chatMessage,
	chatID, userID int64,
	username string,
	messageID int64,
) error {
	errs := []error{}

	// leave a reaction on the original message for confirmation
	_ = bot.SetMessageReaction(
		chatID,
		messageID,
		tg.NewMessageReactionWithEmoji("👌"),
	)

	opts := &gt.GenerationOptions{
		HarmBlockThreshold: conf.GoogleAIHarmBlockThreshold,
		ResponseModalities: []gt.ResponseModality{
			gt.ResponseModalityAudio,
		},
	}

	// prompt
	var promptText string
	promptFiles := map[string]io.Reader{}

	if original != nil {
		// text
		promptFilesFromURL := [][]byte{}
		promptText = original.text
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
		} else {
			errs = append(errs, fmt.Errorf("file upload failed: %w", err))
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

	if conf.Verbose {
		log.Printf("[verbose] generating speech [%+v] ...", original)
	}

	// generate
	resultAsText := ""
	successful := false
	if generated, err := gtc.Generate(
		ctx,
		prompts,
		opts,
	); err == nil {
		if generated.UsageMetadata != nil {
			numTokensInput = generated.UsageMetadata.PromptTokenCount
			numTokensOutput = generated.UsageMetadata.CandidatesTokenCount
		}

		errMsg := `Successfully generated a speech, but send failed.`

	outer:
		for _, cand := range generated.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.InlineData != nil {
						// check codec and birtate
						var speechCodec string
						var bitRate int
						for _, split := range strings.Split(part.InlineData.MIMEType, ";") {
							if strings.HasPrefix(split, "codec=") {
								speechCodec = split[6:]
							} else if strings.HasPrefix(split, "rate=") {
								bitRate, _ = strconv.Atoi(split[5:])
							}
						}

						pcmBytes := part.InlineData.Data

						// convert PCM to .wav,
						if speechCodec == "pcm" && bitRate > 0 { // FIXME: only 'pcm' is supported for now
							if wavBytes, err := pcmToWav(
								pcmBytes,
								bitRate,
								wavBitDepth,
								wavNumChannels,
							); err == nil {
								// convert .wav to .ogg,
								if oggBytes, err := wavToOGG(wavBytes); err == nil {
									if _, err = sendVoice(bot, conf, oggBytes, chatID, &messageID); err == nil {
										resultAsText = fmt.Sprintf("%s;%d bytes", mimetype.Detect(oggBytes).String(), len(oggBytes))
										successful = true
										break outer
									} else {
										log.Printf("failed to send speech: %s", err)
									}
								} else {
									log.Printf("failed to convert .wav to .ogg: %s", err)
								}
							} else {
								log.Printf("failed to convert PCM to .wav: %s", err)
							}
						} else {
							errs = append(errs, fmt.Errorf("unsupported part was received (codec: %s, bitrate: %d)", speechCodec, bitRate))
							break outer
						}
					}
				}
			} else if cand.FinishReason != genai.FinishReasonStop {
				if _, e := sendMessage(
					bot,
					conf,
					fmt.Sprintf("Speech generation failed with finish reason: %s", cand.FinishReason),
					chatID,
					&messageID,
				); e != nil {
					errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
				}
			}
		}
		if !successful {
			if _, e := sendMessage(
				bot,
				conf,
				errMsg,
				chatID,
				&messageID,
			); e != nil {
				errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
			}
		}
	} else {
		errs = append(errs, fmt.Errorf("failed to generate speech: %w", err))

		if _, e := sendMessage(
			bot,
			conf,
			fmt.Sprintf("Speech generation failed: %s", redactError(conf, err)),
			chatID,
			&messageID,
		); e != nil {
			errs = append(errs, fmt.Errorf("failed to send error message: %w", e))
		}
	}

	savePromptAndResult(
		db,
		chatID,
		userID,
		username,
		messagesToPrompt(parent, original),
		uint(numTokensInput),
		resultAsText,
		uint(numTokensOutput),
		successful,
	)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
