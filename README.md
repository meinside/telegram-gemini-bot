# Telegram Gemini Bot

A telegram bot which answers to messages with [Gemini API](https://ai.google.dev/tutorials/go_quickstart).

<img width="562" alt="screenshot1" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/1e126512-761a-4f7d-8925-346caf1b3efb">

You can also reply to messages for keeping the context of your conversation:

<img width="563" alt="screenshot2" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/a0674089-739d-4b80-916e-ceb39d48dd09">
<img width="561" alt="screenshot3" src="https://github.com/meinside/telegram-gemini-bot/assets/185988/3861242a-14fc-495c-a1d7-75caac630e4d">

## Prerequisites

* A [Google API key](https://aistudio.google.com/app/apikey), and
* a machine which can build and run golang applications.

## Configurations

Create a configuration file:

```bash
$ cp config.json.sample config.json
$ vi config.json
```

and set your values:

```json
{
  "google_generative_model": "gemini-pro",

  "allowed_telegram_users": ["user1", "user2"],
  "db_filepath": null,
  "verbose": false,

  "telegram_bot_token": "123456:abcdefghijklmnop-QRSTUVWXYZ7890",
  "google_ai_api_key": "ABCDEFGHIJK1234567890"
}
```

If `db_filepath` is given, all prompts and their responses will be logged to the SQLite3 file.

### Using Infisical

You can use [Infisical](https://infisical.com/) for saving & retrieving your bot token and api key:

```json
{
  "google_generative_model": "gemini-pro",

  "allowed_telegram_users": ["user1", "user2"],
  "db_filepath": null,
  "verbose": false,

  "infisical": {
    "client_id": "012345-abcdefg-987654321",
    "client_secret": "aAbBcCdDeEfFgG0123456789xyzwXYZW",

    "workspace_id": "012345abcdefg",
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

Createa a systemd service file:

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

- `/help` for help message.

## Todos / Known Issues

- [X] Handle returning messages' size limit (Telegram Bot API's limit: [4096 chars](https://core.telegram.org/bots/api#sendmessage))
  - Will send a plain-text document instead of an ordinary text message.

## License

The MIT License (MIT)

Copyright © 2024 Sungjin Han

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

