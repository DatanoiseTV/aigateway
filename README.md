# Gemini Proxy Gateway

A secure Go-based API gateway that proxies requests to Google's Gemini API while providing per-client authentication, rate limiting, and usage quotas.

## Features

- **API Key Authentication**: Individual access tokens for each client
- **Rate Limiting**: Configurable raterequests/min limits per client (ute, hour, day)
- **Quota Management**: Token usage tracking and limits per client
- **Admin Web UI**: Dashboard for managing clients and viewing statistics
- **Security**: Hashed API keys, secure cookies, security headers
- **SQLite Database**: Pure Go, no external database dependencies

## Quick Start

### 1. Build

```bash
go build -o gemini-proxy ./cmd/server
```

### 2. Configure

Copy `config.yaml` and edit with your settings:

```yaml
server:
  host: "0.0.0.0"
  port: 8080

admin:
  username: "admin"
  password_hash: "$2y$10$..."  # bcrypt hash
  session_secret: "change-me"

gemini:
  api_key: "YOUR-GEMINI-API-KEY"
  default_model: "gemini-2.0-flash"
  allowed_models:
    - "gemini-2.0-flash"
    - "gemini-2.0-flash-lite"

defaults:
  rate_limit:
    requests_per_minute: 60
    requests_per_hour: 1000
    requests_per_day: 10000
  quota:
    max_input_tokens_per_day: 1000000
    max_output_tokens_per_day: 500000
    max_requests_per_day: 1000

database:
  path: "./data/gateway.db"
```

Generate a bcrypt password hash:

```bash
go run -exec "" -c 'package main; import "golang.org/x/crypto/bcrypt"; import "fmt"; func main() {hash, _ := bcrypt.GenerateFromPassword([]byte("your-password"), bcrypt.DefaultCost); fmt.Println(string(hash))}'
```

Or use an online bcrypt generator.

### 3. Run

```bash
./gemini-proxy
```

The server will start on `http://localhost:8080`

### 4. Access Admin UI

- URL: `http://localhost:8080/admin`
- Login with the credentials from your config

## API Usage

### Generate Content

```bash
curl -X POST http://localhost:8080/v1beta/models/gemini-2.0-flash:generateContent \
  -H "Authorization: Bearer YOUR-CLIENT-API-KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "Hello, how are you?"}]
    }]
  }'
```

### List Models

```bash
curl http://localhost:8080/v1beta/models \
  -H "Authorization: Bearer YOUR-CLIENT-API-KEY"
```

## Configuration Options

| Option | Description | Default |
|--------|-------------|---------|
| `server.host` | Listen host | `0.0.0.0` |
| `server.port` | Listen port | `8080` |
| `server.https.enabled` | Enable HTTPS | `false` |
| `server.https.cert_file` | TLS certificate path | - |
| `server.https.key_file` | TLS key path | - |
| `admin.username` | Admin username | `admin` |
| `admin.password_hash` | bcrypt hashed password | - |
| `admin.session_secret` | Session secret | - |
| `gemini.api_key` | Your Gemini API key | - |
| `gemini.default_model` | Default model | `gemini-2.0-flash` |
| `gemini.timeout_seconds` | Request timeout | `120` |

## Project Structure

```
.
├── cmd/server/          # Main application entry point
├── internal/
│   ├── config/           # Configuration loading
│   ├── handlers/        # HTTP handlers (proxy + admin)
│   ├── middleware/      # Auth, rate limiting, security
│   ├── models/          # Database models
│   └── services/        # Business logic
├── config.yaml          # Configuration file
├── go.mod
└── README.md
```

## Security Considerations

- API keys are stored as SHA-256 hashes
- Admin sessions use secure, HTTP-only cookies
- All responses include security headers
- Rate limiting prevents abuse
- Request size is limited to 10MB

## License

MIT
