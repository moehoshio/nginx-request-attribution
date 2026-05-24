# Nginx Request Attribution

🌐 **English** | [繁體中文](README.zh-TW.md) | [简体中文](README.zh-CN.md) | [日本語](README.ja.md)

A lightweight Nginx access log analytics tool with statistics dashboard and real-time monitoring.

## Screenshots

| Dark Mode | Light Mode |
|:-:|:-:|
| ![Dark Mode](docs/screenshot-dark.png) | ![Light Mode](docs/screenshot-light.png) |

## Features

- 🚀 **Single Binary** - Compiled with Go into a single executable, no additional runtime needed
- 📊 **Built-in Web GUI** - Statistics dashboard embedded in the binary, no separate frontend deployment required
- 🔍 **Multi-dimensional Filtering** - Filter by IP, path, domain, query parameters, OS, browser, status code, etc.
- 🔑 **Keyword Tracking** - Automatically track occurrences of configured keywords
- 📡 **Real-time Monitoring** - Automatically monitor new log file entries
- 🐳 **One-click Deploy** - Docker / Docker Compose deployment support
- 💾 **SQLite Storage** - Lightweight database, no external database service required
- 🌐 **Multilingual Interface** - Web GUI supports English, Traditional Chinese, and Japanese

## Quick Start

### Option 1: Direct Run

```bash
# Build
go build -o nginx-req-attr ./cmd/

# Import existing logs
./nginx-req-attr -import /var/log/nginx/access.log

# Start service (log monitoring + Web GUI)
./nginx-req-attr -config config.json
```

### Option 2: Docker Deploy

```bash
# One-click start
docker-compose up -d

# Or manual Docker
docker build -t nginx-req-attr .
docker run -d \
  -p 8080:8080 \
  -v /var/log/nginx:/var/log/nginx:ro \
  -v ./data:/app/data \
  nginx-req-attr
```

## Configuration

Create `config.json`:

```json
{
  "log_path": "/var/log/nginx/access.log",
  "log_format": "combined",
  "listen_addr": ":8080",
  "db_path": "./data/stats.db",
  "watch": true,
  "keywords": ["login", "admin", "api", "search"],
  "input_mode": "file",
  "syslog_addr": ":1514",
  "syslog_proto": "udp"
}
```

| Field | Description | Default |
|---|---|---|
| `log_path` | Nginx access log path | `/var/log/nginx/access.log` |
| `log_format` | Log format (combined/vhost_combined) | `combined` |
| `listen_addr` | HTTP server listen address | `:8080` |
| `db_path` | SQLite database file path | `./data/stats.db` |
| `watch` | Enable real-time log monitoring | `true` |
| `keywords` | List of keywords to track | `[]` |
| `input_mode` | Input mode (`file`/`syslog`/`both`) | `file` |
| `syslog_addr` | Syslog listen address | `:1514` |
| `syslog_proto` | Syslog protocol (`udp`/`tcp`/`both`) | `udp` |

### Input Modes

- **`file`** — Uses fsnotify event-driven log file monitoring (default, efficient, no nginx config changes needed)
- **`syslog`** — Starts a syslog receiver to receive nginx logs over the network (ideal for multi-instance aggregation)
- **`both`** — Uses both file monitoring and syslog reception simultaneously

#### Syslog Mode Configuration Example

Add to your Nginx configuration:
```nginx
access_log syslog:server=127.0.0.1:1514,facility=local7,tag=nginx combined;
```

## API Endpoints

### GET /api/stats

Get statistics summary. Supports the following query parameter filters:

| Parameter | Description |
|---|---|
| `start` | Start date (YYYY-MM-DD) |
| `end` | End date (YYYY-MM-DD) |
| `ip` | IP address (fuzzy search) |
| `path` | Path (fuzzy search) |
| `domain` | Domain (fuzzy search) |
| `query` | Query string (fuzzy search) |
| `method` | HTTP method |
| `status` | HTTP status code |
| `os` | Operating system |
| `browser` | Browser |
| `keyword` | Keyword |

**Response Example:**
```json
{
  "total_requests": 12345,
  "top_paths": [{"name": "/api/users", "count": 500}],
  "top_ips": [{"name": "192.168.1.1", "count": 300}],
  "top_domains": [{"name": "example.com", "count": 200}],
  "top_os": [{"name": "Windows", "count": 5000}],
  "top_browsers": [{"name": "Chrome", "count": 8000}],
  "top_keywords": [{"name": "api", "count": 1500}],
  "status_codes": [{"name": "200", "count": 10000}],
  "requests_per_day": [{"date": "2023-10-10", "count": 500}]
}
```

### GET /api/requests

Get request list (paginated). Additional parameters:

| Parameter | Description |
|---|---|
| `limit` | Results per page (default 100) |
| `offset` | Offset |

## Supported Log Formats

### Combined (Default)
```
$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

### Virtual Host Combined
```
$host $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

## Development

```bash
# Run tests
go test ./...

# Build
go build -o nginx-req-attr ./cmd/
```

For detailed development guidelines, testing requirements, and contribution workflow, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT License
