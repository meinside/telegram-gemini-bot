# Telegram Gemini Bot

A telegram bot which answers to messages with [Gemini API](https://ai.google.dev/tutorials/go_quickstart).

<img width="562" alt="screenshot1" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/1e126512-761a-4f7d-8925-346caf1b3efb">

---

* You can reply to messages for keeping the context of your conversation:

<img width="563" alt="screenshot2" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/a0674089-739d-4b80-916e-ceb39d48dd09">
<img width="561" alt="screenshot3" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/3861242a-14fc-495c-a1d7-75caac630e4d">

---

* You can also upload photos with a caption as a prompt:

<img width="560" alt="screenshot4" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/b54c1d60-0675-444d-812c-c1c303c8dca2">

---

* Generated text will be received part by part with streaming support:

![streamed message](https://github.com/meinside/telegram-gemini-bot/assets/185988/05dda043-8b3f-4fd9-8be0-9c0f5e8076a3)

---

## Prerequisites

* A [Telegram Bot Token](https://telegram.me/BotFather),
* A [Google API key](https://aistudio.google.com/app/apikey), and
* A machine which can build and run golang applications.
* (Optional) [ffmpeg](https://ffmpeg.org/) installed for speech generation.

## Configurations

Create a configuration file:

```bash
$ cp config.json.sample config.json
$ $EDITOR config.json
```

and set your values:

```json
{
  "google_generative_model": "gemini-2.5-flash",
  "google_generative_model_for_image_generation": "gemini-2.0-flash-preview-image-generation",
  "google_generative_model_for_speech_generation": "gemini-2.5-flash-preview-tts",

  "google_ai_harm_block_threshold": "BLOCK_ONLY_HIGH",

  "allowed_telegram_users": ["user1", "user2"],
  "db_filepath": null,
  "answer_timeout_seconds": 180,
  "verbose": false,

  "telegram_bot_token": "123456:abcdefghijklmnop-QRSTUVWXYZ7890",
  "google_ai_api_key": "ABCDEFGHIJK1234567890"
}
```

You can get appropriate model names from [here](https://ai.google.dev/models/gemini).

If `db_filepath` is given, all prompts and their responses will be logged to the SQLite3 file.

### Using Infisical

You can use [Infisical](https://infisical.com/) for saving & retrieving your bot token and api key:

```json
{
  "google_generative_model": "gemini-2.5-flash",
  "google_generative_model_for_image_generation": "gemini-2.0-flash-preview-image-generation",
  "google_generative_model_for_speech_generation": "gemini-2.5-flash-preview-tts",

  "google_ai_harm_block_threshold": "BLOCK_ONLY_HIGH",

  "allowed_telegram_users": ["user1", "user2"],
  "db_filepath": null,
  "answer_timeout_seconds": 180,
  "verbose": false,

  "infisical": {
    "client_id": "012345-abcdefg-987654321",
    "client_secret": "aAbBcCdDeEfFgG0123456789xyzwXYZW",

    "project_id": "012345abcdefg",
    "environment": "dev",
    "secret_type": "shared",

    "telegram_bot_token_key_path": "/path/to/your/KEY_TO_TELEGRAM_BOT_TOKEN",
    "google_ai_api_key_key_path": "/path/to/your/KEY_TO_GOOGLE_AI_API_KEY"
  }
}
```

## Build

```bash
$ go build
```

## Run

Run the built binary with the config file's path:

```bash
$ ./telegram-gemini-bot path-to/config.json
```

## Run as a systemd service

Create a systemd service file:

```
[Unit]
Description=Telegram Gemini Bot
After=syslog.target
After=network.target

[Service]
Type=simple
User=ubuntu
Group=ubuntu
WorkingDirectory=/dir/to/telegram-gemini-bot
ExecStart=/dir/to/telegram-gemini-bot/telegram-gemini-bot /path/to/config.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

and `systemctl` enable|start|restart|stop the service.

## Commands

- `/image <PROMPT>` for image generation.
- `/speech <PROMPT>` for speech generation.
- `/google <PROMPT>` for generation with grounding (Google Search).
- `/stats` for various statistics of this bot.
- `/help` for help message.

## Todos / Known Issues

- [ ] Handle markdown texts gracefully.

